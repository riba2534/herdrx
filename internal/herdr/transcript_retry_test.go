package herdr

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
)

type transcriptRetryTransport struct{ sessions []*transcriptRetrySession }

func (t *transcriptRetryTransport) NewSession() (sshSession, error) {
	s := t.sessions[0]
	t.sessions = t.sessions[1:]
	return s, nil
}
func (*transcriptRetryTransport) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("unused")
}
func (*transcriptRetryTransport) Close() error { return nil }

type transcriptRetrySession struct {
	output  string
	waitErr error
	closed  atomic.Bool
	onWait  func()
}

func (s *transcriptRetrySession) Output(string) ([]byte, error) { return []byte(s.output), s.waitErr }
func (*transcriptRetrySession) Start(string) error              { return nil }
func (s *transcriptRetrySession) Wait() error {
	if s.onWait != nil {
		s.onWait()
	}
	return s.waitErr
}
func (s *transcriptRetrySession) Close() error                     { s.closed.Store(true); return nil }
func (*transcriptRetrySession) StdinPipe() (io.WriteCloser, error) { return transcriptDiscard{}, nil }
func (s *transcriptRetrySession) StdoutPipe() (io.Reader, error) {
	return strings.NewReader(s.output), nil
}
func (*transcriptRetrySession) StderrPipe() (io.Reader, error) { return strings.NewReader(""), nil }
func (*transcriptRetrySession) setStderr(io.Writer)            {}

type transcriptDiscard struct{}

func (transcriptDiscard) Write(p []byte) (int, error) { return len(p), nil }
func (transcriptDiscard) Close() error                { return nil }

func TestTranscriptTransientReaderFailureDoesNotPoisonSharedHost(t *testing.T) {
	cases := []struct {
		name, output string
		waitErr      error
	}{
		{"empty", "", nil}, {"invalid-json", "not-json", nil},
		{"command-failed", "", errors.New("temporary failure")},
		{"reader-internal", `{"ok":false,"error":"internal"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &SSHEndpoint{client: &transcriptRetryTransport{sessions: []*transcriptRetrySession{
				{output: tc.output, waitErr: tc.waitErr}, {output: `{"ok":true,"entries":[]}`},
			}}}
			_, err := e.execTranscriptReader(context.Background(), transcriptRemoteRequest{Op: "list", Agent: "claude"})
			if err == nil || errors.Is(err, errTranscriptTransport) {
				t.Fatalf("expected retryable error, got %v", err)
			}
			if e.transcriptUnavailable() {
				t.Fatal("transient failure poisoned all panes on shared host")
			}
			got, err := e.execTranscriptReader(context.Background(), transcriptRemoteRequest{Op: "list", Agent: "claude"})
			if err != nil || !got.OK {
				t.Fatalf("retry failed: %+v %v", got, err)
			}
		})
	}
}

func TestTranscriptCancelledReadDoesNotMarkUnsupported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &SSHEndpoint{client: &transcriptRetryTransport{sessions: []*transcriptRetrySession{{onWait: cancel}}}}
	_, err := e.execTranscriptReader(ctx, transcriptRemoteRequest{Op: "list", Agent: "claude"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if e.transcriptUnavailable() {
		t.Fatal("cancellation poisoned host")
	}
}

func TestTranscriptOversizedResponseClosesChannel(t *testing.T) {
	s := &transcriptRetrySession{output: strings.Repeat("x", transcriptRemoteMaxBytes+1)}
	e := &SSHEndpoint{client: &transcriptRetryTransport{sessions: []*transcriptRetrySession{s}}}
	_, err := e.execTranscriptReader(context.Background(), transcriptRemoteRequest{Op: "list", Agent: "claude"})
	if err == nil || !s.closed.Load() || e.transcriptUnavailable() {
		t.Fatalf("oversized response must close channel without poisoning host: %v", err)
	}
}

func TestTranscriptPropagatesRetryableErrorsToHTTP(t *testing.T) {
	e := &SSHEndpoint{client: &transcriptRetryTransport{sessions: []*transcriptRetrySession{{output: ""}, {output: `{"ok":true,"entries":[]}`}}}}
	_, err := e.Transcript(context.Background(), TranscriptScope{PaneID: "p1", Agent: "claude", CWD: "/example"}, TranscriptRequest{})
	if err == nil {
		t.Fatal("retryable failure converted to permanent unsupported response")
	}
	page, err := e.Transcript(context.Background(), TranscriptScope{PaneID: "p2", Agent: "claude", CWD: "/example"}, TranscriptRequest{})
	if err != nil || page.Reason == ChatReasonUnsupportedTransport {
		t.Fatalf("another pane on the shared host could not retry: reason=%s err=%v", page.Reason, err)
	}
}
