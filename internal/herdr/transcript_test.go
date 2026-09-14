package herdr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// 全部 fixture 都是**合成**的：形状抄自公开参考实现与真实 CLI 的字段名，
// 但不含任何真实用户会话内容。测试只碰 t.TempDir()，绝不读取真实 HOME。

func newTranscriptCodecForTest(t *testing.T) transcriptCodec {
	t.Helper()
	codec, err := newTranscriptCodec()
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendRaw(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func claudeUser(uuid, cwd, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":"2026-09-13T04:25:03.000Z","cwd":%q,"message":{"role":"user","content":%q}}`, uuid, cwd, text)
}

func claudeAssistant(uuid, cwd, text string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"timestamp":"2026-09-13T04:25:04.000Z","cwd":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, uuid, cwd, text)
}

func claudeSessionPath(home, cwd, session string) string {
	return filepath.Join(home, ".claude", "projects", agentlog.ClaudeProjectDir(cwd), session+".jsonl")
}

func codexSessionPath(home, date, session string) string {
	return filepath.Join(home, ".codex", "sessions", filepath.FromSlash(date), session+".jsonl")
}

func codexMeta(session, cwd string) string {
	return fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-09-13T04:25:00.000Z","payload":{"id":%q,"cwd":%q,"cli_version":"0.1.0"}}`, session, cwd)
}

func codexMessage(role, text string) string {
	return fmt.Sprintf(`{"type":"response_item","timestamp":"2026-09-13T04:25:03.000Z","payload":{"type":"message","id":"m-%s","role":%q,"content":[{"type":"input_text","text":%q}]}}`, role, role, text)
}

// nextOffset 读出游标里的字节偏移。游标每次都带新随机数，只能比偏移，不能比字符串。
func nextOffset(t *testing.T, codec transcriptCodec, token string) int64 {
	t.Helper()
	parts, ok := codec.open(token, 4)
	if !ok {
		t.Fatalf("cursor did not open: %q", token)
	}
	offset, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		t.Fatalf("cursor offset %q: %v", parts[3], err)
	}
	return offset
}

func claudeScope(cwd string) TranscriptScope {
	return TranscriptScope{PaneID: "pane-1", Agent: agentlog.AgentClaude, CWD: cwd}
}

func codexScope(cwd string) TranscriptScope {
	return TranscriptScope{PaneID: "pane-1", Agent: agentlog.AgentCodex, CWD: cwd}
}

func mustCandidates(t *testing.T, page TranscriptPage, want int) TranscriptPage {
	t.Helper()
	if !page.Supported {
		t.Fatalf("page not supported: %+v", page)
	}
	if len(page.Candidates) != want {
		t.Fatalf("candidates = %+v, want %d", page.Candidates, want)
	}
	if len(page.Messages) != 0 {
		t.Fatalf("candidates page returned messages: %+v", page.Messages)
	}
	return page
}

// 同一编码目录下必须按记录里的真实 cwd 精确过滤：编码有损，`/a-b` 与 `/a/b` 同码。
func TestTranscriptFiltersCandidatesByExactRecordCWD(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"

	writeLines(t, claudeSessionPath(home, cwd, "sess-mine"), []string{claudeUser("u-1", cwd, "我的问题")})
	// 落在同一个编码目录里、却记录了别的 cwd 的兄弟会话：绝不能因为「目录名一样」被认领。
	writeLines(t, claudeSessionPath(home, "/srv/other", "sess-foreign"), []string{claudeUser("u-2", "/srv/other", "别人的问题")})

	page := readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{})
	if !page.Supported {
		t.Fatalf("candidates page not supported: %+v", page)
	}
	if page.Reason != "" {
		t.Fatalf("unexpected reason: %q", page.Reason)
	}
	if len(page.Candidates) != 1 || page.Candidates[0].SessionID != "sess-mine" {
		t.Fatalf("candidates = %+v, want exactly sess-mine", page.Candidates)
	}
	if page.Candidates[0].Agent != agentlog.AgentClaude || page.Candidates[0].UpdatedAt == "" {
		t.Fatalf("candidate shape = %+v", page.Candidates[0])
	}
	// 即使候选只剩一个也**不自动认领**。
	if page.Binding != "" {
		t.Fatalf("binding claimed without selection: %q", page.Binding)
	}
}

