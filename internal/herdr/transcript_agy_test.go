package herdr

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
	_ "modernc.org/sqlite"
)

func TestAgyVerifiedIDReadsTheNamedDatabase(t *testing.T) {
	home := t.TempDir()
	const (
		uuid       = "11111111-2222-4333-8444-555555555555"
		userText   = "agy 用户问题"
		configText = "AGY_CONFIG_SECRET"
		thought    = "AGY_THOUGHT_SECRET"
	)
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := append(pbInt(1, "call_9"), pbInt(2, "read_file")...)
	tool = append(tool, pbInt(3, `{"path":"main.go"}`)...)
	planner := append(pbMsg(1, "planner answer"), pbMsg(3, thought)...)
	planner = append(planner, pbMsg(7, tool)...)
	steps := []struct {
		idx  int64
		kind int
		body []byte
	}{
		{1, 14, pbMsg(19, append(pbMsg(2, userText), pbMsg(12, pbMsg(1, configText))...))},
		{2, 15, pbMsg(20, planner)},
		{3, 99, pbMsg(2, "AGY_UNKNOWN_SECRET")},
	}
	writeAgySteps(t, filepath.Join(dir, uuid+".db"), steps)
	writeAgySteps(t, filepath.Join(dir, "22222222-2222-4333-8444-555555555555.db"), []struct {
		idx  int64
		kind int
		body []byte
	}{{1, 14, pbMsg(19, pbMsg(1, "别的会话"))}})

	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	scope := TranscriptScope{PaneID: "pane-agy", Agent: agentlog.AgentAgy, CWD: "/srv/proj"}
	plain := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if plain.Binding != "" || len(plain.Messages) != 0 || len(plain.Candidates) != 0 || plain.Reason != ChatReasonNoSessionCandidates {
		t.Fatalf("agy scanned without evidence: %+v", plain)
	}
	encoded, _ := json.Marshal(plain)
	if strings.Contains(string(encoded), userText) || strings.Contains(string(encoded), "别的会话") {
		t.Fatal("unbound agy page leaked text")
	}

	scope.Evidence = &AgentSession{Source: "herdr:antigravity_cli", Agent: agentlog.AgentAgy, Kind: "id", Value: uuid}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if !page.Supported || page.Reason != "" || page.Binding != ChatBindingVerified || page.SessionID != uuid {
		t.Fatalf("verified = %+v", page)
	}
	if len(page.Messages) != 2 || page.Messages[0].ID != "1" || page.Messages[0].Role != agentlog.RoleUser || page.Messages[0].Blocks[0].Text != userText {
		t.Fatalf("messages = %+v", page.Messages)
	}
	if page.Messages[1].Role != agentlog.RoleAssistant || page.Messages[1].Blocks[0].Text != "planner answer" || page.Messages[1].Blocks[1].Name != "read_file" {
		t.Fatalf("planner = %+v", page.Messages[1])
	}
	if page.Skipped != 1 {
		t.Fatalf("skipped = %d", page.Skipped)
	}
	body, _ := json.Marshal(page)
	for _, secret := range []string{configText, thought, "AGY_UNKNOWN_SECRET", "别的会话"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}

	for _, value := range []string{"not-a-uuid", "../" + uuid, uuid + ".db", "/tmp/" + uuid, "."} {
		bad := scope
		bad.Evidence = &AgentSession{Agent: agentlog.AgentAgy, Kind: "id", Value: value}
		rejected := readTranscript(context.Background(), files, codec, bad, TranscriptRequest{})
		if rejected.Binding == ChatBindingVerified {
			t.Fatalf("id %q verified", value)
		}
	}
	asPath := scope
	asPath.Evidence = &AgentSession{Agent: agentlog.AgentAgy, Kind: "path", Value: uuid + ".db"}
	if readTranscript(context.Background(), files, codec, asPath, TranscriptRequest{}).Binding == ChatBindingVerified {
		t.Fatal("agy path evidence was verified")
	}
	missing := scope
	missing.Evidence = &AgentSession{Agent: agentlog.AgentAgy, Kind: "id", Value: "33333333-2222-4333-8444-555555555555"}
	if readTranscript(context.Background(), files, codec, missing, TranscriptRequest{}).Binding == ChatBindingVerified {
		t.Fatal("missing agy file was verified")
	}
}

