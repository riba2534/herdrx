package agentcli

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

type enrollmentProcess struct {
	t                         *testing.T
	bin, config, runtime, dir string
	env                       []string
	cmd                       *exec.Cmd
	log                       *os.File
}

func newEnrollmentProcess(t *testing.T) *enrollmentProcess {
	t.Helper()
	dir := t.TempDir()
	f := &enrollmentProcess{t: t, dir: dir, bin: filepath.Join(dir, "herdrx"), config: filepath.Join(dir, "c", "agent.json"), runtime: filepath.Join(dir, "r")}
	buildCLI(t, f.bin)
	fakeHerdr, _ := createMockHerdrFixture(t, dir, filepath.Join(dir, "c"))
	f.env = isolatedEnv(filepath.Join(dir, "c"), f.runtime, filepath.Join(dir, "s"))
	f.run("setup", "--skip-service", "--herdr-bin", fakeHerdr)
	dm := tunnel.RunTestDERPAndSTUN(t, func(string, ...any) {}, "127.0.0.1")
	store, err := NewStateStore(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(func(c *Config) error { c.Node.Public.Region = []*tailcfg.DERPRegion{dm.Regions[1]}; return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.stop)
	f.start()
	return f
}

func (f *enrollmentProcess) run(args ...string) string {
	f.t.Helper()
	args = append(args, "--config", f.config, "--runtime-dir", f.runtime)
	cmd := exec.Command(f.bin, args...)
	cmd.Env = f.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		f.t.Fatalf("CLI %s: %v (stderr %s)", args[0], err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func (f *enrollmentProcess) start() {
	f.t.Helper()
	var err error
	f.log, err = os.CreateTemp(f.dir, "daemon-*.log")
	if err != nil {
		f.t.Fatal(err)
	}
	f.cmd = exec.Command(f.bin, "serve", "--config", f.config, "--runtime-dir", f.runtime)
	f.cmd.Env, f.cmd.Stdout, f.cmd.Stderr = f.env, f.log, f.log
	if err := f.cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	waitDaemonReady(f.t, f.runtime, f.config, f.cmd.Process.Pid, 20*time.Second)
}

func (f *enrollmentProcess) stop() {
	if f.cmd == nil {
		return
	}
	_ = f.cmd.Process.Kill()
	_ = f.cmd.Wait()
	_ = f.log.Close()
	data, _ := os.ReadFile(f.log.Name())
	if bytes.Contains(data, []byte("WARNING: DATA RACE")) {
		f.t.Error("race detected in source-built daemon subprocess")
	}
	if f.t.Failed() {
		f.t.Logf("daemon log (%s):\n%s", f.log.Name(), data)
	}
	f.cmd = nil
}

func dialEnrollmentSSH(t *testing.T, p *tunnel.ParsedConnection) *ssh.Client {
	t.Helper()
	client := tailcat.NewClient(tailcat.Addr(p.Payload.TailcatAddr))
	client.Key, client.Logf = p.ClientNode, func(string, ...any) {}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, err := client.DialTCPPort(ctx, 22)
	if err != nil {
		t.Fatalf("restored enrollment dial: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, "", &ssh.ClientConfig{Auth: []ssh.AuthMethod{ssh.Password(p.Payload.PairSecret)}, HostKeyCallback: ssh.FixedHostKey(p.SSHHostKey)})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("restored enrollment SSH: %v", err)
	}
	_ = conn.SetDeadline(time.Time{})
	cl := ssh.NewClient(sshConn, chans, reqs)
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

func TestCLI_CrashRecovery_EnrollmentRestart(t *testing.T) {
	f := newEnrollmentProcess(t)
	original := f.run("connect", "--plain")
	parsed, err := tunnel.ParseConnectionString(original)
	if err != nil {
		t.Fatal(err)
	}
	f.stop()
	f.start()
	client := dialEnrollmentSSH(t, parsed)
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	_, pub, err := secure.GenerateSSHKey("controller-recovery")
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.Output(fmt.Sprintf("pairing-prepare recovery-request recovery-controller %s %s", key.NewNode().Public().String(), hex.EncodeToString(pub)))
	if err != nil {
		t.Fatalf("prepare after enrollment restart: %v", err)
	}
	if !strings.Contains(string(out), "status=prepared") {
		t.Fatal("restored endpoint did not prepare")
	}
}

func TestCLI_ConnectAfterRestartReusesUnexpiredString(t *testing.T) {
	f := newEnrollmentProcess(t)
	original := f.run("connect", "--plain")
	f.stop()
	f.start()
	if repeated := f.run("connect", "--plain"); repeated != original {
		t.Fatal("connect after restart silently replaced the previously issued enrollment")
	}
}
