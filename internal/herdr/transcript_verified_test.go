package herdr

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
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

// 每个 reader 只认 Herdr 官方集成的 (source, agent)，对照 Herdr agent_resume.rs 的
// is_official_agent_source：traex 没有自己的集成，hook 报成 codex；agy 的集成名是 antigravity_cli。
func TestTranscriptEvidenceSourcePerReader(t *testing.T) {
	cases := []struct {
		reader, source, agent string
		rejected              [][2]string
	}{
		{agentlog.AgentClaude, "herdr:claude", "claude", [][2]string{{"herdr:codex", "codex"}}},
		{agentlog.AgentCodex, "herdr:codex", "codex", [][2]string{{"herdr:traex", "traex"}, {"herdr:claude", "claude"}}},
		{agentlog.AgentTraex, "herdr:codex", "codex", [][2]string{{"herdr:traex", "traex"}, {"herdr:codex", "traex"}}},
		{agentlog.AgentCursor, "herdr:cursor", "cursor", [][2]string{{"herdr:claude", "cursor"}}},
		{agentlog.AgentGrok, "herdr:grok", "grok", [][2]string{{"custom:grok", "grok"}}},
		{agentlog.AgentPi, "herdr:pi", "pi", [][2]string{{"herdr:omp", "omp"}}},
		{agentlog.AgentAgy, "herdr:antigravity_cli", "agy", [][2]string{{"herdr:agy", "agy"}, {"herdr:antigravity_cli", "antigravity"}}},
	}
	for _, tc := range cases {
		ok := &AgentSession{Source: tc.source, Agent: tc.agent, Kind: "id", Value: "s-1"}
		if !transcriptEvidenceMatches(tc.reader, ok, "s-1", "x") {
			t.Fatalf("%s: official source %s/%s rejected", tc.reader, tc.source, tc.agent)
		}
		if transcriptEvidenceMatches(tc.reader, ok, "s-2", "x") {
			t.Fatalf("%s: id mismatch accepted", tc.reader)
		}
		for _, bad := range tc.rejected {
			if transcriptEvidenceMatches(tc.reader, &AgentSession{Source: bad[0], Agent: bad[1], Kind: "id", Value: "s-1"}, "s-1", "x") {
				t.Fatalf("%s: %s/%s accepted", tc.reader, bad[0], bad[1])
			}
		}
	}
}

// kind=path 按候选所在的固定根核对后缀；rollout 候选带根标记时按标记指向的根核对。
func TestTranscriptEvidencePathUsesTheCandidateRoot(t *testing.T) {
	rel := "2026/10/27/rollout-2026-10-27T12-00-00-s-1.jsonl"
	codex := &AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "path", Value: "/home/example/.trae/cli/sessions/" + rel}
	if !transcriptEvidenceMatches(agentlog.AgentCodex, codex, "s-1", rolloutTraexMarker+"/"+rel) {
		t.Fatal("traex-rooted candidate of a codex pane did not match its own path")
	}
	if transcriptEvidenceMatches(agentlog.AgentCodex, codex, "s-1", rel) {
		t.Fatal("trae path matched a ~/.codex candidate")
	}
	pi := &AgentSession{Source: "herdr:pi", Agent: "pi", Kind: "path", Value: "/home/example/.pi/agent/sessions/--srv-proj--/a_s-1.jsonl"}
	if !transcriptEvidenceMatches(agentlog.AgentPi, pi, "s-1", "--srv-proj--/a_s-1.jsonl") {
		t.Fatal("pi path rejected")
	}
	agy := &AgentSession{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "path", Value: "/home/example/.gemini/antigravity-cli/conversations/s-1.db"}
	if transcriptEvidenceMatches(agentlog.AgentAgy, agy, "s-1", "s-1.db") {
		t.Fatal("agy accepted kind=path")
	}
}

// 远端 python 读取器的 ROOTS 必须与 Go 侧的固定日志根逐项一致，证据路径后缀才按同一个根核对。
func TestRemoteReaderRootsMatchGoRoots(t *testing.T) {
	match := regexp.MustCompile(`(?m)^ROOTS = (\{.*\})$`).FindStringSubmatch(transcriptReaderSource)
	if match == nil {
		t.Fatal("ROOTS not found in the remote reader")
	}
	var roots map[string]string
	if err := json.Unmarshal([]byte(match[1]), &roots); err != nil {
		t.Fatal(err)
	}
	for agent, root := range roots {
		if got := strings.Join(transcriptRootParts(transcriptRootFor(agent)), "/"); got != root {
			t.Fatalf("%s: go root %q, remote root %q", agent, got, root)
		}
	}
	for _, agent := range []string{agentlog.AgentClaude, agentlog.AgentCodex, agentlog.AgentTraex, agentlog.AgentDSH, agentlog.AgentCursor, agentlog.AgentGemini, agentlog.AgentAgy, agentlog.AgentPi, agentlog.AgentGrok} {
		if _, ok := roots[agent]; !ok {
			t.Fatalf("remote reader has no root for %s", agent)
		}
	}
}

// 根标记只属于 rollout reader；其它 reader 的相对路径即使以标记开头也不换根。
func TestRolloutMarkersOnlyApplyToRolloutReaders(t *testing.T) {
	if root, rel := rolloutStorage(agentlog.AgentCodex, rolloutTraexMarker+"/a.jsonl"); root != transcriptRootTraex || rel != "a.jsonl" {
		t.Fatalf("codex marker = %s %s", root, rel)
	}
	if root, rel := rolloutStorage(agentlog.AgentClaude, rolloutTraexMarker+"/a.jsonl"); root != transcriptRootClaude || rel != rolloutTraexMarker+"/a.jsonl" {
		t.Fatalf("claude marker = %s %s", root, rel)
	}
}

// agy 的唯一候选只能由严格 uuid 构造，任何带路径字符的值都不会变成候选。
func TestAgyEvidenceCandidateNeedsAStrictUUID(t *testing.T) {
	codec := newTranscriptCodecForTest(t)
	const uuid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	evidence := &AgentSession{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: uuid}
	if candidate, ok := transcriptAgyEvidenceCandidate(codec, evidence); !ok || candidate.SessionID != uuid {
		t.Fatalf("uuid candidate = %+v %v", candidate, ok)
	}
	for _, value := range []string{"../" + uuid, uuid + ".db", "/tmp/" + uuid, "not-a-uuid", ""} {
		evidence.Value = value
		if _, ok := transcriptAgyEvidenceCandidate(codec, evidence); ok {
			t.Fatalf("%q became a candidate", value)
		}
	}
}
