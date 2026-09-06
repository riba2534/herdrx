package herdr

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"golang.org/x/crypto/ssh"
)

type UnknownHostKeyError struct {
	Key         string
	Fingerprint string
}

func (e *UnknownHostKeyError) Error() string {
	return "SSH host key confirmation required: " + e.Fingerprint
}

type SSHOptions struct {
	Host      store.Host
	Secret    []byte
	Timeout   time.Duration
	OnHostKey func(key, fingerprint string)
}

type SSHEndpoint struct {
	host       store.Host
	client     *ssh.Client
	socketPath string
	extraClose io.Closer
}

func DialSSHEndpoint(ctx context.Context, options SSHOptions) (*SSHEndpoint, error) {
	if options.Timeout == 0 {
		options.Timeout = 15 * time.Second
	}
	target := net.JoinHostPort(options.Host.Hostname, strconv.Itoa(options.Host.Port))
	dialer := net.Dialer{Timeout: options.Timeout, KeepAlive: 15 * time.Second}
	networkConn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, fmt.Errorf("dial SSH %s: %w", target, err)
	}
	return DialSSHOnConn(ctx, networkConn, target, options, nil)
}

func DialSSHOnConn(ctx context.Context, networkConn net.Conn, target string, options SSHOptions, extraClose io.Closer) (*SSHEndpoint, error) {
	if options.Timeout == 0 {
		options.Timeout = 15 * time.Second
	}
	stopDial := context.AfterFunc(ctx, func() { _ = networkConn.Close() })
	defer stopDial()
	auth, err := sshAuth(options.Host.AuthMethod, options.Secret)
	if err != nil {
		_ = networkConn.Close()
		if extraClose != nil {
			_ = extraClose.Close()
		}
		return nil, err
	}
	config := &ssh.ClientConfig{
		User: options.Host.Username, Auth: []ssh.AuthMethod{auth}, Timeout: options.Timeout,
		HostKeyCallback: hostKeyCallback(options.Host.HostKey, options.OnHostKey),
	}
	if deadlineConn, ok := networkConn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = deadlineConn.SetDeadline(time.Now().Add(options.Timeout))
		defer deadlineConn.SetDeadline(time.Time{})
	}
	connection, channels, requests, err := ssh.NewClientConn(networkConn, target, config)
	if err != nil {
		_ = networkConn.Close()
		if extraClose != nil {
			_ = extraClose.Close()
		}
		return nil, fmt.Errorf("SSH handshake: %w", err)
	}
	client := ssh.NewClient(connection, channels, requests)
	endpoint := &SSHEndpoint{host: options.Host, client: client, extraClose: extraClose}
	endpoint.socketPath, err = endpoint.locateSocket(ctx)
	if err != nil {
		_ = endpoint.Close()
		return nil, err
	}
	return endpoint, nil
}

func sshAuth(method string, secret []byte) (ssh.AuthMethod, error) {
	switch method {
	case "saved_key", "private_key_bundle":
		var material secure.SSHKeyMaterial
		if err := json.Unmarshal(secret, &material); err != nil {
			return nil, fmt.Errorf("invalid saved SSH key")
		}
		signer, _, err := material.Parse()
		if err != nil {
			return nil, err
		}
		return ssh.PublicKeys(signer), nil
	case "password":
		if len(secret) == 0 {
			return nil, fmt.Errorf("SSH password is empty")
		}
		return ssh.Password(string(secret)), nil
	case "generated", "private_key":
		signer, err := ssh.ParsePrivateKey(secret)
		if err != nil {
			return nil, fmt.Errorf("parse SSH private key: %w", err)
		}
		return ssh.PublicKeys(signer), nil
	default:
		return nil, fmt.Errorf("unsupported SSH authentication method")
	}
}

func hostKeyCallback(expected string, onHostKey func(string, string)) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		encoded := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		fingerprint := ssh.FingerprintSHA256(key)
		if expected == "" {
			if onHostKey != nil {
				onHostKey(encoded, fingerprint)
			}
			return &UnknownHostKeyError{Key: encoded, Fingerprint: fingerprint}
		}
		if subtleString(expected, encoded) {
			return nil
		}
		return fmt.Errorf("SSH host key changed: expected %s, received %s", fingerprintForEncoded(expected), fingerprint)
	}
}

func subtleString(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func fingerprintForEncoded(encoded string) string {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(encoded))
	if err != nil {
		return "invalid stored key"
	}
	return ssh.FingerprintSHA256(key)
}

