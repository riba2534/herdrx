package herdr

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
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
		bad.Evidence = &AgentSession{Source: "herdr:antigravity_cli", Agent: agentlog.AgentAgy, Kind: "id", Value: value}
		rejected := readTranscript(context.Background(), files, codec, bad, TranscriptRequest{})
		if rejected.Binding == ChatBindingVerified {
			t.Fatalf("id %q verified", value)
		}
	}
	asPath := scope
	asPath.Evidence = &AgentSession{Source: "herdr:antigravity_cli", Agent: agentlog.AgentAgy, Kind: "path", Value: uuid + ".db"}
	if readTranscript(context.Background(), files, codec, asPath, TranscriptRequest{}).Binding == ChatBindingVerified {
		t.Fatal("agy path evidence was verified")
	}
	// 证据来源必须是 Herdr 的 antigravity_cli 集成，herdr:agy 或第三方来源都不认。
	for _, source := range []string{"herdr:agy", "custom:agy", ""} {
		foreign := scope
		foreign.Evidence = &AgentSession{Source: source, Agent: agentlog.AgentAgy, Kind: "id", Value: uuid}
		if readTranscript(context.Background(), files, codec, foreign, TranscriptRequest{}).Binding == ChatBindingVerified {
			t.Fatalf("source %q verified", source)
		}
	}
	missing := scope
	missing.Evidence = &AgentSession{Source: "herdr:antigravity_cli", Agent: agentlog.AgentAgy, Kind: "id", Value: "33333333-2222-4333-8444-555555555555"}
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
		Evidence: &AgentSession{Source: "herdr:antigravity_cli", Agent: agentlog.AgentAgy, Kind: "id", Value: uuid},
	}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || !page.HasMore || page.PrevCursor == "" || len(page.Messages) == 0 {
		t.Fatalf("tail = %+v", page)
	}
	if page.Messages[len(page.Messages)-1].ID != "5" {
		t.Fatalf("tail ids = %s", idsOf(page.Messages))
	}
	earlier := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: page.Session, Before: page.PrevCursor})
	if earlier.Binding != ChatBindingSelected || len(earlier.Messages) == 0 || earlier.Messages[0].ID != "1" {
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
	forward := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: page.Session, Cursor: page.NextCursor})
	if len(forward.Messages) != 0 || forward.Reset {
		t.Fatalf("forward before append = %+v", forward)
	}
	appendAgyStep(t, path, 6, 14, pbMsg(19, pbMsg(1, "第六条")))
	forward = readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: page.Session, Cursor: page.NextCursor})
	if len(forward.Messages) != 1 || forward.Messages[0].ID != "6" || forward.Messages[0].Blocks[0].Text != "第六条" {
		t.Fatalf("forward = %+v", forward.Messages)
	}
}

// agy 令牌不能拿到别的 pane 用：续读要求当前 pane 的证据仍指向令牌里的会话。
func TestAgySessionTokenNeedsThisPanesEvidence(t *testing.T) {
	home := t.TempDir()
	const mine = "abababab-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	const theirs = "cdcdcdcd-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{mine, theirs} {
		writeAgySteps(t, filepath.Join(dir, id+".db"), []struct {
			idx  int64
			kind int
			body []byte
		}{{1, 14, pbMsg(19, pbMsg(1, "AGY_TEXT_"+id))}})
	}
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	paneA := TranscriptScope{PaneID: "a", Agent: agentlog.AgentAgy, CWD: "/srv/a",
		Evidence: &AgentSession{Source: "herdr:antigravity_cli", Agent: agentlog.AgentAgy, Kind: "id", Value: theirs}}
	first := readTranscript(context.Background(), files, codec, paneA, TranscriptRequest{})
	if first.Binding != ChatBindingVerified || first.Session == "" {
		t.Fatalf("pane a = %+v", first)
	}
	own := readTranscript(context.Background(), files, codec, paneA, TranscriptRequest{Session: first.Session, Cursor: first.NextCursor})
	if own.Reason != "" || own.SessionID != theirs {
		t.Fatalf("own continuation = %+v", own)
	}
	for name, evidence := range map[string]*AgentSession{
		"no evidence":    nil,
		"other session":  {Source: "herdr:antigravity_cli", Agent: agentlog.AgentAgy, Kind: "id", Value: mine},
		"foreign source": {Source: "custom:agy", Agent: agentlog.AgentAgy, Kind: "id", Value: theirs},
	} {
		paneB := TranscriptScope{PaneID: "b", Agent: agentlog.AgentAgy, CWD: "/srv/b", Evidence: evidence}
		page := readTranscript(context.Background(), files, codec, paneB, TranscriptRequest{Session: first.Session})
		encoded, _ := json.Marshal(page)
		if page.Reason != ChatReasonSessionUnavailable || len(page.Messages) != 0 || strings.Contains(string(encoded), "AGY_TEXT_"+theirs) {
			t.Fatalf("%s: reused token = %+v", name, page)
		}
	}
}

