//go:build linux

package hostruntime

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
	"tailscale.com/wgengine/filter"
)

// These opt-in measurements use real transports and a separate fixture process.
// They measure access-pool overhead, not physical hosts or terminal rendering.
type capacityFixture struct {
	Port       int
	HostKey    string
	Address    string
	Region     *tailcfg.DERPRegion
	ClientKeys []key.NodePrivate
}

func TestCapacityFixtureProcess(t *testing.T) {
	if os.Getenv("HERDRX_CAPACITY_FIXTURE") != "1" {
		t.Skip("invoked only by the isolated capacity harness")
	}
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	sshConfig := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }, PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	sshConfig.AddHostKey(signer)
	var mu sync.Mutex
	connections := map[*ssh.ServerConn]bool{}
	serve := func(network net.Conn) {
		defer network.Close()
		connection, channels, requests, err := ssh.NewServerConn(network, sshConfig)
		if err != nil {
			return
		}
		defer connection.Close()
		mu.Lock()
		connections[connection] = true
		mu.Unlock()
		defer func() { mu.Lock(); delete(connections, connection); mu.Unlock() }()
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			if incoming.ChannelType() != "session" && incoming.ChannelType() != "direct-streamlocal@openssh.com" {
				_ = incoming.Reject(ssh.UnknownChannelType, "fixture rejects unsupported operations")
				continue
			}
			channel, requests, err := incoming.Accept()
			if err != nil {
				continue
			}
			go func(kind string, channel ssh.Channel, requests <-chan *ssh.Request) {
				defer channel.Close()
				if kind == "session" {
					for request := range requests {
						var command struct{ Command string }
						if request.Type != "exec" || ssh.Unmarshal(request.Payload, &command) != nil || !strings.HasPrefix(command.Command, "sh -c 'printf") {
							_ = request.Reply(false, nil)
							continue
						}
						_ = request.Reply(true, nil)
						_, _ = io.WriteString(channel, "/tmp/isolated-herdrx-capacity\n\n")
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						return
					}
					return
				}
				go ssh.DiscardRequests(requests)
				var request struct{ ID, Method string }
				if json.NewDecoder(io.LimitReader(channel, 4096)).Decode(&request) != nil || request.Method != "session.snapshot" {
					return
				}
				_ = json.NewEncoder(channel).Encode(map[string]any{"id": request.ID, "result": map[string]any{"snapshot": herdr.Snapshot{Protocol: 22, Version: "capacity-fixture", Workspaces: []herdr.Workspace{{ID: "workspace-fixture", PaneCount: 1}}, Panes: []herdr.Pane{{ID: "pane-fixture", AgentStatus: "idle"}}}}})
			}(incoming.ChannelType(), channel, requests)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serve(conn)
		}
	}()
	fixture := capacityFixture{Port: listener.Addr().(*net.TCPAddr).Port, HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))}
	if os.Getenv("HERDRX_CAPACITY_TRANSPORT") == "tailcat" {
		fixture.Region = tunnel.RunTestDERPAndSTUN(t, logger.Discard, "127.0.0.1").Regions[1]
		allowed := []key.NodePublic{}
		for range 100 {
			k := key.NewNode()
			fixture.ClientKeys = append(fixture.ClientKeys, k)
			allowed = append(allowed, k.Public())
		}
		server := &tailcat.Server{Key: key.NewNode(), PresharedKey: tailcat.NewPresharedKey(), Region: fixture.Region, AllowedClients: allowed, Logf: logger.Discard, ServedTCPPorts: []filter.PortRange{{First: 22, Last: 22}}, OnTCP: func(port uint16) func(net.Conn) {
			if port == 22 {
				return serve
			}
			return nil
		}}
		if err := server.Start(); err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		fixture.Address = string(server.TailcatAddr())
	}
	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(fixture); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if scanner.Text() != "drop" {
			break
		}
		mu.Lock()
		current := make([]*ssh.ServerConn, 0, len(connections))
		for conn := range connections {
			current = append(current, conn)
		}
		mu.Unlock()
		for _, conn := range current {
			_ = conn.Close()
		}
		if err := encoder.Encode(map[string]bool{"dropped": true}); err != nil {
			t.Fatal(err)
		}
	}
}