func TestTranscriptEncodingCollisionIsNotOwnership(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}

	// `/a-b` 与 `/a/b` 编码成同一个目录名。pane 在 `/a/b`，磁盘上的会话属于 `/a-b`。
	if agentlog.ClaudeProjectDir("/a-b") != agentlog.ClaudeProjectDir("/a/b") {
		t.Fatal("fixture no longer exercises the documented collision")
	}
	writeLines(t, claudeSessionPath(home, "/a-b", "sess-1"), []string{claudeUser("u-1", "/a-b", "别人的问题")})

	page := readTranscript(context.Background(), files, codec, claudeScope("/a/b"), TranscriptRequest{})
	if !page.Supported || len(page.Candidates) != 0 || page.Reason != ChatReasonNoSessionCandidates {
		t.Fatalf("collision was claimed as ours: %+v", page)
	}
}

func TestTranscriptCursorReturnsOnlyNewRecords(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	path := claudeSessionPath(home, cwd, "sess-1")
	writeLines(t, path, []string{claudeUser("u-1", cwd, "第一问"), claudeAssistant("a-1", cwd, "第一答")})

	scope := claudeScope(cwd)
	candidates := mustCandidates(t, readTranscript(context.Background(), files, codec, scope, TranscriptRequest{}), 1)
	session := candidates.Candidates[0].ID

	bound := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session})
	if bound.Binding != ChatBindingSelected || len(bound.Messages) != 2 {
		t.Fatalf("initial page = %+v", bound)
	}
	if bound.NextCursor == "" || bound.PrevCursor != "" {
		t.Fatalf("initial cursors = next %q previous %q", bound.NextCursor, bound.PrevCursor)
	}
	if bound.Messages[0].Role != agentlog.RoleUser || bound.Messages[1].Role != agentlog.RoleAssistant {
		t.Fatalf("roles = %+v", bound.Messages)
	}

	// 没有新内容时再取一次必须是空页，不重放已有记录。
	empty := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Cursor: bound.NextCursor})
	if len(empty.Messages) != 0 || empty.Reset || empty.HasMore {
		t.Fatalf("idle poll returned %+v", empty)
	}
	if empty.Binding != ChatBindingSelected {
		t.Fatalf("idle poll lost the binding: %+v", empty)
	}

	appendRaw(t, path, claudeUser("u-2", cwd, "第二问")+"\n"+claudeAssistant("a-2", cwd, "第二答")+"\n")
	next := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Cursor: bound.NextCursor})
	if next.Reset || len(next.Messages) != 2 {
		t.Fatalf("incremental page = %+v", next)
	}
	if next.Messages[0].ID != "u-2" || next.Messages[1].ID != "a-2" {
		t.Fatalf("increment carried wrong records: %+v", next.Messages)
	}
}

// 尾部未闭合的半行不解析、不产出、游标不推进；补齐后整行重读。
func TestTranscriptHalfLineIsNotConsumed(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	path := claudeSessionPath(home, cwd, "sess-1")
	writeLines(t, path, []string{claudeUser("u-1", cwd, "完整的一行")})

	scope := claudeScope(cwd)
	candidates := mustCandidates(t, readTranscript(context.Background(), files, codec, scope, TranscriptRequest{}), 1)
	session := candidates.Candidates[0].ID
	first := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session})
	if len(first.Messages) != 1 {
		t.Fatalf("first page = %+v", first.Messages)
	}

	whole := claudeUser("u-2", cwd, "写到一半就被读走了")
	appendRaw(t, path, whole[:len(whole)/2])

	half := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Cursor: first.NextCursor})
	if len(half.Messages) != 0 {
		t.Fatalf("half line was parsed: %+v", half.Messages)
	}
	if nextOffset(t, codec, half.NextCursor) != nextOffset(t, codec, first.NextCursor) {
		t.Fatalf("cursor advanced past a half line: %q -> %q", first.NextCursor, half.NextCursor)
	}
	if half.Skipped != 0 {
		t.Fatalf("half line counted as unrecognized: %d", half.Skipped)
	}

	appendRaw(t, path, whole[len(whole)/2:]+"\n")

	completed := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Cursor: first.NextCursor})
	if len(completed.Messages) != 1 || completed.Messages[0].ID != "u-2" {
		t.Fatalf("completed line not delivered: %+v", completed.Messages)
	}
	if completed.Messages[0].Blocks[0].Text != "写到一半就被读走了" {
		t.Fatalf("half line was stitched wrong: %+v", completed.Messages[0].Blocks)
	}
}

