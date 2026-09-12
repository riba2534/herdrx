package herdr

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type openSSHTransport struct {
	binary   string
	dir      string
	control  string
	ctx      context.Context
	cancel   context.CancelFunc
	master   *exec.Cmd
	done     chan struct{}
	stderr   limitedSSHLog
	mu       sync.Mutex
	forwards map[string]string
	once     sync.Once
}

type limitedSSHLog struct {
	mu   sync.Mutex
	data []byte
}

func (b *limitedSSHLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if left := 16*1024 - len(b.data); left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func (b *limitedSSHLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}

func openSSHArgs(options SSHOptions, control string) ([]string, error) {
	h := options.Host
	validToken := func(s string) bool {
		return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, " \t\r\n\x00/\\@'\"`$;&|<>(){}%")
	}
	if !validToken(h.Hostname) || (h.Username != "" && !validToken(h.Username)) || h.Port < 0 || h.Port > 65535 {
		return nil, fmt.Errorf("invalid system OpenSSH destination")
	}
	if h.SessionName != "" && !publicID.MatchString(h.SessionName) {
		return nil, fmt.Errorf("invalid herdr session name")
	}
	timeout := max(1, int(options.Timeout.Seconds()))
	args := []string{"-M", "-N", "-T", "-S", control,
		"-o", "ControlPersist=no", "-o", "ForkAfterAuthentication=no",
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no",
		"-o", "GSSAPIAuthentication=yes", "-o", "ForwardAgent=no", "-o", "PermitLocalCommand=no",
		"-o", "ClearAllForwardings=yes", "-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=" + strconv.Itoa(timeout)}
	if h.Port != 0 {
		args = append(args, "-p", strconv.Itoa(h.Port))
	}
	if h.Username != "" {
		args = append(args, "-l", h.Username)
	}
	return append(args, "--", h.Hostname), nil
}

func DialOpenSSHEndpoint(ctx context.Context, binary string, options SSHOptions) (*SSHEndpoint, error) {
	if binary == "" {
		return nil, fmt.Errorf("system OpenSSH 未启用，请管理员设置 HERDRX_SSH_BIN 并在工作台主机验证 SSH 与 Kerberos ticket")
	}
	if options.Timeout <= 0 {
		options.Timeout = 15 * time.Second
	}
	dir, err := os.MkdirTemp("", "hx-ssh-")
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	t := &openSSHTransport{binary: binary, dir: dir, control: filepath.Join(dir, "ctl"), ctx: life, cancel: cancel, done: make(chan struct{}), forwards: make(map[string]string)}
	args, err := openSSHArgs(options, t.control)
	if err != nil {
		cancel()
		os.RemoveAll(dir)
		return nil, err
	}
	t.master = openSSHCommand(life, binary, args...)
	t.master.Stderr = &t.stderr
	if err = t.master.Start(); err != nil {
		cancel()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("启动 system OpenSSH 失败，请检查 HERDRX_SSH_BIN: %w", err)
	}
	go func() { _ = t.master.Wait(); cancel(); close(t.done) }()
	ok := false
	defer func() {
		if !ok {
			_ = t.Close()
		}
	}()
	dialCtx, stop := context.WithTimeout(ctx, options.Timeout)
	defer stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(t.control); err == nil {
			break
		}
		select {
		case <-t.done:
			return nil, fmt.Errorf("system OpenSSH 连接失败，请用工作台服务账号检查 known_hosts、kinit 和 SSH 配置: %s", t.stderr.String())
		case <-dialCtx.Done():
			return nil, fmt.Errorf("system OpenSSH 连接超时或取消: %w", dialCtx.Err())
		case <-ticker.C:
		}
	}
	e := &SSHEndpoint{host: options.Host, client: t}
	e.socketPath, err = e.locateSocket(dialCtx)
	if err != nil {
		return nil, err
	}
	ok = true
	return e, nil
}

func (t *openSSHTransport) command(ctx context.Context, args ...string) *exec.Cmd {
	// A dead master must fail, never silently reauthenticate or replay an operation.
	base := []string{"-F", "/dev/null", "-T", "-S", t.control, "-o", "BatchMode=yes", "-o", "ProxyCommand=false"}
	return openSSHCommand(ctx, t.binary, append(base, args...)...)
}

func openSSHCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	configureOpenSSHProcess(cmd)
	// A proxy that detaches from the process group can still inherit stderr.
	// Bound os/exec's pipe drain even when that process outlives the SSH client.
	cmd.WaitDelay = time.Second
	return cmd
}

func (t *openSSHTransport) NewSession() (sshSession, error) {
	if t.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	ctx, cancel := context.WithCancel(t.ctx)
	return &openSSHSession{cmd: t.command(ctx), cancel: cancel}, nil
}

func (t *openSSHTransport) DialContext(ctx context.Context, network, remote string) (net.Conn, error) {
	if network != "unix" || !strings.HasPrefix(remote, "/") || strings.ContainsAny(remote, ":\r\n\x00") {
		return nil, fmt.Errorf("invalid OpenSSH Unix socket forwarding path")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	local, ok := t.forwards[remote]
	if !ok {
		local = filepath.Join(t.dir, "sock"+strconv.Itoa(len(t.forwards)))
		forwardCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(t.ctx, cancel)
		defer cancel()
		defer stop()
		cmd := t.command(forwardCtx, "-O", "forward", "-L", local+":"+remote, "--", "herdrx-mux")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("OpenSSH socket forwarding failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
		t.forwards[remote] = local
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", local)
}

func (t *openSSHTransport) Close() error {
	t.once.Do(func() { t.cancel(); <-t.done; _ = os.RemoveAll(t.dir) })
	return nil
}

type openSSHSession struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	mu     sync.Mutex
	done   chan struct{}
	err    error
	closed bool
	pipes  []io.Closer
}

func (s *openSSHSession) StdinPipe() (io.WriteCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, net.ErrClosed
	}
	pipe, err := s.cmd.StdinPipe()
	if err == nil {
		s.pipes = append(s.pipes, pipe, s.cmd.Stdin.(io.Closer))
	}
	return pipe, err
}
func (s *openSSHSession) StdoutPipe() (io.Reader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, net.ErrClosed
	}
	pipe, err := s.cmd.StdoutPipe()
	if err == nil {
		s.pipes = append(s.pipes, pipe, s.cmd.Stdout.(io.Closer))
	}
	return pipe, err
}
func (s *openSSHSession) StderrPipe() (io.Reader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, net.ErrClosed
	}
	pipe, err := s.cmd.StderrPipe()
	if err == nil {
		s.pipes = append(s.pipes, pipe, s.cmd.Stderr.(io.Closer))
	}
	return pipe, err
}
func (s *openSSHSession) setStderr(w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && s.done == nil {
		s.cmd.Stderr = w
	}
}

// os/exec closes these descriptors after Start/Wait, but not when a session is
// cancelled before Start. Keep both ends so every cancellation path releases them.
func (s *openSSHSession) closePipesLocked() {
	for _, pipe := range s.pipes {
		_ = pipe.Close()
	}
	s.pipes = nil
}
func (s *openSSHSession) Start(command string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	if s.done != nil {
		return fmt.Errorf("OpenSSH session already started")
	}
	s.cmd.Args = append(s.cmd.Args, "--", "herdrx-mux", command)
	if err := s.cmd.Start(); err != nil {
		s.closed = true
		s.closePipesLocked()
		s.cancel()
		return err
	}
	s.done = make(chan struct{})
	return nil
}
func (s *openSSHSession) Output(command string) ([]byte, error) {
	var output bytes.Buffer
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, net.ErrClosed
	}
	if s.done != nil || s.cmd.Stdout != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("OpenSSH session output already configured or started")
	}
	s.cmd.Stdout = &output
	s.mu.Unlock()
	if err := s.Start(command); err != nil {
		return nil, err
	}
	err := s.Wait()
	return output.Bytes(), err
}
func (s *openSSHSession) Wait() error {
	s.mu.Lock()
	done := s.done
	if done == nil {
		s.mu.Unlock()
		return fmt.Errorf("OpenSSH session not started")
	}
	if !s.closed {
		s.closed = true
		s.mu.Unlock()
		err := s.cmd.Wait()
		s.mu.Lock()
		s.err = err
		s.closePipesLocked()
		s.cancel()
		close(done)
	}
	s.mu.Unlock()
	<-done
	return s.err
}
func (s *openSSHSession) Close() error {
	s.cancel()
	s.mu.Lock()
	started := s.done != nil
	if !started {
		s.closed = true
		s.closePipesLocked()
	}
	s.mu.Unlock()
	if started {
		_ = s.Wait()
	}
	return nil
}