// Cancel only this channel request. A late channel is closed without affecting
// other users of the shared transport; its transport idle timeout bounds cleanup.
func (e *SSHEndpoint) newSession(ctx context.Context) (*ssh.Session, error) {
	type result struct {
		session *ssh.Session
		err     error
	}
	ready := make(chan result)
	go func() {
		session, err := e.client.NewSession()
		select {
		case ready <- result{session, err}:
		case <-ctx.Done():
			if session != nil {
				_ = session.Close()
			}
		}
	}()
	select {
	case value := <-ready:
		return value.session, value.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *SSHEndpoint) locateSocket(ctx context.Context) (string, error) {
	session, err := e.newSession(ctx)
	if err != nil {
		return "", fmt.Errorf("open SSH session for socket discovery: %w", err)
	}
	defer session.Close()
	output, err := session.Output(`sh -c 'printf "%s\n%s\n" "$HOME" "${XDG_CONFIG_HOME:-}"'`)
	if err != nil {
		return "", fmt.Errorf("discover remote config path: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(output), "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "/") {
		return "", fmt.Errorf("remote HOME is not an absolute path")
	}
	configRoot := ""
	if len(lines) > 1 {
		configRoot = strings.TrimSpace(lines[1])
	}
	if configRoot == "" {
		configRoot = strings.TrimRight(lines[0], "/") + "/.config"
	}
	if !strings.HasPrefix(configRoot, "/") {
		return "", fmt.Errorf("remote XDG_CONFIG_HOME is not an absolute path")
	}
	path := strings.TrimRight(configRoot, "/") + "/herdr"
	if e.host.SessionName != "" {
		if !publicID.MatchString(e.host.SessionName) {
			return "", fmt.Errorf("invalid herdr session name")
		}
		path += "/sessions/" + e.host.SessionName
	}
	return path + "/herdr.sock", nil
}

func (e *SSHEndpoint) Snapshot(ctx context.Context) (Snapshot, error) {
	result, err := e.Call(ctx, "session.snapshot", map[string]any{})
	if err != nil {
		return Snapshot{}, err
	}
	var wrapper struct {
		Snapshot Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(result, &wrapper); err != nil {
		return Snapshot{}, fmt.Errorf("decode herdr snapshot: %w", err)
	}
	return wrapper.Snapshot, nil
}

func (e *SSHEndpoint) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	conn, err := e.client.DialContext(ctx, "unix", e.socketPath)
	if err != nil {
		return nil, fmt.Errorf("forward remote herdr socket %s: %w", e.socketPath, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(map[string]any{"id": "herdrx", "method": method, "params": params}); err != nil {
		return nil, fmt.Errorf("write remote herdr request: %w", err)
	}
	var response Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return nil, fmt.Errorf("read remote herdr response: %w", err)
	}
	if response.Error != nil {
		return nil, fmt.Errorf("herdr %s: %s: %s", method, response.Error.Code, response.Error.Message)
	}
	return response.Result, nil
}

func (e *SSHEndpoint) OpenTerminal(ctx context.Context, open TerminalOpen) (TerminalProcess, error) {
	if !publicID.MatchString(open.PaneID) || (open.Mode != "observe" && open.Mode != "control") {
		return nil, fmt.Errorf("invalid terminal request")
	}
	if open.Cols < 10 || open.Cols > 1000 || open.Rows < 3 || open.Rows > 500 {
		return nil, fmt.Errorf("invalid terminal dimensions")
	}
	args := []string{"herdr"}
	if e.host.SessionName != "" {
		args = append(args, "--session", e.host.SessionName)
	}
	args = append(args, "terminal", "session", open.Mode, open.PaneID)
	if open.Takeover {
		args = append(args, "--takeover")
	}
	args = append(args, "--cols", strconv.Itoa(int(open.Cols)), "--rows", strconv.Itoa(int(open.Rows)))
	session, err := e.newSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("open SSH terminal session: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stopStart := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stopStart()
	if err := session.Start(terminalCommand(e.host.Transport, args)); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("start remote terminal session: %w", err)
	}
	process := &sshTerminal{session: session, stdin: stdin, stdout: stdout, stderr: stderr, done: make(chan struct{})}
	go process.captureStderr()
	go func() {
		select {
		case <-ctx.Done():
			_ = process.Close()
		case <-process.done:
		}
	}()
	return process, nil
}

func terminalCommand(transport string, args []string) string {
	command := strings.Join(args, " ") // All arguments are fixed tokens or validated IDs/numbers.
	if transport == "tailcat" {
		// The access daemon accepts this exact command grammar, without a shell.
		return command
	}
	// Non-interactive SSH often omits the installer's ~/.local/bin directory.
	// Preserve the host's PATH precedence and quote HOME inside the remote shell.
	return `sh -c 'PATH="${PATH:-/usr/bin:/bin}:$HOME/.local/bin:/usr/local/bin"; export PATH; exec ` + command + `'`
}

func (e *SSHEndpoint) StageImage(ctx context.Context, ext string, r io.Reader) (string, error) {
	session, err := e.newSession(ctx)
	if err != nil {
		return "", fmt.Errorf("open SSH session for stage-image: %w", err)
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()

	stdin, err := session.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("open stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("open stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	session.Stderr = &stderrBuf

	var remoteCmd string
	if e.host.Transport == "tailcat" {
		remoteCmd = "herdrx-stage-image " + ext
	} else {
		randomBytes := make([]byte, 8)
		if _, err := rand.Read(randomBytes); err != nil {
			return "", fmt.Errorf("generate random bytes: %w", err)
		}
		filename := fmt.Sprintf("img_%d_%x.%s", time.Now().UnixMilli(), randomBytes, ext)
		remoteCmd = fmt.Sprintf(`sh -c 'dir="${XDG_CACHE_HOME:-$HOME/.cache}/herdrx/staging"; mkdir -p "$dir" && cat > "$dir/%s" && chmod 600 "$dir/%s" && printf "%%s/%%s\n" "$dir" "%s"'`, filename, filename, filename)
	}

	if err := session.Start(remoteCmd); err != nil {
		return "", fmt.Errorf("start remote stage-image command: %w", err)
	}

	const maxImageSize = 20 << 20 // 20MB
	limited := io.LimitReader(r, maxImageSize+1)
	written, copyErr := io.Copy(stdin, limited)
	_ = stdin.Close()
	if copyErr != nil {
		return "", fmt.Errorf("transfer image stream: %w", copyErr)
	}
	if written > maxImageSize {
		return "", fmt.Errorf("image exceeds maximum size of 20MB")
	}

	output, readErr := io.ReadAll(stdout)
	if err := session.Wait(); err != nil {
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg != "" {
			return "", fmt.Errorf("remote stage-image: %s", errMsg)
		}
		return "", fmt.Errorf("remote stage-image failed: %w", err)
	}
	if readErr != nil {
		return "", fmt.Errorf("read remote stage-image output: %w", readErr)
	}
	remotePath := strings.TrimSpace(string(output))
	if remotePath == "" {
		return "", fmt.Errorf("empty remote stage-image path")
	}
	return remotePath, nil
}

func (e *SSHEndpoint) Close() error {
	err := e.client.Close()
	if e.extraClose != nil {
		if extraErr := e.extraClose.Close(); err == nil {
			err = extraErr
		}
	}
	return err
}

type sshTerminal struct {
	session   *ssh.Session
	stdin     io.WriteCloser
	stdout    io.Reader
	stderr    io.Reader
	stderrMu  sync.Mutex
	stderrBuf strings.Builder
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (p *sshTerminal) Stdout() io.Reader     { return p.stdout }
func (p *sshTerminal) Stdin() io.WriteCloser { return p.stdin }
func (p *sshTerminal) Wait() error {
	err := p.session.Wait()
	if err != nil && !errors.Is(err, io.EOF) {
		p.stderrMu.Lock()
		message := strings.TrimSpace(p.stderrBuf.String())
		p.stderrMu.Unlock()
		if message != "" {
			return fmt.Errorf("%w: %s", err, message)
		}
	}
	return err
}
func (p *sshTerminal) Close() error {
	p.closeOnce.Do(func() {
		_ = p.stdin.Close()
		p.closeErr = p.session.Close()
		if p.done != nil {
			close(p.done)
		}
	})
	return p.closeErr
}
func (p *sshTerminal) captureStderr() {
	scanner := bufio.NewScanner(p.stderr)
	for scanner.Scan() {
		p.stderrMu.Lock()
		if p.stderrBuf.Len() < 16*1024 {
			p.stderrBuf.WriteString(scanner.Text())
			p.stderrBuf.WriteByte('\n')
		}
		p.stderrMu.Unlock()
	}
}