// 文件被截断到游标之前 → reset，客户端整体清空重建。
func TestTranscriptRotationResets(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	path := claudeSessionPath(home, cwd, "sess-1")
	lines := []string{claudeUser("u-1", cwd, "第一问"), claudeAssistant("a-1", cwd, strings.Repeat("很长很长的第一答", 200))}
	writeLines(t, path, lines)

	scope := claudeScope(cwd)
	candidates := mustCandidates(t, readTranscript(context.Background(), files, codec, scope, TranscriptRequest{}), 1)
	session := candidates.Candidates[0].ID
	bound := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session})
	if bound.PrevCursor != "" {
		t.Fatalf("small file should not offer a previous cursor: %q", bound.PrevCursor)
	}

	// 轮转：只留下第一行，游标指向的偏移已经超过文件大小。
	writeLines(t, path, lines[:1])
	rotated := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Cursor: bound.NextCursor})
	if !rotated.Reset {
		t.Fatalf("rotation did not reset: %+v", rotated)
	}
	if len(rotated.Messages) != 1 || rotated.Messages[0].ID != "u-1" {
		t.Fatalf("reset page did not re-read from the tail: %+v", rotated.Messages)
	}
	if rotated.Binding != ChatBindingSelected {
		t.Fatalf("reset page lost the binding: %+v", rotated)
	}
}

// 文件被换成另一个项目（cwd 不同）→ session 失效，重新给候选并置 reset。
func TestTranscriptReplacedFileInvalidatesSession(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	path := claudeSessionPath(home, cwd, "sess-1")
	writeLines(t, path, []string{claudeUser("u-1", cwd, "原始会话")})

	scope := claudeScope(cwd)
	candidates := mustCandidates(t, readTranscript(context.Background(), files, codec, scope, TranscriptRequest{}), 1)
	session := candidates.Candidates[0].ID

	writeLines(t, path, []string{claudeUser("u-9", "/srv/other", "完全不同的项目")})
	lost := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session})
	if lost.Reason != ChatReasonSessionUnavailable || !lost.Reset {
		t.Fatalf("cwd mismatch did not invalidate the session: %+v", lost)
	}
	if len(lost.Messages) != 0 || lost.Binding != "" {
		t.Fatalf("invalidated session still returned messages: %+v", lost)
	}
	if !lost.Supported {
		t.Fatalf("session loss must still be a supported answer: %+v", lost)
	}
}

// 客户端传回的只是不透明 id；篡改后的 id 即使能被解开，落点仍必须回到「当前 pane 的
// 那一个日志根内的合法会话文件」，不能变成任意路径读取。
func TestTranscriptRejectsSessionThatEscapesTheLogRoot(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	writeLines(t, claudeSessionPath(home, cwd, "sess-1"), []string{claudeUser("u-1", cwd, "会话")})
	writeLines(t, filepath.Join(home, "secret.jsonl"), []string{claudeUser("u-1", cwd, "不该被读到")})

	for _, rel := range []string{"../../secret.jsonl", "..", "/etc/passwd", "a/../../b.jsonl", ".ssh/id_ed25519", "a\\b.jsonl", "-srv-proj/../../secret.jsonl"} {
		token, err := codec.seal(agentlog.AgentClaude, "sess-1", rel)
		if err != nil {
			t.Fatal(err)
		}
		page := readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{Session: token})
		if page.Reason != ChatReasonSessionUnavailable || page.Binding != "" || len(page.Messages) != 0 {
			t.Fatalf("escaping rel %q was not rejected: %+v", rel, page)
		}
	}
}

