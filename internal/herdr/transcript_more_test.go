package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
)

func TestHomeSymlinkReadsLogsAndMatchesBothSpellings(t *testing.T) {
	base := t.TempDir()
	physical := filepath.Join(base, "physical")
	real := filepath.Join(physical, "home")
	via := filepath.Join(base, "via")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	// 祖先目录也是符号链接，和 macOS 临时目录的 /var -> /private/var 一样。
	if err := os.Symlink(physical, via); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	link := filepath.Join(via, "home-link")
	// 链接目标故意经过 via。realpath 会把这一层和祖先都展开。
	if err := os.Symlink(filepath.Join(via, "home"), link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == link || resolved == filepath.Join(via, "home") {
		t.Fatalf("realpath left an ancestor symlink in %s", resolved)
	}
	realCwd := resolved + "/proj"
	linkCwd := link + "/proj"
	writeLines(t, claudeSessionPath(real, realCwd, "sess-real"), []string{claudeUser("u-real", realCwd, "真实家目录")})
	writeLines(t, claudeSessionPath(real, linkCwd, "sess-link"), []string{claudeUser("u-link", linkCwd, "链接家目录")})
	files := &localTranscriptFS{home: link}
	codec := newTranscriptCodecForTest(t)

	fromLink := TranscriptScope{
		PaneID: "p", Agent: agentlog.AgentClaude, CWD: linkCwd,
		Evidence: &AgentSession{Source: "hook", Agent: agentlog.AgentClaude, Kind: "id", Value: "sess-real"},
	}
	page := readTranscript(context.Background(), files, codec, fromLink, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || len(page.Messages) != 1 || page.Messages[0].Blocks[0].Text != "真实家目录" {
		t.Fatalf("link pane / real record = %+v", page)
	}
	fromReal := TranscriptScope{
		PaneID: "p", Agent: agentlog.AgentClaude, CWD: realCwd,
		Evidence: &AgentSession{Source: "hook", Agent: agentlog.AgentClaude, Kind: "id", Value: "sess-link"},
	}
	page = readTranscript(context.Background(), files, codec, fromReal, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || len(page.Messages) != 1 || page.Messages[0].Blocks[0].Text != "链接家目录" {
		t.Fatalf("real pane / link record = %+v", page)
	}

	outside := filepath.Join(base, "outside")
	writeLines(t, filepath.Join(outside, "projects", agentlog.ClaudeProjectDir(realCwd), "sess-x.jsonl"), []string{claudeUser("u-x", realCwd, "不该读到")})
	if err := os.Rename(filepath.Join(real, ".claude"), filepath.Join(real, ".claude-real")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(real, ".claude")); err != nil {
		t.Fatal(err)
	}
	denied := readTranscript(context.Background(), files, codec, fromLink, TranscriptRequest{})
	if denied.Reason != ChatReasonReadDenied || len(denied.Messages) != 0 {
		t.Fatalf("symlinked .claude under a symlinked home = %+v", denied)
	}
	encoded, _ := json.Marshal(denied)
	if strings.Contains(string(encoded), "不该读到") {
		t.Fatal("followed .claude symlink")
	}
}

func TestPythonReaderFollowsOnlyTheHomeSymlink(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	base := t.TempDir()
	real := filepath.Join(base, "real")
	link := filepath.Join(base, "link")
	if err := os.MkdirAll(filepath.Join(real, ".claude", "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	request, _ := json.Marshal(transcriptRemoteRequest{Op: "list", Agent: "claude", Depth: 1})
	output := runPythonTranscript(t, link, request)
	var response transcriptRemoteResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Home != link || response.HomeReal == "" || response.HomeReal == link {
		t.Fatalf("python symlink home = %+v", response)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(real, ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(real, ".claude")); err != nil {
		t.Fatal(err)
	}
	output = runPythonTranscript(t, link, request)
	response = transcriptRemoteResponse{}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error != "denied" {
		t.Fatalf("python followed .claude = %+v", response)
	}
}

func runPythonTranscript(t *testing.T, home string, request []byte) []byte {
	t.Helper()
	cmd := exec.Command("python3", "-c", transcriptReaderSource)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(string(request))
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func TestCodexIDBindsTheTraexRoot(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/proj"
	const (
		sessionID = "11111111-2222-4333-8444-555555555555"
		otherID   = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	)
	name := "rollout-2026-10-27T12-00-00-" + sessionID + "_" + otherID
	writeLines(t, traexSessionPath(home, "2026/10/27", name), []string{traexFixture(sessionID, cwd)})
	writeLines(t, traexSessionPath(home, "2026/10/27", "rollout-2026-10-27T13-00-00-"+otherID+"_"+sessionID), []string{
		codexMeta(otherID, cwd),
		codexMessage("user", "更新的另一场"),
	})
	writeLines(t, codexSessionPath(home, "2026/10/27", "rollout-2026-10-27T14-00-00-"+sessionID), []string{
		codexMeta(sessionID, "/srv/other"),
		codexMessage("user", "codex 里同名但 cwd 不对"),
	})
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	scope := TranscriptScope{
		PaneID: "p", Agent: agentlog.AgentCodex, CWD: cwd,
		Evidence: &AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: sessionID},
	}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || page.Agent != agentlog.AgentCodex || page.SessionID != sessionID {
		t.Fatalf("binding = %+v", page)
	}
	text := joinedText(page.Messages)
	for _, want := range []string{"traex 问题", "file body", "patched", "plain codex line"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	for _, secret := range []string{"DEV_SECRET", "THINK_SECRET", "REPLACE_SECRET", "EVENT_SECRET", "EXTRA_SECRET", "更新的另一场", "cwd 不对"} {
		if strings.Contains(text, secret) {
			t.Fatalf("leaked %s in %s", secret, text)
		}
	}
	ids := map[string]bool{}
	for _, message := range page.Messages {
		ids[message.ID] = true
	}
	for _, id := range []string{"u1", "fc1", "fo1", "cc1", "co1"} {
		if !ids[id] {
			t.Fatalf("missing record %s in %+v", id, page.Messages)
		}
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), `"cwd"`) {
		t.Fatalf("cwd leaked: %s", encoded)
	}

	asTraex := scope
	asTraex.Agent = "traex"
	asTraex.Evidence = &AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: sessionID}
	traexPage := readTranscript(context.Background(), files, codec, asTraex, TranscriptRequest{})
	if traexPage.Binding != ChatBindingVerified || traexPage.Agent != "traex" || !strings.Contains(joinedText(traexPage.Messages), "traex 问题") {
		t.Fatalf("traex agent = %+v", traexPage)
	}
}

func traexSessionPath(home, date, name string) string {
	return filepath.Join(home, ".trae", "cli", "sessions", filepath.FromSlash(date), name+".jsonl")
}

func traexFixture(sessionID, cwd string) string {
	return strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"` + sessionID + `","session_id":"` + sessionID + `","cwd":"` + cwd + `","originator":"traex-tui"}}`,
		`{"type":"history_mutation","payload":{"version":1,"commit_id":"c1","operation":"append","turn_id":"t1","items":[` +
			`{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"traex 问题"}],"internal_chat_message_metadata_passthrough":{"ignored":true}},` +
			`{"type":"message","id":"d1","role":"developer","content":[{"type":"input_text","text":"DEV_SECRET"}]},` +
			`{"type":"reasoning","id":"r1","summary":[{"type":"summary_text","text":"THINK_SECRET"}]},` +
			`{"type":"function_call","id":"fc1","name":"read_file","arguments":{"path":"a.go"},"call_id":"call_1"},` +
			`{"type":"function_call_output","id":"fo1","call_id":"call_1","output":"file body"},` +
			`{"type":"custom_tool_call","id":"cc1","name":"apply_patch","input":{"patch":"x"},"call_id":"call_2"},` +
			`{"type":"custom_tool_call_output","id":"co1","call_id":"call_2","output":"patched"},` +
			`{"type":"trae_extra_info","id":"x1","info":"EXTRA_SECRET"}]}}`,
		`{"type":"history_mutation","payload":{"operation":"replace","turn_id":"t1","items":[{"type":"message","id":"u9","role":"user","content":[{"type":"input_text","text":"REPLACE_SECRET"}]}]}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"agent_message","id":"dup","content":[{"type":"output_text","text":"EVENT_SECRET"}]}}}`,
		`{"type":"token_usage_record","payload":{"total":1}}`,
		`{"type":"response_item","payload":{"type":"message","id":"plain1","role":"assistant","content":[{"type":"output_text","text":"plain codex line"}]}}`,
	}, "\n")
}

func joinedText(records []agentlog.Record) string {
	var builder strings.Builder
	for _, record := range records {
		for _, block := range record.Blocks {
			builder.WriteString(block.Text)
			builder.WriteString(block.Output)
			builder.WriteString(block.Name)
		}
	}
	return builder.String()
}

func TestPiIDAndAbsolutePathBothUseTheEncodedDirectory(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/proj"
	id := "11111111-2222-4333-8444-555555555555"
	name := "2024-12-03T14-00-00-000Z_" + id + ".jsonl"
	dir := filepath.Join(home, ".pi", "agent", "sessions", agentlog.PiProjectDir(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`{"type":"session","version":3,"id":"` + id + `","timestamp":"2024-12-03T14:00:00.000Z","cwd":"/srv/proj"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2024-12-03T14:00:01.000Z","message":{"role":"user","content":"pi 的问题"}}`,
		`{"type":"thinking_level_change","id":"h1","parentId":"u1","timestamp":"2024-12-03T14:00:02.000Z","thinkingLevel":"high"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, ".pi", "agent", "sessions", agentlog.PiProjectDir("/srv/other"))
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, name), []byte(strings.ReplaceAll(body, "/srv/proj", "/srv/other")), 0o600); err != nil {
		t.Fatal(err)
	}
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	scope := TranscriptScope{
		PaneID: "p", Agent: agentlog.AgentPi, CWD: cwd,
		Evidence: &AgentSession{Source: "herdr:pi", Agent: "pi", Kind: "id", Value: id},
	}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || page.Messages[0].Blocks[0].Text != "pi 的问题" {
		t.Fatalf("pi id = %+v", page)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), `"cwd"`) {
		t.Fatalf("cwd leaked: %s", encoded)
	}
	byPath := scope
	byPath.Evidence = &AgentSession{Source: "herdr:pi", Agent: "pi", Kind: "path", Value: "/tmp/not-used/" + name}
	pathPage := readTranscript(context.Background(), files, codec, byPath, TranscriptRequest{})
	if pathPage.Binding != ChatBindingVerified || pathPage.Messages[0].Blocks[0].Text != "pi 的问题" {
		t.Fatalf("pi path = %+v", pathPage)
	}
	mismatch := scope
	mismatch.CWD = "/srv/missing"
	if readTranscript(context.Background(), files, codec, mismatch, TranscriptRequest{}).Binding == ChatBindingVerified {
		t.Fatal("pi cwd mismatch verified")
	}
}

func TestGrokIDReadsOnlyTheEncodedCWD(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/proj"
	id := "22222222-2222-4333-8444-555555555555"
	dir := filepath.Join(home, ".grok", "sessions", agentlog.GrokProjectDir(cwd), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	history := strings.Join([]string{
		`{"type":"user","content":"grok 的问题","prompt_index":0}`,
		`{"type":"reasoning","id":"r","summary":[{"type":"summary_text","text":"GROK_SECRET"}]}`,
		`{"type":"assistant","content":"好"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "chat_history.jsonl"), []byte(history), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{\"type\":\"tool_started\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, ".grok", "sessions", agentlog.GrokProjectDir("/srv/other"), id)
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "chat_history.jsonl"), []byte("{\"type\":\"user\",\"content\":\"别人的 grok\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	scope := TranscriptScope{
		PaneID: "p", Agent: agentlog.AgentGrok, CWD: cwd,
		Evidence: &AgentSession{Source: "herdr:grok", Agent: "grok", Kind: "id", Value: id},
	}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || len(page.Messages) != 2 || page.Messages[0].Blocks[0].Text != "grok 的问题" {
		t.Fatalf("grok = %+v", page)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "GROK_SECRET") || strings.Contains(string(encoded), "别人的 grok") || strings.Contains(string(encoded), `"cwd"`) {
		t.Fatalf("grok leaked: %s", encoded)
	}
	asPath := scope
	asPath.Evidence = &AgentSession{Source: "herdr:test", Agent: "grok", Kind: "path", Value: agentlog.GrokProjectDir(cwd) + "/" + id + "/chat_history.jsonl"}
	if readTranscript(context.Background(), files, codec, asPath, TranscriptRequest{}).Binding == ChatBindingVerified {
		t.Fatal("grok path evidence was verified")
	}
}

