package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/agentlog"
	"github.com/riba2534/herdrx/internal/store"
)

func TestTranscriptTailcatCommandMatchesTheDaemonAllowlist(t *testing.T) {
	if transcriptTailcatCommand != agent.TranscriptCommand {
		t.Fatalf("client command %q != daemon command %q", transcriptTailcatCommand, agent.TranscriptCommand)
	}
	spec, err := agent.ParseCommand(transcriptTailcatCommand)
	if err != nil || spec.Internal != "transcript" || len(spec.Args) != 0 {
		t.Fatalf("allowlist rejected the transcript command: %+v %v", spec, err)
	}
	for _, command := range []string{
		"python3 -c 'print(1)'",
		"sh -c " + transcriptTailcatCommand,
		transcriptTailcatCommand + " /tmp/log",
		transcriptTailcatCommand + "; id",
	} {
		if _, err := agent.ParseCommand(command); err == nil {
			t.Fatalf("daemon accepted %q", command)
		}
	}
}

func TestSSHTranscriptKeepsThePythonReader(t *testing.T) {
	session := &transcriptRetrySession{output: `{"ok":true,"entries":[]}`}
	endpoint := &SSHEndpoint{
		host:   store.Host{Transport: "ssh"},
		client: &transcriptRetryTransport{sessions: []*transcriptRetrySession{session}},
	}
	got, err := endpoint.execTranscriptReader(context.Background(), transcriptRemoteRequest{Op: "list", Agent: "claude"})
	if err != nil || !got.OK {
		t.Fatalf("ssh reader: %+v %v", got, err)
	}
	if !strings.HasPrefix(session.command, "python3 -c '") || strings.Contains(session.command, transcriptTailcatCommand) {
		t.Fatalf("ssh command = %q", session.command)
	}
	if endpoint.transcriptUnavailable() {
		t.Fatal("working ssh reader was cached as unsupported")
	}
}

func TestTailcatReaderFailureStaysRetryable(t *testing.T) {
	failed := &transcriptRetrySession{waitErr: errors.New("exit status 126")}
	retried := &transcriptRetrySession{output: `{"ok":true,"entries":[]}`}
	endpoint := &SSHEndpoint{
		host:   store.Host{Transport: "tailcat"},
		client: &transcriptRetryTransport{sessions: []*transcriptRetrySession{failed, retried}},
	}
	_, err := endpoint.execTranscriptReader(context.Background(), transcriptRemoteRequest{Op: "list", Agent: "claude"})
	if err == nil || errors.Is(err, errTranscriptTransport) || endpoint.transcriptUnavailable() {
		t.Fatalf("tailcat failure was cached or lost: %v unsupported=%v", err, endpoint.transcriptUnavailable())
	}
	if failed.command != transcriptTailcatCommand || strings.Contains(failed.command, "python") || strings.Contains(failed.command, "sh") {
		t.Fatalf("tailcat command = %q", failed.command)
	}
	if strings.Contains(err.Error(), "/") {
		t.Fatalf("retryable error leaked a path: %v", err)
	}
	got, err := endpoint.execTranscriptReader(context.Background(), transcriptRemoteRequest{Op: "list", Agent: "claude"})
	if err != nil || !got.OK || endpoint.transcriptUnavailable() || retried.command != transcriptTailcatCommand {
		t.Fatalf("retry: %+v %v command=%q", got, err, retried.command)
	}
}

func TestTransportWithoutAReaderIsTheOnlyCachedGap(t *testing.T) {
	transport := &countingTranscriptTransport{}
	endpoint := &SSHEndpoint{host: store.Host{Transport: "mosh"}, client: transport}
	page, err := endpoint.Transcript(context.Background(), TranscriptScope{PaneID: "p", Agent: agentlog.AgentClaude, CWD: "/srv/app"}, TranscriptRequest{})
	if err != nil || page.Supported || page.Reason != ChatReasonUnsupportedTransport || transport.calls != 0 {
		t.Fatalf("page=%+v err=%v calls=%d", page, err, transport.calls)
	}
	if !endpoint.transcriptUnavailable() || endpoint.TranscriptUnavailableReason() == "" {
		t.Fatal("missing reader was not cached")
	}
	again, err := endpoint.Transcript(context.Background(), TranscriptScope{PaneID: "other", Agent: agentlog.AgentClaude, CWD: "/srv/app"}, TranscriptRequest{})
	if err != nil || again.Reason != ChatReasonUnsupportedTransport || transport.calls != 0 {
		t.Fatalf("cached gap retried the transport: %+v %v calls=%d", again, err, transport.calls)
	}
}

type countingTranscriptTransport struct{ calls int }

func (t *countingTranscriptTransport) NewSession() (sshSession, error) {
	t.calls++
	return &transcriptRetrySession{output: `{"ok":true,"entries":[]}`}, nil
}
func (*countingTranscriptTransport) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("unused")
}
func (*countingTranscriptTransport) Close() error { return nil }