func TestTranscriptRefusesSymlinkedSessionFile(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"

	real := filepath.Join(home, "elsewhere", "target.jsonl")
	writeLines(t, real, []string{claudeUser("u-1", cwd, "真实内容")})

	linkDir := filepath.Join(home, ".claude", "projects", agentlog.ClaudeProjectDir(cwd))
	if err := os.MkdirAll(linkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(linkDir, "sess-link.jsonl")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	page := readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{})
	if !page.Supported || len(page.Candidates) != 0 {
		t.Fatalf("symlinked session became a candidate: %+v", page)
	}

	// 即便有人把 id 造出来指向那个符号链接，读取也必须被拒。
	token, err := codec.seal(agentlog.AgentClaude, "sess-link", agentlog.ClaudeProjectDir(cwd)+"/sess-link.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	denied := readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{Session: token})
	if denied.Reason != ChatReasonReadDenied || len(denied.Messages) != 0 {
		t.Fatalf("symlinked file was read: %+v", denied)
	}
}

func TestTranscriptRefusesSymlinkedParentDirectory(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"

	// `.claude` 本身是指向别处的符号链接：根以下的任何一段都必须拒绝。
	realRoot := filepath.Join(home, "real-claude")
	writeLines(t, filepath.Join(realRoot, "projects", agentlog.ClaudeProjectDir(cwd), "sess-1.jsonl"), []string{claudeUser("u-1", cwd, "内容")})
	if err := os.Symlink(realRoot, filepath.Join(home, ".claude")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	page := readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{})
	if page.Supported || page.Reason != ChatReasonReadDenied {
		t.Fatalf("symlinked parent directory was followed: %+v", page)
	}
}

func TestTranscriptDegradesForUnknownAgentAndMissingCWD(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}

	shell := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: "", CWD: "/srv"}, TranscriptRequest{})
	if shell.Supported || shell.Reason != ChatReasonNoAgent {
		t.Fatalf("shell pane = %+v", shell)
	}
	other := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: "gemini", CWD: "/srv"}, TranscriptRequest{})
	if other.Supported || other.Reason != ChatReasonUnsupportedAgent {
		t.Fatalf("unknown agent = %+v", other)
	}
	if other.Agent != "" {
		t.Fatalf("agent %q leaked outside the closed set", other.Agent)
	}
	noCWD := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: agentlog.AgentClaude}, TranscriptRequest{})
	if noCWD.Supported || noCWD.Reason != ChatReasonCWDUnavailable {
		t.Fatalf("missing cwd = %+v", noCWD)
	}
	relative := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: agentlog.AgentClaude, CWD: "srv/proj"}, TranscriptRequest{})
	if relative.Supported || relative.Reason != ChatReasonCWDUnavailable {
		t.Fatalf("relative cwd = %+v", relative)
	}
}

func TestTranscriptReportsMissingLogRoot(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	page := readTranscript(context.Background(), files, codec, claudeScope("/srv/proj"), TranscriptRequest{})
	if page.Supported || page.Reason != ChatReasonLogRootUnavailable {
		t.Fatalf("missing log root = %+v", page)
	}
	// 日志根存在、但这个 cwd 还没有目录：这是「候选为空」，不是能力缺失。
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	empty := readTranscript(context.Background(), files, codec, claudeScope("/srv/proj"), TranscriptRequest{})
	if !empty.Supported || empty.Reason != ChatReasonNoSessionCandidates {
		t.Fatalf("empty cwd dir = %+v", empty)
	}
}

type stubTranscriptFS struct {
	listErr error
	readErr error
	read    []transcriptReadResult
}

func (s stubTranscriptFS) List(context.Context, transcriptRootKind, string, int) ([]transcriptEntry, error) {
	return nil, s.listErr
}

func (s stubTranscriptFS) Read(context.Context, transcriptRootKind, []transcriptReadSpec) ([]transcriptReadResult, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return s.read, nil
}

func TestTranscriptMapsTransportErrorsToContractReasons(t *testing.T) {
	codec := newTranscriptCodecForTest(t)
	cases := []struct {
		err  error
		want string
	}{
		{errTranscriptTransport, ChatReasonUnsupportedTransport},
		{errTranscriptDenied, ChatReasonReadDenied},
		{errTranscriptRoot, ChatReasonLogRootUnavailable},
		// 「日志根在、但这个 cwd 的目录还没有」是正常的空态，不是能力缺失。
		{errTranscriptNotFound, ChatReasonNoSessionCandidates},
		{errors.New("boom"), ChatReasonInternalError},
	}
	for _, testCase := range cases {
		page := readTranscript(context.Background(), stubTranscriptFS{listErr: testCase.err}, codec, claudeScope("/srv/proj"), TranscriptRequest{})
		if testCase.want == ChatReasonNoSessionCandidates {
			if !page.Supported || page.Reason != testCase.want {
				t.Fatalf("err %v mapped to %+v, want %q", testCase.err, page, testCase.want)
			}
			continue
		}
		if page.Supported || page.Reason != testCase.want {
			t.Fatalf("err %v mapped to %+v, want %q", testCase.err, page, testCase.want)
		}
		if page.Messages == nil {
			t.Fatalf("err %v produced a nil messages array", testCase.err)
		}
	}
}

