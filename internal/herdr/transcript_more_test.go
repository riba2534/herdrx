package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
)

func TestTraexUsesTheCodexReader(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/proj"
	writeLines(t, codexSessionPath(home, "2026/09/13", "rollout-2026-09-13T04-25-03-traex"), []string{
		codexMeta("sess-traex", cwd),
		codexMessage("user", "traex 的问题"),
	})
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	page := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: "traex", CWD: cwd}, TranscriptRequest{})
	if !page.Supported || page.Agent != "traex" || len(page.Candidates) != 1 || page.Binding != "" {
		t.Fatalf("traex candidates = %+v", page)
	}
	bound := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: "traecli", CWD: cwd}, TranscriptRequest{Session: page.Candidates[0].ID})
	if bound.Agent != "traecli" || len(bound.Messages) != 1 || bound.Messages[0].Blocks[0].Text != "traex 的问题" {
		t.Fatalf("traex page = %+v", bound)
	}
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