// 尾部一整轮扫描全是非消息步时，页面没有记录也要给 prev 游标，能继续加载到更早的对话。
func TestAgyEmptyScanStillOffersEarlierPages(t *testing.T) {
	home := t.TempDir()
	const uuid = "efefefef-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		idx  int64
		kind int
		body []byte
	}{{1, 14, pbMsg(19, pbMsg(1, "最早的问题"))}}
	for idx := int64(2); idx <= 16*64+20; idx++ {
		steps = append(steps, struct {
			idx  int64
			kind int
			body []byte
		}{idx, 99, pbMsg(2, "noise")})
	}
	writeAgySteps(t, filepath.Join(dir, uuid+".db"), steps)
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	scope := TranscriptScope{PaneID: "p", Agent: agentlog.AgentAgy, CWD: "/srv/proj",
		Evidence: &AgentSession{Source: "herdr:antigravity_cli", Agent: agentlog.AgentAgy, Kind: "id", Value: uuid}}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || len(page.Messages) != 0 || !page.HasMore || page.PrevCursor == "" {
		t.Fatalf("empty tail = messages %d has_more %v prev %q", len(page.Messages), page.HasMore, page.PrevCursor)
	}
	before := page.PrevCursor
	for round := 0; round < 4 && before != ""; round++ {
		earlier := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: page.Session, Before: before})
		if len(earlier.Messages) > 0 {
			if earlier.Messages[0].Blocks[0].Text != "最早的问题" {
				t.Fatalf("earliest = %+v", earlier.Messages)
			}
			return
		}
		before = earlier.PrevCursor
	}
	t.Fatal("never reached the earliest step")
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
		Evidence: &AgentSession{Source: "herdr:antigravity_cli", Agent: agentlog.AgentAgy, Kind: "id", Value: uuid},
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
	// 家目录里带 URI 特殊字符：sqlite 的 file: URI 必须百分号转义，否则 `?`/`#` 会截断路径。
	home := filepath.Join(t.TempDir(), "h?#%41 x")
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

// 远端 python3 缺 _sqlite3 时，只有 agy 降级为不支持；claude 等读取器照常工作。
func TestPythonReaderWithoutSQLiteOnlyDegradesAgy(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	home := t.TempDir()
	cwd := "/srv/proj"
	writeLines(t, claudeSessionPath(home, cwd, "sess-1"), []string{claudeUser("u-1", cwd, "无 sqlite 也能读")})
	const uuid = "dddddddd-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeAgySteps(t, filepath.Join(dir, uuid+".db"), []struct {
		idx  int64
		kind int
		body []byte
	}{{1, 14, pbMsg(19, pbMsg(1, "x"))}})
	run := func(request transcriptRemoteRequest) transcriptRemoteResponse {
		t.Helper()
		payload, _ := json.Marshal(request)
		cmd := exec.Command("python3", "-c", "import sys\nsys.modules['sqlite3'] = None\n"+transcriptReaderSource)
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = strings.NewReader(string(payload))
		output, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		var response transcriptRemoteResponse
		if err := json.Unmarshal(output, &response); err != nil {
			t.Fatalf("%s: %v", output, err)
		}
		return response
	}
	listed := run(transcriptRemoteRequest{Op: "list", Agent: "claude", Rel: agentlog.ClaudeProjectDir(cwd), Depth: 1})
	if !listed.OK || len(listed.Entries) != 1 {
		t.Fatalf("claude list without sqlite3 = %+v", listed)
	}
	agy := run(transcriptRemoteRequest{Op: "agy", Agent: "agy", Rel: uuid + ".db", AgyLimit: 10})
	if agy.OK || agy.Error != "agy_unsupported" {
		t.Fatalf("agy without sqlite3 = %+v", agy)
	}
}

// sqlite 按路径名重开时若落到另一个 inode（核对后中间目录被换掉），远端读取器丢弃结果。
// 这里把 sqlite3.connect 换成打开另一份库来模拟那次换路径。
func TestPythonAgyRejectsAnotherInodeAfterOpen(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	home := t.TempDir()
	const uuid = "eeeeeeee-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	step := []struct {
		idx  int64
		kind int
		body []byte
	}{{1, 14, pbMsg(19, pbMsg(1, "SWAPPED_SECRET"))}}
	writeAgySteps(t, filepath.Join(dir, uuid+".db"), step)
	other := filepath.Join(t.TempDir(), "other.db")
	writeAgySteps(t, other, step)
	// 进程里另有一个 fd 一直开着核对过的那份库（长驻进程的另一个连接），它不能替 sqlite 作证。
	prelude := "import os, sqlite3\n_held = open(os.environ['REAL_DB'], 'rb')\n_connect = sqlite3.connect\nsqlite3.connect = lambda target, **kw: _connect('file:' + os.environ['OTHER_DB'] + '?mode=ro', uri=True)\n"
	payload, _ := json.Marshal(transcriptRemoteRequest{Op: "agy", Agent: "agy", Rel: uuid + ".db", AgyLimit: 10})
	cmd := exec.Command("python3", "-c", prelude+transcriptReaderSource)
	cmd.Env = append(os.Environ(), "HOME="+home, "OTHER_DB="+other, "REAL_DB="+filepath.Join(dir, uuid+".db"))
	cmd.Stdin = strings.NewReader(string(payload))
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var response transcriptRemoteResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error != "denied" || len(response.Steps) != 0 {
		t.Fatalf("swapped open = %+v", response)
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
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
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