// 分页必须停在**完整记录**边界上：触顶时下一次请求从那里继续，既不重复也不丢。
func TestTranscriptPageLimitStopsOnCompleteRecords(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	path := claudeSessionPath(home, cwd, "sess-1")
	total := transcriptPageRecords + 20
	lines := make([]string, 0, total)
	for index := 0; index < total; index++ {
		lines = append(lines, claudeUser("u-"+strconv.Itoa(index), cwd, "问题 "+strconv.Itoa(index)))
	}
	writeLines(t, path, lines)

	scope := claudeScope(cwd)
	candidates := mustCandidates(t, readTranscript(context.Background(), files, codec, scope, TranscriptRequest{}), 1)
	session := candidates.Candidates[0].ID

	// 首屏给最新的 200 条，更早的走 previous 反向翻页。
	newest := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session})
	if len(newest.Messages) != transcriptPageRecords || !newest.HasMore || newest.PrevCursor == "" {
		t.Fatalf("first page = %d messages has_more=%v prev=%v", len(newest.Messages), newest.HasMore, newest.PrevCursor != "")
	}
	earlier := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Before: newest.PrevCursor})
	if len(earlier.Messages) != 20 || earlier.HasMore || earlier.PrevCursor != "" {
		t.Fatalf("older page = %d messages has_more=%v prev=%v", len(earlier.Messages), earlier.HasMore, earlier.PrevCursor != "")
	}
	seen := make(map[string]bool)
	for _, record := range append(append([]agentlog.Record{}, newest.Messages...), earlier.Messages...) {
		if seen[record.ID] {
			t.Fatalf("record %q delivered twice across pages", record.ID)
		}
		seen[record.ID] = true
	}
	if len(seen) != total {
		t.Fatalf("pagination lost records: %d of %d", len(seen), total)
	}
	// 首屏的最新一条之后的 next_cursor 必须停在文件末尾：再拉一次没有新内容。
	idle := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Cursor: newest.NextCursor})
	if len(idle.Messages) != 0 || idle.Reset {
		t.Fatalf("forward poll after the newest page = %+v", idle)
	}
}

func TestTranscriptReportsSkippedWithoutAlarmingOnMetadata(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	path := claudeSessionPath(home, cwd, "sess-1")
	writeLines(t, path, []string{
		`{"type":"summary","summary":"会话摘要","leafUuid":"u-1"}`,
		claudeUser("u-1", cwd, "正常问题"),
		`{"type":"assistant","uuid":"a-bad","cwd":"` + cwd + `","message":{"role":"assistant","content":[{"type":"server_tool_use","id":"x"}]}}`,
	})

	scope := claudeScope(cwd)
	candidates := mustCandidates(t, readTranscript(context.Background(), files, codec, scope, TranscriptRequest{}), 1)
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: candidates.Candidates[0].ID})
	if page.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (metadata must not be counted)", page.Skipped)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != "u-1" {
		t.Fatalf("messages = %+v", page.Messages)
	}
}

// 首屏不拉全量：无游标时只取尾部窗口，并且可以一路向前翻到最早一条。
func TestTranscriptInitialWindowIsTailAndPagesBackToTheStart(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	path := claudeSessionPath(home, cwd, "sess-1")
	total := 250
	lines := make([]string, 0, total)
	for index := 0; index < total; index++ {
		lines = append(lines, claudeUser("u-"+strconv.Itoa(index), cwd, "第 "+strconv.Itoa(index)+" 个问题 "+strings.Repeat("x", 4000)))
	}
	writeLines(t, path, lines)

	scope := claudeScope(cwd)
	candidates := mustCandidates(t, readTranscript(context.Background(), files, codec, scope, TranscriptRequest{}), 1)
	session := candidates.Candidates[0].ID

	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session})
	if len(page.Messages) == 0 {
		t.Fatal("initial window returned nothing")
	}
	if page.PrevCursor == "" {
		t.Fatal("initial window did not offer a previous cursor for earlier history")
	}
	if page.Messages[0].ID == "u-0" {
		t.Fatalf("initial window pulled the whole file instead of the tail: %q", page.Messages[0].ID)
	}
	seen := make(map[string]bool, total)
	for _, record := range page.Messages {
		seen[record.ID] = true
	}
	for round := 0; page.PrevCursor != ""; round++ {
		if round > 50 {
			t.Fatal("reverse paging did not terminate")
		}
		page = readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Before: page.PrevCursor})
		if page.Reset {
			t.Fatalf("reverse page reset: %+v", page)
		}
		if len(page.Messages) == 0 {
			t.Fatalf("reverse page empty while prev_cursor was set: %+v", page)
		}
		for _, record := range page.Messages {
			seen[record.ID] = true
		}
	}
	if len(seen) != total {
		t.Fatalf("reverse paging covered %d of %d records", len(seen), total)
	}
	if !seen["u-0"] {
		t.Fatal("reverse paging never reached the oldest record")
	}
}

