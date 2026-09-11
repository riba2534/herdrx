package herdr

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/riba2534/herdrx/internal/herdrpaths"
)

var publicID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,127}$`)

type LocalEndpoint struct {
	Binary      string
	SocketPath  string
	SessionName string
}

func NewLocalEndpoint(binary, sessionName string) (*LocalEndpoint, error) {
	herdrDir, err := herdrpaths.HerdrDir()
	if err != nil {
		return nil, fmt.Errorf("resolve herdr config directory: %w", err)
	}
	socket := filepath.Join(herdrDir, "herdr.sock")
	if sessionName != "" {
		if !publicID.MatchString(sessionName) {
			return nil, fmt.Errorf("invalid herdr session name")
		}
		socket = filepath.Join(herdrDir, "sessions", sessionName, "herdr.sock")
	}
	return &LocalEndpoint{Binary: binary, SocketPath: socket, SessionName: sessionName}, nil
}

func (e *LocalEndpoint) Snapshot(ctx context.Context) (Snapshot, error) {
	result, err := e.Call(ctx, "session.snapshot", map[string]any{})
	if err != nil {
		return Snapshot{}, err
	}
	var wrapper struct {
		Type     string   `json:"type"`
		Snapshot Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(result, &wrapper); err != nil {
		return Snapshot{}, fmt.Errorf("decode herdr snapshot: %w", err)
	}
	return wrapper.Snapshot, nil
}

func (e *LocalEndpoint) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", e.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("connect herdr API socket %s: %w", e.SocketPath, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	request := map[string]any{"id": "herdrx", "method": method, "params": params}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return nil, fmt.Errorf("write herdr request: %w", err)
	}
	var response Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return nil, fmt.Errorf("read herdr response: %w", err)
	}
	if response.Error != nil {
		return nil, fmt.Errorf("herdr %s: %s: %s", method, response.Error.Code, response.Error.Message)
	}
	return response.Result, nil
}

func (e *LocalEndpoint) OpenTerminal(ctx context.Context, open TerminalOpen) (TerminalProcess, error) {
	if !publicID.MatchString(open.PaneID) {
		return nil, fmt.Errorf("invalid pane id")
	}
	if open.Mode != "observe" && open.Mode != "control" {
		return nil, fmt.Errorf("invalid terminal mode")
	}
	if open.Cols < 10 || open.Cols > 1000 || open.Rows < 3 || open.Rows > 500 {
		return nil, fmt.Errorf("invalid terminal dimensions")
	}
	args := make([]string, 0, 12)
	if e.SessionName != "" {
		args = append(args, "--session", e.SessionName)
	}
	args = append(args, "terminal", "session", open.Mode, open.PaneID)
	if open.Takeover {
		args = append(args, "--takeover")
	}
	args = append(args, "--cols", fmt.Sprint(open.Cols), "--rows", fmt.Sprint(open.Rows))
	cmd := exec.CommandContext(ctx, e.Binary, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start herdr terminal session: %w", err)
	}
	process := &commandTerminal{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}
	go process.captureStderr()
	return process, nil
}

func (e *LocalEndpoint) StageImage(ctx context.Context, ext string, r io.Reader) (string, error) {
	cacheDir, err := herdrpaths.CacheRoot()
	if err != nil || cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "herdrx-staging")
	} else {
		cacheDir = filepath.Join(cacheDir, "herdrx", "staging")
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("create local staging directory: %w", err)
	}
	randomBytes := make([]byte, 8)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	filename := fmt.Sprintf("img_%d_%x.%s", time.Now().UnixMilli(), randomBytes, ext)
	filePath := filepath.Join(cacheDir, filename)

	const maxImageSize = 20 << 20 // 20MB
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create staging file: %w", err)
	}
	defer file.Close()

	limited := io.LimitReader(r, maxImageSize+1)
	written, err := io.Copy(file, limited)
	if err != nil {
		_ = os.Remove(filePath)
		return "", fmt.Errorf("write staging file: %w", err)
	}
	if written > maxImageSize {
		_ = os.Remove(filePath)
		return "", fmt.Errorf("image exceeds maximum size of 20MB")
	}
	return filePath, nil
}

func (e *LocalEndpoint) Close() error { return nil }

type commandTerminal struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	mu        sync.Mutex
	stderrBuf strings.Builder
	closeOnce sync.Once
}

func (p *commandTerminal) Stdout() io.Reader     { return p.stdout }
func (p *commandTerminal) Stdin() io.WriteCloser { return p.stdin }
func (p *commandTerminal) Wait() error {
	err := p.cmd.Wait()
	if err != nil {
		p.mu.Lock()
		message := strings.TrimSpace(p.stderrBuf.String())
		p.mu.Unlock()
		if message != "" {
			return fmt.Errorf("%w: %s", err, message)
		}
	}
	return err
}

func (p *commandTerminal) Close() error {
	p.closeOnce.Do(func() {
		_ = p.stdin.Close()
		// This child is `herdr terminal session observe/control`, never the Herdr daemon
		// or the pane's PTY. Do not replace this with a process-group signal.
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	})
	return nil
}
func (p *commandTerminal) captureStderr() {
	scanner := bufio.NewScanner(p.stderr)
	for scanner.Scan() {
		p.mu.Lock()
		if p.stderrBuf.Len() < 16*1024 {
			p.stderrBuf.WriteString(scanner.Text())
			p.stderrBuf.WriteByte('\n')
		}
		p.mu.Unlock()
	}
}