func TestCursorHomeProjectIsNotHiddenByOtherTranscripts(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/shengtao"
	noise := filepath.Join(home, ".cursor", "projects", "zzz-noise", "agent-transcripts")
	for index := 0; index < 450; index++ {
		id := fmt.Sprintf("n%03d", index)
		dir := filepath.Join(noise, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{\"role\":\"user\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"noise\"}]}}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	payload, _ := json.Marshal(map[string]string{"workspacePath": "/srv/other"})
	if err := os.WriteFile(filepath.Join(home, ".cursor", "projects", "zzz-noise", ".workspace-trusted"), append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCursorTranscript(t, home, "mmm-home", "sess-home", cwd, `{"role":"user","message":{"content":[{"type":"text","text":"家里的问题"}]}}`)
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	page := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: agentlog.AgentCursor, CWD: cwd}, TranscriptRequest{})
	if page.Reason == ChatReasonReadLimitExceeded || len(page.Candidates) != 1 || page.Candidates[0].SessionID != "sess-home" {
		t.Fatalf("home cursor candidates = %+v", page)
	}
}

func TestIdentifiedCursorSessionReadsTheTail(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/shengtao"
	var lines []string
	lines = append(lines, `{"role":"user","message":{"content":[{"type":"text","text":"最早的 cursor"}]}}`)
	for index := 0; index < 4000; index++ {
		lines = append(lines, fmt.Sprintf(`{"role":"assistant","message":{"content":[{"type":"text","text":"pad %04d %s"}]}}`, index, strings.Repeat("x", 80)))
	}
	lines = append(lines, `{"role":"assistant","message":{"content":[{"type":"text","text":"最新的 cursor"}]}}`)
	writeCursorTranscript(t, home, "mmm-home", "sess-long", cwd, lines...)
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	scope := TranscriptScope{
		PaneID: "p", Agent: agentlog.AgentCursor, CWD: cwd,
		Evidence: &AgentSession{Source: "hook", Agent: "cursor", Kind: "id", Value: "sess-long"},
	}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Reason == ChatReasonReadLimitExceeded || page.Binding != ChatBindingVerified || !page.HasMore || page.PrevCursor == "" {
		t.Fatalf("tail = %+v", page)
	}
	if page.Messages[len(page.Messages)-1].Blocks[0].Text != "最新的 cursor" {
		t.Fatalf("last = %+v", page.Messages[len(page.Messages)-1])
	}
	earlier := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Before: page.PrevCursor})
	if earlier.Reason == ChatReasonReadLimitExceeded || len(earlier.Messages) == 0 || !earlier.HasMore {
		t.Fatalf("before = reason %q messages %d has_more %v", earlier.Reason, len(earlier.Messages), earlier.HasMore)
	}
	if recordOffset(t, earlier.Messages[len(earlier.Messages)-1].ID) >= recordOffset(t, page.Messages[0].ID) {
		t.Fatalf("before did not move older: %s then %s", earlier.Messages[len(earlier.Messages)-1].ID, page.Messages[0].ID)
	}
}

func recordOffset(t *testing.T, id string) int64 {
	t.Helper()
	index := strings.LastIndexByte(id, ':')
	if index < 0 {
		t.Fatalf("record id %q", id)
	}
	var offset int64
	if _, err := fmt.Sscan(id[index+1:], &offset); err != nil {
		t.Fatal(err)
	}
	return offset
}