// 正向游标页不得产出 previous_cursor。
//
// 回归：正向游标页解出的第一条记录也晚于客户端已经持有的最旧记录；把它当 before 锚点会让
// 「加载更早」原地打转 —— 客户端拿回的是一整页自己已经有的记录，条数一条都不涨。
// 只有初始页 / before 反向页 / reset 重来页才会（在确实还有更早内容时）给出 previous_cursor。
func TestTranscriptForwardCursorPageDoesNotOfferPrevious(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	path := claudeSessionPath(home, cwd, "sess-1")
	total := 260
	lines := make([]string, 0, total)
	for index := 0; index < total; index++ {
		lines = append(lines, claudeUser("u-"+strconv.Itoa(index), cwd, "第 "+strconv.Itoa(index)+" 个问题"))
	}
	writeLines(t, path, lines)

	scope := claudeScope(cwd)
	session := mustCandidates(t, readTranscript(context.Background(), files, codec, scope, TranscriptRequest{}), 1).Candidates[0].ID

	initial := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session})
	if len(initial.Messages) != 200 {
		t.Fatalf("initial page returned %d records, want the 200-record page limit", len(initial.Messages))
	}
	if initial.PrevCursor == "" {
		t.Fatal("initial page must offer a previous cursor")
	}
	initialOldest := initial.Messages[0].ID

	// 客户端追平的增量轮询：游标在文件末尾，没有任何新记录。
	caughtUp := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Cursor: initial.NextCursor})
	if len(caughtUp.Messages) != 0 {
		t.Fatalf("caught-up cursor returned %d records", len(caughtUp.Messages))
	}
	if caughtUp.PrevCursor != "" {
		t.Fatalf("a forward cursor page must not offer a previous cursor: %q", caughtUp.PrevCursor)
	}

	// 反向页必须仍然给得出来，并且确实回到更早的记录。
	earlier := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Before: initial.PrevCursor})
	if len(earlier.Messages) == 0 {
		t.Fatal("reverse page returned nothing")
	}
	if earlier.Messages[0].ID == initialOldest {
		t.Fatalf("reverse page did not go earlier than %q", initialOldest)
	}
	// 走到文件开头时必须停止给出 previous_cursor，客户端据此收起「加载更早」。
	for round := 0; earlier.PrevCursor != ""; round++ {
		if round > 50 {
			t.Fatal("reverse paging did not terminate")
		}
		earlier = readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Before: earlier.PrevCursor})
		if len(earlier.Messages) == 0 {
			t.Fatalf("reverse page empty while previous cursor was set: %+v", earlier)
		}
	}
	if earlier.Messages[0].ID != "u-0" {
		t.Fatalf("reverse paging stopped at %q instead of the oldest record", earlier.Messages[0].ID)
	}
}

// 游标落在行中间（外部改动）时必须回退到前一个换行符之后，重复投递由 upsert 吸收。
func TestTranscriptCursorMidLineBacksOffToLineStart(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	path := claudeSessionPath(home, cwd, "sess-1")
	second := claudeUser("u-2", cwd, "第二行")
	writeLines(t, path, []string{claudeUser("u-1", cwd, "第一行"), second})

	scope := claudeScope(cwd)
	candidates := mustCandidates(t, readTranscript(context.Background(), files, codec, scope, TranscriptRequest{}), 1)
	session := candidates.Candidates[0].ID

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := fileIdentity(info)
	if identity == "" {
		t.Skip("platform has no file identity")
	}
	head, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mid := int64(len(head) - len(second)/2)
	// 游标绑定的是「会话 id + 相对路径 + 文件身份 + 偏移」，不是会话 id 本身那个令牌。
	token, err := codec.seal("sess-1", agentlog.ClaudeProjectDir(cwd)+"/sess-1.jsonl", identity, strconv.FormatInt(mid, 10))
	if err != nil {
		t.Fatal(err)
	}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: session, Cursor: token})
	if page.Reset {
		t.Fatalf("mid-line cursor caused a reset instead of a back-off: %+v", page)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != "u-2" {
		t.Fatalf("back-off produced %+v", page.Messages)
	}
}

