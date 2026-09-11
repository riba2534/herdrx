package agentcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

func TestEndpointMigrationKeepsIdentityAcrossDaemonRestart(t *testing.T) {
	oldMap := tunnel.RunTestDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	newMap := tunnel.RunTestDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	for _, region := range []*tailcfg.DERPRegion{oldMap.Regions[1], newMap.Regions[1]} {
		node := region.Nodes[0]
		for _, port := range []int{node.DERPPort, node.STUNPort} {
			tunnel.DefaultSSRFValidator.AllowPrivateEndpoint(netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", port)))
		}
	}
	defer tunnel.DefaultSSRFValidator.ClearAllowed()
	dir := shortTempDir(t)
	node := tailcat.NewPrivateKey()
	node.Public.Region = []*tailcfg.DERPRegion{oldMap.Regions[1]}
	hostPrivate, hostPublic, err := secure.GenerateSSHKey("endpoint-root")
	if err != nil {
		t.Fatal(err)
	}
	_, controllerSSH, err := secure.GenerateSSHKey("endpoint-controller")
	if err != nil {
		t.Fatal(err)
	}
	clientKey := key.NewNode()
	cfg := Config{Version: 1, Node: *node, PresharedKey: node.Public.PresharedKey, SSHHostPrivate: string(hostPrivate), SetupID: "agent-endpoint", Paired: true, Binding: agent.BindingConfig{Status: "active", BindingID: "binding-endpoint", ControllerID: "controller-endpoint", FormalClientNode: clientKey.Public().String(), FormalSSHPublicKey: strings.TrimSpace(string(controllerSSH)), FormalTailcatAddr: string(node.Public.Addr()), Epoch: 42}}
	path := filepath.Join(dir, "config.json")
	if err := SafeSaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	env := Environment{ConfigDir: dir, RuntimeDir: dir, HomeDir: dir}
	server := NewIPCServer(env, store)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { server.Close() }()
	ping := func(address string) {
		t.Helper()
		client := tailcat.NewClient(tailcat.Addr(address))
		client.Key = clientKey
		client.Logf = logger.Discard
		defer client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := client.Ping(ctx); err != nil {
			t.Fatal("formal endpoint not reachable", err)
		}
	}
	ping(cfg.Binding.FormalTailcatAddr)
	before, _ := json.Marshal(cfg.Binding)
	result, err := server.refreshEndpoint(newMap.Regions[1])
	if err != nil {
		t.Fatal(err)
	}
	identity := tunnel.EndpointIdentity{AgentID: cfg.SetupID, ControllerID: cfg.Binding.ControllerID, BindingID: cfg.Binding.BindingID, ClientNode: cfg.Binding.FormalClientNode, Address: cfg.Binding.FormalTailcatAddr, SSHHostKey: string(hostPublic)}
	verified, err := tunnel.VerifyEndpointUpdate(result.Update, identity)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 1 || verified.Address == cfg.Binding.FormalTailcatAddr {
		t.Fatal("migration did not change endpoint")
	}
	ping(verified.Address)
	saved, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Node.Private.Equal(cfg.Node.Private) || saved.FormalPSK() != cfg.FormalPSK() || saved.SSHHostPrivate != cfg.SSHHostPrivate || saved.Binding.Epoch != cfg.Binding.Epoch {
		t.Fatal("endpoint migration changed identity or authorization epoch")
	}
	binding := saved.Binding
	binding.FormalTailcatAddr = cfg.Binding.FormalTailcatAddr
	after, _ := json.Marshal(binding)
	if !bytes.Equal(before, after) {
		t.Fatal("endpoint migration changed controller authorization")
	}
	server.Close()
	server = NewIPCServer(env, store)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	ping(verified.Address)
	// Exercise the real CLI and IPC path after restart, with an exact config path.
	var stdout, stderr bytes.Buffer
	if code := runConnect([]string{"--config", path, "--refresh-endpoint", "--plain"}, &stdout, &stderr, env, path); code != 0 {
		t.Fatalf("refresh CLI failed: %s", stderr.String())
	}
	identity.MinimumVersion = 1
	refreshed, err := tunnel.VerifyEndpointUpdate(strings.TrimSpace(stdout.String()), identity)
	if err != nil || refreshed.Revision != 2 || refreshed.Address != verified.Address {
		t.Fatal("endpoint revision/address did not survive restart", err)
	}
	if _, err := store.Update(func(c *Config) error {
		c.Revoked = true
		c.Binding.Status = "revoked"
		c.Binding.Epoch = 43
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.refreshEndpoint(nil); err == nil {
		t.Fatal("revoked binding exported an endpoint authorization")
	}
}

func TestDERPConfigRejectsMalformedDataAndProbeChecksProtocol(t *testing.T) {
	for _, data := range []string{`{"RegionID":1,"Nodes":[]}`, `{"RegionID":1,"Unknown":true}`, `{"RegionID":1,"Nodes":[{"HostName":"169.254.169.254"}]}`, `{"RegionID":1,"Nodes":[{"HostName":"203.0.113.20","InsecureForTests":true}]}`} {
		if _, err := tunnel.ParseDERPConfig([]byte(data)); err == nil {
			t.Fatal("invalid DERP configuration accepted")
		}
	}
	dm := tunnel.RunTestDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	node := dm.Regions[1].Nodes[0]
	for _, port := range []int{node.DERPPort, node.STUNPort} {
		tunnel.DefaultSSRFValidator.AllowPrivateEndpoint(netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", port)))
	}
	defer tunnel.DefaultSSRFValidator.ClearAllowed()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := tunnel.ProbeDERPRegion(ctx, dm.Regions[1])
	if len(results) != 1 || !results[0].Reachable || results[0].Stage != "DERP/TLS" {
		t.Fatalf("real DERP handshake was not reported: %+v", results)
	}
}

func TestSetupDERPConfigIsRepeatableAndCannotMoveActiveBinding(t *testing.T) {
	dir := shortTempDir(t)
	bin, _ := createMockHerdrFixture(t, dir, filepath.Join(dir, "fixture-config"))
	env := Environment{HomeDir: dir, ConfigDir: filepath.Join(dir, "config"), RuntimeDir: filepath.Join(dir, "run"), HerdrBin: bin, CommandRunner: fixtureCommandRunner}
	configPath := filepath.Join(env.ConfigDir, "config.json")
	regionPath := filepath.Join(dir, "derp.json")
	region := []byte(`{"RegionID":7,"Nodes":[{"Name":"relay","HostName":"203.0.113.20","STUNPort":-1}]}`)
	if err := os.WriteFile(regionPath, region, 0600); err != nil {
		t.Fatal(err)
	}
	run := func() int {
		t.Helper()
		var out, errOut bytes.Buffer
		code := runSetup([]string{"--skip-service", "--derp-config", regionPath}, &out, &errOut, env, configPath)
		if code != 0 {
			t.Log(errOut.String())
		}
		return code
	}
	if code := run(); code != 0 {
		t.Fatal("initial setup rejected valid region")
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if code := run(); code != 0 {
		t.Fatal("repeated setup failed")
	}
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("repeated setup rewrote identity")
	}
	cfg, err := SafeLoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FormalPSK().IsZero() || len(cfg.Node.Public.Region) != 1 || cfg.Node.Public.Region[0].Nodes[0].IPv4 != "203.0.113.20" {
		t.Fatal("DERP config was not pinned and persisted")
	}
	cfg.Paired = true
	cfg.Binding = agent.BindingConfig{Status: "active", FormalClientNode: key.NewNode().Public().String(), FormalSSHPublicKey: "fixture"}
	if err := SafeSaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	before, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.ReplaceAll(region, []byte("203.0.113.20"), []byte("203.0.113.21"))
	if err := os.WriteFile(regionPath, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if code := run(); code == 0 {
		t.Fatal("setup silently migrated an active binding")
	}
	after, err = os.ReadFile(configPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected region change rewrote active identity")
	}
}
