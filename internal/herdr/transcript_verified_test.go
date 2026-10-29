package herdr

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// verifiedFixture 在同一编码目录下放两份会话：当前 cwd 的 sess-mine，以及记录了别的
// cwd 的 sess-foreign（同码碰撞）。证据指向后者时必须被拒，因为它不在候选集合里。
func verifiedFixture(t *testing.T) (string, *localTranscriptFS, transcriptCodec, string) {
	t.Helper()
	home := t.TempDir()
	cwd := "/srv/proj"
	writeLines(t, claudeSessionPath(home, cwd, "sess-mine"), []string{claudeUser("u-1", cwd, "我的问题"), claudeAssistant("a-1", cwd, "回答")})
	writeLines(t, claudeSessionPath(home, cwd, "sess-other"), []string{claudeUser("u-2", cwd, "另一个会话")})
	foreign := "/srv/proj-x"
	writeLines(t, filepath.Join(home, ".claude", "projects", agentlog.ClaudeProjectDir(cwd), "sess-foreign.jsonl"), []string{claudeUser("u-3", foreign, "别人的问题")})
	return home, &localTranscriptFS{home: home}, newTranscriptCodecForTest(t), cwd
}

func verifiedScope(cwd string, evidence *AgentSession) TranscriptScope {
	scope := claudeScope(cwd)
	scope.Evidence = evidence
	return scope
}

func TestTranscriptVerifiedBindingByHerdrSessionID(t *testing.T) {
	_, files, codec, cwd := verifiedFixture(t)
	evidence := &AgentSession{Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "sess-mine"}
	page := readTranscript(context.Background(), files, codec, verifiedScope(cwd, evidence), TranscriptRequest{})
	if page.Binding != ChatBindingVerified || page.SessionID != "sess-mine" {
		t.Fatalf("binding = %q session = %q, want verified sess-mine", page.Binding, page.SessionID)
	}
	if len(page.Messages) != 2 || page.Messages[0].ID != "u-1" {
		t.Fatalf("messages = %+v", page.Messages)
	}
	if page.Session == "" || page.NextCursor == "" || len(page.Candidates) != 2 {
		t.Fatalf("verified page must carry its candidate id, cursor and candidates: %+v", page)
	}
	// 返回的候选 id 就是之后续读用的 session：带上它读到的是同一份文件，但那是续读而非新的证据。
	next := readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{Session: page.Session, Cursor: page.NextCursor})
	if next.SessionID != "sess-mine" || next.Binding != ChatBindingSelected || len(next.Messages) != 0 {
		t.Fatalf("continuation = %+v", next)
	}
}

func TestTranscriptVerifiedBindingByHerdrSessionPath(t *testing.T) {
	home, files, codec, cwd := verifiedFixture(t)
	evidence := &AgentSession{Source: "herdr:claude", Agent: "claude", Kind: "path", Value: claudeSessionPath(home, cwd, "sess-other")}
	page := readTranscript(context.Background(), files, codec, verifiedScope(cwd, evidence), TranscriptRequest{})
	if page.Binding != ChatBindingVerified || page.SessionID != "sess-other" {
		t.Fatalf("binding = %q session = %q", page.Binding, page.SessionID)
	}
}

func TestTranscriptEvidenceWithoutMatchKeepsCandidateFlow(t *testing.T) {
	home, files, codec, cwd := verifiedFixture(t)
	cases := map[string]*AgentSession{
		"no evidence":           nil,
		"third-party source":    {Source: "custom:claude", Agent: "claude", Kind: "id", Value: "sess-mine"},
		"other agent source":    {Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "sess-mine"},
		"agent field mismatch":  {Source: "herdr:claude", Agent: "codex", Kind: "id", Value: "sess-mine"},
		"id outside candidates": {Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "sess-foreign"},
		"unknown kind":          {Source: "herdr:claude", Agent: "claude", Kind: "uuid", Value: "sess-mine"},
		"path outside candidates": {Source: "herdr:claude", Agent: "claude", Kind: "path",
			Value: claudeSessionPath(home, cwd, "sess-foreign")},
		"path elsewhere":     {Source: "herdr:claude", Agent: "claude", Kind: "path", Value: "/tmp/sess-mine.jsonl"},
		"relative path":      {Source: "herdr:claude", Agent: "claude", Kind: "path", Value: strings.TrimPrefix(claudeSessionPath(home, cwd, "sess-mine"), "/")},
		"unclean path":       {Source: "herdr:claude", Agent: "claude", Kind: "path", Value: filepath.Join(home, ".claude", "projects") + "/../projects/" + agentlog.ClaudeProjectDir(cwd) + "/sess-mine.jsonl"},
		"path to other root": {Source: "herdr:claude", Agent: "claude", Kind: "path", Value: filepath.Join(home, ".codex", "sessions", agentlog.ClaudeProjectDir(cwd), "sess-mine.jsonl")},
	}
	for name, evidence := range cases {
		t.Run(name, func(t *testing.T) {
			page := readTranscript(context.Background(), files, codec, verifiedScope(cwd, evidence), TranscriptRequest{})
			mustCandidates(t, page, 2)
			if page.Binding != "" || page.Session != "" {
				t.Fatalf("unexpected binding: %+v", page)
			}
		})
	}
}

// 用户显式选择的会话仍然是 selected，即使 Herdr 的证据恰好指向同一份文件。
func TestTranscriptExplicitSelectionStaysSelected(t *testing.T) {
	_, files, codec, cwd := verifiedFixture(t)
	evidence := &AgentSession{Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "sess-mine"}
	list := readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{})
	for _, candidate := range list.Candidates {
		page := readTranscript(context.Background(), files, codec, verifiedScope(cwd, evidence), TranscriptRequest{Session: candidate.ID})
		if page.Binding != ChatBindingSelected || page.Session != "" {
			t.Fatalf("explicit %s = %q/%q", candidate.SessionID, page.Binding, page.Session)
		}
	}
}

func TestAgentSessionParsedButNotSentToBrowser(t *testing.T) {
	raw := `{"panes":[{"pane_id":"p1","agent":"claude","agent_status":"idle","agent_session":{"source":"herdr:claude","agent":"claude","kind":"id","value":"sess-1"}}],
		"agents":[{"agent":"claude","pane_id":"p1","agent_status":"idle","agent_session":{"source":"herdr:claude","agent":"claude","kind":"path","value":"/home/example/.claude/projects/x/sess-1.jsonl"}}]}`
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Panes[0].AgentSession; got == nil || got.Value != "sess-1" || got.Kind != "id" {
		t.Fatalf("pane agent_session = %+v", got)
	}
	if got := snapshot.Agents[0].AgentSession; got == nil || got.Kind != "path" {
		t.Fatalf("agent agent_session = %+v", got)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "agent_session") || !strings.Contains(string(encoded), `"pane_id":"p1"`) {
		t.Fatalf("snapshot for browser = %s", encoded)
	}
}