func TestTranscriptCodexUsesResponseItemCanonicalPath(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/codex"
	path := codexSessionPath(home, "2026/09/13", "rollout-2026-09-13T04-25-03-sess-1")
	writeLines(t, path, []string{
		codexMeta("sess-1", cwd),
		// 同一条用户消息在两种形状里各写一次：只能出一条。
		`{"type":"event_msg","timestamp":"2026-09-13T04:25:03Z","payload":{"type":"user_message","message":"跑一下测试"}}`,
		codexMessage("user", "跑一下测试"),
		`{"type":"event_msg","timestamp":"2026-09-13T04:25:04Z","payload":{"type":"turn_aborted"}}`,
	})

	scope := codexScope(cwd)
	candidates := mustCandidates(t, readTranscript(context.Background(), files, codec, scope, TranscriptRequest{}), 1)
	if candidates.Candidates[0].SessionID != "sess-1" {
		t.Fatalf("codex session id = %q, want the recorded session_meta id", candidates.Candidates[0].SessionID)
	}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: candidates.Candidates[0].ID})
	if len(page.Messages) != 2 {
		t.Fatalf("codex page = %+v", page.Messages)
	}
	if page.Messages[0].Role != agentlog.RoleUser || page.Messages[0].Blocks[0].Text != "跑一下测试" {
		t.Fatalf("codex user record = %+v", page.Messages[0])
	}
	// 生命周期信号在任何情况下都保留。
	if page.Messages[1].Role != agentlog.RoleSystem {
		t.Fatalf("turn_aborted lost: %+v", page.Messages[1])
	}
	if !strings.HasPrefix(page.Messages[0].ID, "sess-1:") {
		t.Fatalf("codex record id = %q, want <session_id>:<offset>", page.Messages[0].ID)
	}

	// 同源的两次相同 prompt 必须留两条：按 offset 区分，不做文本去重。
	appendRaw(t, path, codexMessage("user", "跑一下测试")+"\n")
	again := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: candidates.Candidates[0].ID})
	repeated := 0
	for _, record := range again.Messages {
		if len(record.Blocks) > 0 && record.Role == agentlog.RoleUser && record.Blocks[0].Text == "跑一下测试" {
			repeated++
		}
	}
	if repeated != 2 {
		t.Fatalf("repeated identical prompt was merged: %+v", again.Messages)
	}
}

func TestTranscriptCodexRejectsForeignCWD(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	writeLines(t, codexSessionPath(home, "2026/09/13", "rollout-2026-09-13T04-25-03-sess-9"), []string{
		codexMeta("sess-9", "/srv/other"),
		codexMessage("user", "别人的"),
	})

	page := readTranscript(context.Background(), files, codec, codexScope("/srv/codex"), TranscriptRequest{})
	if !page.Supported || len(page.Candidates) != 0 || page.Reason != ChatReasonNoSessionCandidates {
		t.Fatalf("foreign codex session became a candidate: %+v", page)
	}
}

// 两个 pane 指向不同 cwd 时必须互不串读。
func TestTranscriptDoesNotLeakAcrossPanes(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	writeLines(t, claudeSessionPath(home, "/srv/alpha", "sess-alpha"), []string{claudeUser("u-alpha", "/srv/alpha", "alpha 的问题")})
	writeLines(t, claudeSessionPath(home, "/srv/beta", "sess-beta"), []string{claudeUser("u-beta", "/srv/beta", "beta 的问题")})

	alpha := mustCandidates(t, readTranscript(context.Background(), files, codec, claudeScope("/srv/alpha"), TranscriptRequest{}), 1)
	beta := mustCandidates(t, readTranscript(context.Background(), files, codec, claudeScope("/srv/beta"), TranscriptRequest{}), 1)
	if alpha.Candidates[0].SessionID != "sess-alpha" || beta.Candidates[0].SessionID != "sess-beta" {
		t.Fatalf("candidates crossed panes: %+v / %+v", alpha.Candidates, beta.Candidates)
	}
	alphaPage := readTranscript(context.Background(), files, codec, claudeScope("/srv/alpha"), TranscriptRequest{Session: alpha.Candidates[0].ID})
	if len(alphaPage.Messages) != 1 || alphaPage.Messages[0].Blocks[0].Text != "alpha 的问题" {
		t.Fatalf("alpha page = %+v", alphaPage.Messages)
	}
	// 拿 alpha 的会话 id 到 beta 的 pane 上读：文件里的 cwd 对不上，必须失效重选。
	crossed := readTranscript(context.Background(), files, codec, claudeScope("/srv/beta"), TranscriptRequest{Session: alpha.Candidates[0].ID})
	if crossed.Reason != ChatReasonSessionUnavailable || len(crossed.Messages) != 0 {
		t.Fatalf("cross-pane session was honoured: %+v", crossed)
	}
}