func TestAgyPagesByIdxAndKeepsTheTail(t *testing.T) {
	home := t.TempDir()
	const uuid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, uuid+".db")
	var steps []struct {
		idx  int64
		kind int
		body []byte
	}
	for idx := int64(1); idx <= 5; idx++ {
		steps = append(steps, struct {
			idx  int64
			kind int
			body []byte
		}{idx, 14, pbMsg(19, pbMsg(1, strings.Repeat("x", 60000)+string(rune('0'+idx))))})
	}
	writeAgySteps(t, path, steps)
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	scope := TranscriptScope{
		PaneID: "pane-agy", Agent: agentlog.AgentAgy, CWD: "/srv/proj",
		Evidence: &AgentSession{Agent: agentlog.AgentAgy, Kind: "id", Value: uuid},
	}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || !page.HasMore || page.PrevCursor == "" || len(page.Messages) == 0 {
		t.Fatalf("tail = %+v", page)
	}
	if page.Messages[len(page.Messages)-1].ID != "5" {
		t.Fatalf("tail ids = %s", idsOf(page.Messages))
	}
	earlier := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Before: page.PrevCursor})
	if earlier.Binding != ChatBindingVerified || len(earlier.Messages) == 0 || earlier.Messages[0].ID != "1" {
		t.Fatalf("earlier = %+v", earlier.Messages)
	}
	seen := map[string]bool{}
	for _, message := range append(append([]agentlog.Record{}, earlier.Messages...), page.Messages...) {
		if seen[message.ID] {
			t.Fatalf("duplicate %s", message.ID)
		}
		seen[message.ID] = true
	}
	for _, id := range []string{"1", "2", "3", "4", "5"} {
		if !seen[id] {
			t.Fatalf("missing %s in %v", id, seen)
		}
	}
	forward := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Cursor: page.NextCursor})
	if len(forward.Messages) != 0 || forward.Reset {
		t.Fatalf("forward before append = %+v", forward)
	}
	appendAgyStep(t, path, 6, 14, pbMsg(19, pbMsg(1, "第六条")))
	forward = readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Cursor: page.NextCursor})
	if len(forward.Messages) != 1 || forward.Messages[0].ID != "6" || forward.Messages[0].Blocks[0].Text != "第六条" {
		t.Fatalf("forward = %+v", forward.Messages)
	}
}

func TestAgySymlinkIsNotVerified(t *testing.T) {
	home := t.TempDir()
	const uuid = "bbbbbbbb-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "AGY_LINK_SECRET"
	real := filepath.Join(dir, "real.db")
	writeAgySteps(t, real, []struct {
		idx  int64
		kind int
		body []byte
	}{{1, 14, pbMsg(19, pbMsg(1, secret))}})
	if err := os.Symlink(real, filepath.Join(dir, uuid+".db")); err != nil {
		t.Fatal(err)
	}
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	scope := TranscriptScope{
		PaneID: "pane-agy", Agent: agentlog.AgentAgy, CWD: "/srv/proj",
		Evidence: &AgentSession{Agent: agentlog.AgentAgy, Kind: "id", Value: uuid},
	}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Binding == ChatBindingVerified {
		t.Fatal("symlink was verified")
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), secret) {
		t.Fatal("symlink target leaked")
	}
}

func TestAgyPythonReaderReturnsPayloads(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	home := t.TempDir()
	const uuid = "cccccccc-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeAgySteps(t, filepath.Join(dir, uuid+".db"), []struct {
		idx  int64
		kind int
		body []byte
	}{{4, 14, pbMsg(19, pbMsg(2, "python 行"))}})
	request, _ := json.Marshal(transcriptRemoteRequest{Op: "agy", Agent: "agy", Rel: uuid + ".db", AgyDesc: true, AgyLimit: 10})
	cmd := exec.Command("python3", "-c", transcriptReaderSource)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(string(request))
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var response transcriptRemoteResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || !response.End || len(response.Steps) != 1 || response.Steps[0].Idx != 4 || response.Steps[0].Data == "" {
		t.Fatalf("python agy = %+v", response)
	}
	denied, _ := json.Marshal(transcriptRemoteRequest{Op: "agy", Agent: "agy", Rel: "../" + uuid + ".db"})
	cmd = exec.Command("python3", "-c", transcriptReaderSource)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(string(denied))
	output, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	response = transcriptRemoteResponse{}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error != "denied" {
		t.Fatalf("python path = %+v", response)
	}
}

func idsOf(records []agentlog.Record) string {
	parts := make([]string, len(records))
	for i, record := range records {
		parts[i] = record.ID
	}
	return strings.Join(parts, ",")
}

func pbVar(value uint64) []byte {
	var out []byte
	for {
		byteValue := byte(value & 0x7f)
		value >>= 7
		if value == 0 {
			return append(out, byteValue)
		}
		out = append(out, byteValue|0x80)
	}
}

func pbMsg(field int, body any) []byte {
	var raw []byte
	switch typed := body.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = typed
	default:
		panic("bad pb body")
	}
	out := append(pbVar(uint64(field<<3|2)), pbVar(uint64(len(raw)))...)
	return append(out, raw...)
}

func pbInt(field int, text string) []byte {
	return pbMsg(field, text)
}

func writeAgySteps(t *testing.T, path string, steps []struct {
	idx  int64
	kind int
	body []byte
}) {
	t.Helper()
	db := openAgyDB(t, path)
	defer db.Close()
	for _, step := range steps {
		if _, err := db.Exec(`INSERT INTO steps (idx, step_type, status, step_format, step_payload) VALUES (?, ?, 3, 0, ?)`, step.idx, step.kind, step.body); err != nil {
			t.Fatal(err)
		}
	}
}

func appendAgyStep(t *testing.T, path string, idx int64, kind int, body []byte) {
	t.Helper()
	db := openAgyDB(t, path)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO steps (idx, step_type, status, step_format, step_payload) VALUES (?, ?, 3, 0, ?)`, idx, kind, body); err != nil {
		t.Fatal(err)
	}
}

func openAgyDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS steps (
		idx integer primary key, step_type integer, status integer, has_subtrajectory numeric,
		metadata blob, error_details blob, permissions blob, task_details blob,
		render_info blob, step_payload blob, step_format integer)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}