func TestTailcatWhitelistedReaderReturnsStructuredPageWithoutCWD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/srv/tailcat-project"
	text := "hello from the log"
	writeLines(t, claudeSessionPath(home, cwd, "sess-1"), []string{claudeUser("u-1", cwd, text)})

	transport := &tailcatLiveTransport{}
	endpoint := &SSHEndpoint{host: store.Host{Transport: "tailcat"}, client: transport}
	page, err := endpoint.Transcript(context.Background(), claudeScope(cwd), TranscriptRequest{})
	if err != nil || !page.Supported || page.Reason != "" || len(page.Candidates) != 1 || len(page.Messages) != 0 {
		t.Fatalf("candidates: %+v %v", page, err)
	}
	if page.Candidates[0].SessionID != "sess-1" || page.Candidates[0].Agent != agentlog.AgentClaude {
		t.Fatalf("candidate = %+v", page.Candidates[0])
	}
	bound, err := endpoint.Transcript(context.Background(), claudeScope(cwd), TranscriptRequest{Session: page.Candidates[0].ID})
	if err != nil || !bound.Supported || bound.Binding != ChatBindingSelected || bound.SessionID != "sess-1" || len(bound.Messages) != 1 {
		t.Fatalf("messages: %+v %v", bound, err)
	}
	if bound.Messages[0].Role != agentlog.RoleUser || len(bound.Messages[0].Blocks) != 1 || bound.Messages[0].Blocks[0].Text != text {
		t.Fatalf("record = %+v", bound.Messages[0])
	}
	encoded, err := json.Marshal(struct {
		Candidates []agentlog.Candidate `json:"candidates"`
		Messages   []agentlog.Record    `json:"messages"`
		SessionID  string               `json:"session_id"`
	}{bound.Candidates, bound.Messages, bound.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(cwd)) || bytes.Contains(encoded, []byte(home)) || bytes.Contains(encoded, []byte(`"cwd"`)) {
		t.Fatalf("client response leaked cwd: %s", encoded)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.commands) == 0 {
		t.Fatal("tailcat reader never ran")
	}
	for _, command := range transport.commands {
		if command != transcriptTailcatCommand {
			t.Fatalf("tailcat ran %q", command)
		}
	}
}

// $HOME 是符号链接、pane cwd 用链接写法、日志按真实写法落盘：同一条连接的第一次读取就要匹配上。
func TestTailcatFirstReadMatchesTheOtherHomeSpelling(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real-home")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", link)
	realCwd := resolved + "/proj"
	writeLines(t, claudeSessionPath(real, realCwd, "sess-real"), []string{claudeUser("u-1", realCwd, "真实写法")})
	endpoint := &SSHEndpoint{host: store.Host{Transport: "tailcat"}, client: &tailcatLiveTransport{}}
	page, err := endpoint.Transcript(context.Background(), claudeScope(link+"/proj"), TranscriptRequest{})
	if err != nil || len(page.Candidates) != 1 || page.Candidates[0].SessionID != "sess-real" {
		t.Fatalf("first read: %+v %v", page, err)
	}
}