func TestTranscriptSessionTokenIsOpaqueAndUnforgeable(t *testing.T) {
	codec := newTranscriptCodecForTest(t)
	token, err := codec.seal(agentlog.AgentClaude, "sess-1", "-srv-proj/sess-1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(token, "/=+") || strings.Contains(token, "jsonl") || strings.Contains(token, "sess-1") {
		t.Fatalf("token leaks the path: %q", token)
	}
	if _, ok := codec.open(token, 3); !ok {
		t.Fatal("valid token did not open")
	}
	// 换一个密钥（等价于攻击者伪造）必须解不开。
	other := newTranscriptCodecForTest(t)
	if _, ok := other.open(token, 3); ok {
		t.Fatal("token verified under a different key")
	}
	if _, ok := codec.open(token, 4); ok {
		t.Fatal("token accepted with the wrong arity")
	}
	if _, ok := codec.open(token+"A", 3); ok {
		t.Fatal("tampered token accepted")
	}
}

func TestTranscriptRelAllowedShape(t *testing.T) {
	cases := []struct {
		agent   string
		cwd     string
		session string
		rel     string
		want    bool
	}{
		{agent: agentlog.AgentClaude, rel: "-srv-proj/0f3a.jsonl", want: true},
		{agent: agentlog.AgentClaude, rel: "0f3a.jsonl", want: false},
		{agent: agentlog.AgentClaude, rel: "-srv-proj/a/b.jsonl", want: false},
		{agent: agentlog.AgentClaude, rel: "-srv-proj/../secret.jsonl", want: false},
		{agent: agentlog.AgentClaude, rel: "/-srv-proj/0f3a.jsonl", want: false},
		{agent: agentlog.AgentClaude, rel: "-srv-proj/0f3a.txt", want: false},
		{agent: agentlog.AgentClaude, rel: "-srv-proj/.hidden.jsonl", want: false},
		{agent: agentlog.AgentCodex, rel: "2026/09/13/rollout-1.jsonl", want: true},
		{agent: agentlog.AgentCodex, rel: "rollout-1.jsonl", want: true},
		{agent: agentlog.AgentCodex, rel: "2026/09/13/abc.jsonl", want: false},
		{agent: agentlog.AgentCodex, rel: "a/b/c/d/e/f/rollout-1.jsonl", want: false},
		{agent: agentlog.AgentCodex, rel: "2026/09/13/../rollout-1.jsonl", want: false},

		// DSH：三段路径必须逐字等于「cwd 的项目目录 + 会话 id 的规范编码 + 受支持文件名」。
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-app--/sess-1/session.v3.jsonl", want: true},
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-app--/sess-1/session.v3.jsonl.zstd", want: true},
		// 其它代际不是本期支持的产物。
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-app--/sess-1/session.v2.jsonl", want: false},
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-app--/sess-1/session.jsonl", want: false},
		// 项目目录必须就是 cwd 的编码：换一个目录就是越界。
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-other--/sess-1/session.v3.jsonl", want: false},
		// 会话目录段必须是这个 session id 的规范编码（防线之一，归属仍由 header 判定）。
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-app--/sess-2/session.v3.jsonl", want: false},
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-app--/..%2F/session.v3.jsonl", want: false},
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-app--/../session.v3.jsonl", want: false},
		// 层数必须恰好三层。
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-app--/sess-1", want: false},
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-app--/sess-1/sub/session.v3.jsonl", want: false},
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-app--//session.v3.jsonl", want: false},
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "--srv-app--/sess-1/other.jsonl", want: false},
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "sess-1", rel: "/--srv-app--/sess-1/session.v3.jsonl", want: false},
		// 需要转义的会话 id：编码后仍必须能被对拍回去。
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "a/b", rel: "--srv-app--/a~002Fb/session.v3.jsonl.zstd", want: true},
		{agent: agentlog.AgentDSH, cwd: "/srv/app", session: "a/b", rel: "--srv-app--/a/b/session.v3.jsonl.zstd", want: false},
	}
	for _, testCase := range cases {
		if got := transcriptRelAllowed(testCase.agent, testCase.cwd, testCase.session, testCase.rel); got != testCase.want {
			t.Fatalf("transcriptRelAllowed(%q, %q, %q, %q) = %v, want %v", testCase.agent, testCase.cwd, testCase.session, testCase.rel, got, testCase.want)
		}
	}
}