type capacitySample struct {
	RSSMiB     float64   `json:"rss_mib"`
	FD         int       `json:"fd"`
	Goroutines int       `json:"goroutines"`
	CPUSeconds float64   `json:"cpu_seconds"`
	Pool       PoolStats `json:"pool"`
}

func sampleCapacity(t *testing.T, factory *Factory) capacitySample {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(raw))
	pages, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	seconds := float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
	return capacitySample{pages * float64(os.Getpagesize()) / (1 << 20), len(files), runtime.NumGoroutine(), seconds, factory.Stats()}
}

func TestCapacityMeasured(t *testing.T) {
	count, err := strconv.Atoi(os.Getenv("HERDRX_CAPACITY_HOSTS"))
	if err != nil {
		t.Skip("run scripts/test-capacity.py to collect resource evidence")
	}
	transport := os.Getenv("HERDRX_CAPACITY_TRANSPORT")
	if count != 10 && count != 50 && count != 100 || transport != "ssh" && transport != "tailcat" {
		t.Fatal("invalid capacity parameters")
	}
	duration, err := time.ParseDuration(os.Getenv("HERDRX_CAPACITY_DURATION"))
	if err != nil || duration < 2*time.Second {
		t.Fatal("invalid observation duration")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestCapacityFixtureProcess$", "-test.timeout=5m")
	command.Env = append(os.Environ(), "HERDRX_CAPACITY_FIXTURE=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = command.Process.Kill(); _ = command.Wait() })
	decoder := json.NewDecoder(stdout)
	var fixture capacityFixture
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Region != nil {
		for _, node := range fixture.Region.Nodes {
			for _, port := range []int{node.DERPPort, node.STUNPort} {
				tunnel.DefaultSSRFValidator.AllowPrivateEndpoint(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)))
			}
		}
		defer tunnel.DefaultSSRFValidator.ClearAllowed()
	}
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := secure.OpenVault(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := db.CreateUser(ctx, store.User{ID: "capacity-owner", Email: "capacity@example.test", DisplayName: "Capacity", Role: "admin", PasswordHash: "fixture-only"}); err != nil {
		t.Fatal(err)
	}
	sshPrivate, _, err := secure.GenerateSSHKey("capacity-fixture")
	if err != nil {
		t.Fatal(err)
	}
	hosts := make([]store.Host, count)
	for i := range hosts {
		hosts[i] = store.Host{ID: fmt.Sprintf("capacity-%d", i), OwnerID: "capacity-owner", Name: fmt.Sprintf("Capacity %d", i), Transport: transport, Hostname: "127.0.0.1", Port: fixture.Port, Username: "capacity", AuthMethod: "password", CredentialID: fmt.Sprintf("credential-%d", i), HostKey: fixture.HostKey}
		secret := []byte("fixture-only")
		if transport == "tailcat" {
			secret, err = json.Marshal(TailcatCredential{NodePrivate: fixture.ClientKeys[i], SSHPrivate: string(sshPrivate), DialFormalAddr: fixture.Address})
			if err != nil {
				t.Fatal(err)
			}
		}
		ciphertext, err := SealCredential(vault, hosts[i].OwnerID, hosts[i].CredentialID, transport, secret)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveHost(ctx, hosts[i], &store.Credential{ID: hosts[i].CredentialID, OwnerID: hosts[i].OwnerID, Kind: transport, Ciphertext: ciphertext}, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Only idle timeout is shortened for collection; hot/dial limits are defaults.
	factory := &Factory{Store: db, Vault: vault, Config: config.Config{HostIdleTimeout: time.Second}}
	defer factory.Close()
	before := sampleCapacity(t, factory)
	var peakDial atomic.Int32
	stopMonitor := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopMonitor:
				return
			case <-ticker.C:
				value := int32(factory.Stats().Dialing)
				for old := peakDial.Load(); value > old && !peakDial.CompareAndSwap(old, value); old = peakDial.Load() {
				}
			}
		}
	}()
	defer func() { close(stopMonitor); <-monitorDone }()
	endpoints := make([]herdr.Endpoint, count)
	errorsByHost := make([]error, count)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range hosts {
		wg.Add(1)
		go func(i int) { defer wg.Done(); endpoints[i], errorsByHost[i] = factory.Open(ctx, hosts[i]) }(i)
	}
	wg.Wait()
	connectMS := float64(time.Since(start).Microseconds()) / 1000
	active, rejected := 0, 0
	for i, err := range errorsByHost {
		if err != nil {
			if DescribeError(err).Code != "host_capacity" {
				t.Fatalf("open host %d: %v", i, err)
			}
			rejected++
		} else {
			active++
		}
	}
	if active != min(count, 20) || rejected != max(count-20, 0) {
		t.Fatalf("active=%d rejected=%d", active, rejected)
	}
	ready := sampleCapacity(t, factory)
	var calls atomic.Int64
	poll := func() {
		t.Helper()
		var group sync.WaitGroup
		failures := make(chan error, active)
		for _, endpoint := range endpoints {
			if endpoint != nil {
				group.Add(1)
				go func(endpoint herdr.Endpoint) {
					defer group.Done()
					c, stop := context.WithTimeout(ctx, 5*time.Second)
					defer stop()
					snapshot, err := endpoint.Snapshot(c)
					if err != nil {
						failures <- err
						return
					}
					if len(snapshot.Panes) != 1 {
						failures <- fmt.Errorf("snapshot lost pane")
					}
					calls.Add(1)
				}(endpoint)
			}
		}
		group.Wait()
		close(failures)
		for err := range failures {
			t.Fatal(err)
		}
	}
	samples := []capacitySample{ready}
	steadyStart := time.Now()
	for time.Since(steadyStart) < duration {
		poll()
		samples = append(samples, sampleCapacity(t, factory))
		time.Sleep(time.Second)
	}
	steadyEnd := sampleCapacity(t, factory)
	steadyCPU := (steadyEnd.CPUSeconds - ready.CPUSeconds) / time.Since(steadyStart).Seconds() * 100
	if _, err := io.WriteString(stdin, "drop\n"); err != nil {
		t.Fatal(err)
	}
	var dropped map[string]bool
	if err := decoder.Decode(&dropped); err != nil || !dropped["dropped"] {
		t.Fatal("fixture did not drop transports", err)
	}
	recoverStart := time.Now()
	for i, endpoint := range endpoints {
		if endpoint == nil {
			continue
		}
		factory.CloseHost(hosts[i].ID)
		_ = endpoint.Close()
	}
	for i, endpoint := range endpoints {
		if endpoint == nil {
			continue
		}
		wg.Add(1)
		go func(i int) { defer wg.Done(); endpoints[i], errorsByHost[i] = factory.Open(ctx, hosts[i]) }(i)
	}
	wg.Wait()
	for i, err := range errorsByHost {
		if err != nil && DescribeError(err).Code != "host_capacity" {
			t.Fatalf("recovery host %d: %v", i, err)
		}
	}
	poll()
	recoveryMS := float64(time.Since(recoverStart).Microseconds()) / 1000
	for _, endpoint := range endpoints {
		if endpoint != nil {
			_ = endpoint.Close()
		}
	}
	time.Sleep(1500 * time.Millisecond)
	idle := sampleCapacity(t, factory)
	if idle.Pool.Connections != 0 || idle.Pool.Pending != 0 || peakDial.Load() > 4 {
		t.Fatal("capacity or idle collection limit failed")
	}
	result := map[string]any{"transport": transport, "configured_hosts": count, "active": active, "capacity_rejections": rejected, "gomaxprocs": runtime.GOMAXPROCS(0), "go": runtime.Version(), "architecture": runtime.GOARCH, "before": before, "ready": ready, "samples": samples, "steady_cpu_one_core_percent": steadyCPU, "snapshot_calls": calls.Load(), "connect_ms": connectMS, "recovery_after_forced_transport_invalidation_ms": recoveryMS, "max_concurrent_dials": peakDial.Load(), "after_idle": idle, "observation_seconds": duration.Seconds()}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("CAPACITY_RESULT %s\n", raw)
}