// 远端 python 读取器同样能列出 slug 目录、读 `.project_root` 与 `chats/<parentId>/` 下的会话。
func TestPythonReaderReadsGeminiSlugProjects(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini", "tmp", "proj", "chats", "parent-1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "tmp", "proj", ".project_root"), []byte("/srv/proj"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session-c.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(request transcriptRemoteRequest) transcriptRemoteResponse {
		t.Helper()
		payload, _ := json.Marshal(request)
		var response transcriptRemoteResponse
		if err := json.Unmarshal(runPythonTranscript(t, home, payload), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	listed := run(transcriptRemoteRequest{Op: "list", Agent: "gemini", Rel: "proj/chats", Depth: 2})
	found := false
	for _, entry := range listed.Entries {
		found = found || entry.Rel == "proj/chats/parent-1/session-c.json"
	}
	read := run(transcriptRemoteRequest{Op: "read", Agent: "gemini", Reads: []transcriptRemoteRead{{Rel: "proj/.project_root", Length: 64}}})
	if !listed.OK || !found || !read.OK || len(read.Reads) != 1 || !read.Reads[0].Exists {
		t.Fatalf("list = %+v read = %+v", listed, read)
	}
}

// Gemini 的 slug 目录与 `.project_root` 标记经受控端读取器同样可读（固定根内、逐段 O_NOFOLLOW）。
func TestTailcatReadsGeminiSlugProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/srv/proj"
	dir := filepath.Join(home, ".gemini", "tmp", "proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".project_root"), []byte(cwd), 0o600); err != nil {
		t.Fatal(err)
	}
	writeGeminiSession(t, home, "proj", "session-a.json", `{"sessionId":"a","projectHash":"`+agentlog.GeminiProjectHash(cwd)+`","messages":[{"type":"user","content":"经受控端"}]}`)
	endpoint := &SSHEndpoint{host: store.Host{Transport: "tailcat"}, client: &tailcatLiveTransport{}}
	scope := TranscriptScope{PaneID: "p", Agent: agentlog.AgentGemini, CWD: cwd}
	page, err := endpoint.Transcript(context.Background(), scope, TranscriptRequest{})
	if err != nil || len(page.Candidates) != 1 {
		t.Fatalf("candidates = %+v %v", page, err)
	}
	bound, err := endpoint.Transcript(context.Background(), scope, TranscriptRequest{Session: page.Candidates[0].ID})
	if err != nil || len(bound.Messages) != 1 || bound.Messages[0].Blocks[0].Text != "经受控端" {
		t.Fatalf("bound = %+v %v", bound, err)
	}
}

// 远端 python3 缺 sqlite3：agy 诚实降级为不支持，但不缓存成整台主机不支持。
func TestSSHAgyWithoutSQLiteDegradesOnlyAgy(t *testing.T) {
	const uuid = "12345678-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	endpoint := &SSHEndpoint{client: &transcriptRetryTransport{sessions: []*transcriptRetrySession{
		{output: `{"ok":true,"home":"/home/example","home_real":"/home/example"}`},
		{output: `{"ok":false,"error":"agy_unsupported"}`},
		{output: `{"ok":true,"entries":[]}`},
	}}}
	scope := TranscriptScope{PaneID: "p", Agent: agentlog.AgentAgy, CWD: "/srv/app",
		Evidence: &AgentSession{Source: "herdr:antigravity_cli", Agent: agentlog.AgentAgy, Kind: "id", Value: uuid}}
	page, err := endpoint.Transcript(context.Background(), scope, TranscriptRequest{})
	if err != nil || page.Supported || page.Reason != ChatReasonUnsupportedTransport {
		t.Fatalf("agy without sqlite3 = %+v %v", page, err)
	}
	if endpoint.transcriptUnavailable() {
		t.Fatal("a missing sqlite3 module disabled every reader on the host")
	}
	claude, err := endpoint.Transcript(context.Background(), claudeScope("/srv/app"), TranscriptRequest{})
	if err != nil || !claude.Supported {
		t.Fatalf("claude after agy degrade = %+v %v", claude, err)
	}
}

type tailcatLiveTransport struct {
	mu       sync.Mutex
	commands []string
}

func (t *tailcatLiveTransport) NewSession() (sshSession, error) {
	return newTailcatLiveSession(func(command string) {
		t.mu.Lock()
		t.commands = append(t.commands, command)
		t.mu.Unlock()
	}), nil
}
func (*tailcatLiveTransport) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("unused")
}
func (*tailcatLiveTransport) Close() error { return nil }

type tailcatLiveSession struct {
	command        string
	onStart        func(string)
	stdinR         *io.PipeReader
	stdinW         *io.PipeWriter
	stdoutR        *io.PipeReader
	stdoutW        *io.PipeWriter
	done           chan struct{}
	waitErr        error
	closed         atomic.Bool
	closePipesOnce sync.Once
}

func newTailcatLiveSession(onStart func(string)) *tailcatLiveSession {
	session := &tailcatLiveSession{onStart: onStart, done: make(chan struct{})}
	session.stdinR, session.stdinW = io.Pipe()
	session.stdoutR, session.stdoutW = io.Pipe()
	return session
}

func (s *tailcatLiveSession) Start(command string) error {
	s.command = command
	if s.onStart != nil {
		s.onStart(command)
	}
	go func() {
		defer close(s.done)
		var buf bytes.Buffer
		err := agent.ServeTranscript(s.stdinR, &buf)
		if err != nil {
			s.waitErr = err
			_ = s.stdoutW.CloseWithError(err)
			return
		}
		if _, err := s.stdoutW.Write(buf.Bytes()); err != nil {
			s.waitErr = err
		}
		_ = s.stdoutW.Close()
	}()
	return nil
}

func (s *tailcatLiveSession) Wait() error {
	<-s.done
	return s.waitErr
}
func (s *tailcatLiveSession) Output(string) ([]byte, error)      { return nil, errors.New("unused") }
func (s *tailcatLiveSession) StdinPipe() (io.WriteCloser, error) { return s.stdinW, nil }
func (s *tailcatLiveSession) StdoutPipe() (io.Reader, error)     { return s.stdoutR, nil }
func (s *tailcatLiveSession) StderrPipe() (io.Reader, error)     { return strings.NewReader(""), nil }
func (s *tailcatLiveSession) setStderr(io.Writer)                {}
func (s *tailcatLiveSession) Close() error {
	s.closed.Store(true)
	s.closePipesOnce.Do(func() {
		_ = s.stdinR.Close()
		_ = s.stdinW.Close()
		_ = s.stdoutR.Close()
		_ = s.stdoutW.Close()
	})
	return nil
}
