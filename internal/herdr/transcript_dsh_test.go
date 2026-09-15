package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/riba2534/herdrx/internal/agentlog"
)

// 全部 fixture 都是**合成**的：形状抄自本机安装的 `@deepseek-ai/dsh@0.1.5-rc.2` 源码，
// 但不含任何真实用户会话内容。测试只碰 t.TempDir()，绝不读取真实 HOME。

const (
	dshTestSessionID = "01J8ZQ4T7K3M9P2R5V6W7X8Y9Z"
	dshTestCWD       = "/srv/app"
)

// ── 合成产物构造 ──

func dshTestHeader(id, cwd string) string {
	body := fmt.Sprintf(`{"type":"session","version":3,"id":%q,"createdAt":1757742303000,"isSeeded":false,"delegationDepth":0`, id)
	if cwd != "" {
		body += fmt.Sprintf(`,"cwd":%q`, cwd)
	}
	return body + "}"
}

func dshTestSeededHeader(id, cwd string) string {
	return fmt.Sprintf(`{"type":"session","version":3,"id":%q,"createdAt":1757742303000,"isSeeded":true,"delegationDepth":0,"cwd":%q}`, id, cwd)
}

func dshTestUser(seq int, text string) string {
	return fmt.Sprintf(`{"type":"user/message","seq":%d,"time":%d,"data":{"id":"msg-u%d","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":%q}]},"surfaceOp":"append"}`,
		seq, 1757742304000+seq, seq, text)
}

func dshTestAssistant(seq int, text string) string {
	return fmt.Sprintf(`{"type":"assistant/message","seq":%d,"time":%d,"data":{"turn":1,"step":1,"message":{"id":"msg-a%d","role":"assistant","source":{"kind":"model","provider":"deepseek","model":"deepseek-chat"},"content":[{"type":"text","text":%q}]}},"surfaceOp":"append"}`,
		seq, 1757742305000+seq, seq, text)
}

// dshTestExchange 生成一段用户/助手交替的合成对话，seq 从 1 开始稠密递增。
func dshTestExchange(turns int) []string {
	lines := make([]string, 0, turns*2)
	for turn := 0; turn < turns; turn++ {
		lines = append(lines,
			dshTestUser(turn*2+1, fmt.Sprintf("第 %d 个问题", turn+1)),
			dshTestAssistant(turn*2+2, fmt.Sprintf("第 %d 个回答", turn+1)),
		)
	}
	return lines
}

// zstdFrame 压出一个**独立带校验和**的 zstd 帧，与上游 `compressZstdFrame` 同形。
func zstdFrame(t *testing.T, plaintext string) []byte {
	t.Helper()
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true), zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	return encoder.EncodeAll([]byte(plaintext), nil)
}

// dshWriteZstd 写一份 zstd 产物：header 独占首帧，之后每个批次各占一帧。
func dshWriteZstd(t *testing.T, path, header string, batches ...[]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := zstdFrame(t, header+"\n")
	for _, batch := range batches {
		content = append(content, zstdFrame(t, strings.Join(batch, "\n")+"\n")...)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// dshAppendZstd 追加一个批次帧，模拟 Agent 继续写同一个会话。
func dshAppendZstd(t *testing.T, path string, batch []string) []byte {
	t.Helper()
	content := zstdFrame(t, strings.Join(batch, "\n")+"\n")
	appendRaw(t, path, string(content))
	return content
}

func dshTestSessionPath(home, cwd, session, name string) string {
	project, ok := dshProjectDir(cwd)
	if !ok {
		panic("test cwd is not encodable")
	}
	segment, ok := dshEncodeSegment(session)
	if !ok {
		panic("test session id is not encodable")
	}
	return filepath.Join(home, ".dsh", "sessions", project, segment, name)
}

func dshScope(cwd string) TranscriptScope {
	return TranscriptScope{PaneID: "pane-1", Agent: agentlog.AgentDSH, CWD: cwd}
}

// dshBinding 走完整的「列候选 → 选一个」流程，返回会话 token。
func dshBinding(t *testing.T, files transcriptFS, codec transcriptCodec, cwd string, want int) (string, TranscriptPage) {
	t.Helper()
	page := readTranscript(context.Background(), files, codec, dshScope(cwd), TranscriptRequest{})
	if !page.Supported {
		t.Fatalf("candidates page not supported: %+v", page)
	}
	if len(page.Candidates) != want {
		t.Fatalf("candidates = %+v, want %d (reason %q)", page.Candidates, want, page.Reason)
	}
	if len(page.Messages) != 0 {
		t.Fatalf("candidate page leaked messages: %+v", page.Messages)
	}
	return page.Candidates[0].ID, page
}

func dshLocalFS(home string) transcriptFS {
	return &localTranscriptFS{home: filepath.Clean(home)}
}

// ── 目录名编码 ──

// 这些值是**执行**上游实现（dsh-session-persistence-jsonl 的 projectKey / encodeSegment）
// 得到的，不是照着代码手推的。
func TestDSHProjectDirMatchesUpstreamReference(t *testing.T) {
	cases := []struct {
		cwd  string
		want string
	}{
		{"/srv/app", "--srv-app--"},
		{"/home/opc", "--home-opc--"},
		{"/a-b", "--a-b--"},
		{"/a/b", "--a-b--"},
		{"/a//b", "--a-b--"},
		{"/", "--root--"},
		{"/tmp", "--tmp--"},
		{"/srv/app/", "--srv-app---"},
		{"/home/opc/orca-workspaces/astergate", "--home-opc-orca-workspaces-astergate--"},
		{"/home/用户/x", "--home-~7528~6237-x--"},
		{`C:\Users\x`, "--C-Users-x--"},
	}
	for _, testCase := range cases {
		got, ok := dshProjectDir(testCase.cwd)
		if !ok || got != testCase.want {
			t.Fatalf("dshProjectDir(%q) = %q (%v), want %q", testCase.cwd, got, ok, testCase.want)
		}
	}
	if _, ok := dshProjectDir(""); ok {
		t.Fatal("empty cwd must not produce a project directory")
	}
	// 截断也照抄上游：总长约到 --<251>-- 为止。
	long, ok := dshProjectDir("/" + strings.Repeat("x", 400))
	if !ok || len(long) != 2+251+2 {
		t.Fatalf("truncated project dir = %d chars", len(long))
	}
}

func TestDSHEncodeSegmentMatchesUpstreamReference(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"main-session-7b4071aa", "main-session-7b4071aa"},
		{"..", "~002E~002E"},
		{".", "~002E"},
		{"a/b", "a~002Fb"},
		{"a~b", "a~007Eb"},
		{"中文", "~4E2D~6587"},
	}
	for _, testCase := range cases {
		got, ok := dshEncodeSegment(testCase.raw)
		if !ok || got != testCase.want {
			t.Fatalf("dshEncodeSegment(%q) = %q (%v), want %q", testCase.raw, got, ok, testCase.want)
		}
		// 规范回环：解回来必须还是原串。
		decoded, ok := dshDecodeSegment(got)
		if !ok || decoded != testCase.raw {
			t.Fatalf("dshDecodeSegment(%q) = %q (%v), want %q", got, decoded, ok, testCase.raw)
		}
	}
	if _, ok := dshEncodeSegment(""); ok {
		t.Fatal("empty segment encoded")
	}
	// 非规范写法一律拒绝：小写转义、落单 '~'、白名单外的字面量。
	for _, bad := range []string{"", "~002e", "a~", "~002", "a b", "../x"} {
		if _, ok := dshDecodeSegment(bad); ok {
			t.Fatalf("non-canonical segment accepted: %q", bad)
		}
	}
}

// 目录名编码是**有损**的：`/a-b` 与 `/a/b` 同码，所以目录名不能当归属证明。
func TestDSHProjectDirCollisionIsStillResolvedByHeaderCWD(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	dashed := "01J8ZQ4T7K3M9P2R5V6W7X8Y01"
	nested := "01J8ZQ4T7K3M9P2R5V6W7X8Y02"

	// `/a-b` 与 `/a/b` 编码成**同一个**项目目录；两个会话必须靠 header 的 cwd 区分。
	if got, _ := dshProjectDir("/a-b"); got != "--a-b--" {
		t.Fatalf("project dir = %q", got)
	}
	if got, _ := dshProjectDir("/a/b"); got != "--a-b--" {
		t.Fatalf("project dir = %q", got)
	}
	dshWriteZstd(t, dshTestSessionPath(home, "/a-b", dashed, "session.v3.jsonl.zstd"),
		dshTestHeader(dashed, "/a-b"), dshTestExchange(1))
	dshWriteZstd(t, dshTestSessionPath(home, "/a/b", nested, "session.v3.jsonl.zstd"),
		dshTestHeader(nested, "/a/b"), dshTestExchange(1))

	// 两个目录名相同，所以列目录时两边都能看见对方；归属只能由 header 精确判定。
	for cwd, want := range map[string]string{"/a-b": dashed, "/a/b": nested} {
		page := readTranscript(context.Background(), files, codec, dshScope(cwd), TranscriptRequest{})
		if !page.Supported || len(page.Candidates) != 1 {
			t.Fatalf("cwd %q candidates = %+v (reason %q)", cwd, page.Candidates, page.Reason)
		}
		if page.Candidates[0].SessionID != want {
			t.Fatalf("cwd %q bound to session %q, want %q", cwd, page.Candidates[0].SessionID, want)
		}
		read, ok := codec.open(page.Candidates[0].ID, 3)
		if !ok || read[2] != "--a-b--/"+dshEncodeSegmentForTest(t, want)+"/session.v3.jsonl.zstd" {
			t.Fatalf("cwd %q bound to %q", cwd, read)
		}
	}
}

func dshProjectDirForTest(t *testing.T, cwd string) string {
	t.Helper()
	value, ok := dshProjectDir(cwd)
	if !ok {
		t.Fatalf("project dir for %q", cwd)
	}
	return value
}

func dshEncodeSegmentForTest(t *testing.T, session string) string {
	t.Helper()
	value, ok := dshEncodeSegment(session)
	if !ok {
		t.Fatalf("segment for %q", session)
	}
	return value
}

// ── 帧扫描与解压 ──

func TestDSHScanFramesSeparatesIndependentFrames(t *testing.T) {
	first := zstdFrame(t, "{\"a\":1}\n")
	second := zstdFrame(t, "{\"b\":2}\n{\"c\":3}\n")
	blob := append(append([]byte{}, first...), second...)

	frames, torn, err := dshScanFrames(blob, 0, 0)
	if err != nil || torn != -1 {
		t.Fatalf("scan = %v torn=%d err=%v", frames, torn, err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %+v", frames)
	}
	if frames[0].Start != 0 || frames[0].End != int64(len(first)) {
		t.Fatalf("first frame = %+v, want [0,%d)", frames[0], len(first))
	}
	if frames[1].Start != int64(len(first)) || frames[1].End != int64(len(blob)) {
		t.Fatalf("second frame = %+v", frames[1])
	}

	// maxFrames 让「只读首帧 header」不必扫完整份产物。
	limited, _, err := dshScanFrames(blob, 0, 1)
	if err != nil || len(limited) != 1 || limited[0] != frames[0] {
		t.Fatalf("limited scan = %+v err=%v", limited, err)
	}

	// 尾部被截断：只报告完整帧，并给出未完成帧的起点。
	cut := blob[:len(first)+len(second)/2]
	frames, torn, err = dshScanFrames(cut, 0, 0)
	if err != nil || len(frames) != 1 || torn != int64(len(first)) {
		t.Fatalf("torn scan = %+v torn=%d err=%v", frames, torn, err)
	}

	// 帧结构坏了：不猜，直接报错。
	broken := append([]byte{}, blob...)
	broken[0] ^= 0xFF
	if _, _, err := dshScanFrames(broken, 0, 0); err == nil {
		t.Fatal("invalid frame magic accepted")
	}
}

// ── 候选 ──

func TestTranscriptDSHListsCandidatesFromHeaderCWD(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)

	// 同一 cwd 的两个候选，加上一个别的 cwd 的会话。
	dshWriteZstd(t, dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd"),
		dshTestHeader(dshTestSessionID, dshTestCWD), dshTestExchange(1))
	other := "01J8ZQ4T7K3M9P2R5V6W7X8Y00"
	dshWriteZstd(t, dshTestSessionPath(home, dshTestCWD, other, "session.v3.jsonl.zstd"),
		dshTestHeader(other, dshTestCWD), dshTestExchange(1))
	foreign := "01J8ZQ4T7K3M9P2R5V6W7X8Y11"
	dshWriteZstd(t, dshTestSessionPath(home, "/srv/other", foreign, "session.v3.jsonl.zstd"),
		dshTestHeader(foreign, "/srv/other"), dshTestExchange(1))

	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{})
	if !page.Supported || page.Agent != agentlog.AgentDSH {
		t.Fatalf("page = %+v", page)
	}
	if len(page.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want 2", page.Candidates)
	}
	for _, candidate := range page.Candidates {
		if candidate.Agent != agentlog.AgentDSH || candidate.SessionID == "" {
			t.Fatalf("candidate = %+v", candidate)
		}
	}
}

// 会话目录里只有别的代际时，必须明确说「读不了」，不能含糊成「这里没有会话」。
func TestTranscriptDSHForeignGenerationIsExplicit(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	writeLines(t, dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v2.jsonl"),
		[]string{dshTestHeader(dshTestSessionID, dshTestCWD)})

	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{})
	if !page.Supported {
		t.Fatalf("page = %+v", page)
	}
	if len(page.Candidates) != 0 || page.Reason != ChatReasonUnrecognizedFormat {
		t.Fatalf("reason = %q candidates = %+v, want unrecognized_format", page.Reason, page.Candidates)
	}

	// 日志根不存在是能力缺失，与「这个 cwd 没有会话」是两回事。
	empty := t.TempDir()
	page = readTranscript(context.Background(), dshLocalFS(empty), codec, dshScope(dshTestCWD), TranscriptRequest{})
	if page.Supported || page.Reason != ChatReasonLogRootUnavailable {
		t.Fatalf("missing root = %+v", page)
	}

	// 根在、但这个 cwd 没有任何会话目录，才是 no_session_candidates。
	rootOnly := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootOnly, ".dsh", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	page = readTranscript(context.Background(), dshLocalFS(rootOnly), codec, dshScope(dshTestCWD), TranscriptRequest{})
	if !page.Supported || page.Reason != ChatReasonNoSessionCandidates {
		t.Fatalf("empty project reason = %q", page.Reason)
	}
}

// ── 分页 ──

func TestTranscriptDSHInitialPageIsTailAndTheCursorIsAppendStable(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	path := dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd")

	firstBatch := dshTestExchange(2)
	dshWriteZstd(t, path, dshTestHeader(dshTestSessionID, dshTestCWD), firstBatch)
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)

	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token})
	if !page.Supported || page.SessionID != dshTestSessionID {
		t.Fatalf("page = %+v", page)
	}
	if page.Reason != "" || page.Binding != ChatBindingSelected {
		t.Fatalf("reason=%q binding=%q", page.Reason, page.Binding)
	}
	if len(page.Messages) != len(firstBatch) {
		t.Fatalf("messages = %d, want %d", len(page.Messages), len(firstBatch))
	}
	if page.Messages[0].Role != agentlog.RoleUser || page.Messages[0].ID != dshTestSessionID+":1" {
		t.Fatalf("first message = %+v", page.Messages[0])
	}
	if page.NextCursor == "" {
		t.Fatal("no next cursor")
	}
	// 只有一批：文件开头就是这一批，所以没有更早的内容。
	if page.HasMore || page.PrevCursor != "" {
		t.Fatalf("has_more=%v prev=%q for a single batch", page.HasMore, page.PrevCursor)
	}

	// 追加一批：游标必须仍然落在帧边界上，并且只返回新增的记录。
	batch := []string{dshTestUser(5, "第二个问题"), dshTestAssistant(6, "第二个回答")}
	dshAppendZstd(t, path, batch)

	incremental := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token, Cursor: page.NextCursor})
	if incremental.Reset || len(incremental.Messages) != len(batch) {
		t.Fatalf("incremental = %+v", incremental.Messages)
	}
	if incremental.Messages[0].ID != dshTestSessionID+":5" {
		t.Fatalf("incremental first id = %q", incremental.Messages[0].ID)
	}
	if incremental.SessionID != dshTestSessionID || incremental.Agent != agentlog.AgentDSH {
		t.Fatalf("incremental page = %+v", incremental)
	}
}

func TestTranscriptDSHPagesBackToTheStart(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	path := dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd")

	// 批次要够多，才能让首屏只覆盖尾部。
	batches := make([][]string, 0, 8)
	for turn := 0; turn < 8; turn++ {
		batches = append(batches, []string{
			dshTestUser(turn*2+1, fmt.Sprintf("第 %d 个问题", turn+1)),
			dshTestAssistant(turn*2+2, fmt.Sprintf("第 %d 个回答", turn+1)),
		})
	}
	dshWriteZstd(t, path, dshTestHeader(dshTestSessionID, dshTestCWD), batches...)
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token, Before: cursor})
		if !page.Supported || page.Reset {
			t.Fatalf("page = %+v", page)
		}
		pages++
		for _, message := range page.Messages {
			if seen[message.ID] {
				t.Fatalf("record %q delivered twice across reverse pages", message.ID)
			}
			seen[message.ID] = true
		}
		if page.PrevCursor == "" {
			break
		}
		cursor = page.PrevCursor
		if pages > 16 {
			t.Fatal("reverse pagination did not terminate")
		}
	}
	if len(seen) != 16 {
		t.Fatalf("reverse pagination delivered %d records, want 16", len(seen))
	}
}

func TestTranscriptDSHTruncateResetsInsteadOfGuessing(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	path := dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd")

	dshWriteZstd(t, path, dshTestHeader(dshTestSessionID, dshTestCWD), dshTestExchange(1), dshTestExchange(1))
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)
	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token})
	if page.NextCursor == "" {
		t.Fatal("no cursor")
	}

	// 同一个文件被换成只有一批的内容：游标偏移落不到任何帧边界上。
	// 与通用引擎同一条规矩 —— reset 页从尾部重新读一遍并整体重建，不是重新给候选。
	dshWriteZstd(t, path, dshTestHeader(dshTestSessionID, dshTestCWD), dshTestExchange(1))
	after := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token, Cursor: page.NextCursor})
	if !after.Supported || !after.Reset {
		t.Fatalf("page after truncate = %+v", after)
	}
	if after.Binding != ChatBindingSelected || len(after.Messages) != 2 {
		t.Fatalf("reset page did not re-read the tail: %+v", after.Messages)
	}
	if after.Messages[0].ID != dshTestSessionID+":1" {
		t.Fatalf("reset page first id = %q", after.Messages[0].ID)
	}
}

// 尾部半帧是「Agent 正在写」：不解析、不产出、下次续读整帧重读。
func TestTranscriptDSHPartialLastFrameIsRetried(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	path := dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd")

	dshWriteZstd(t, path, dshTestHeader(dshTestSessionID, dshTestCWD), dshTestExchange(1))
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)

	// 追加半帧。
	pending := zstdFrame(t, strings.Join([]string{dshTestUser(3, "第三个问题"), dshTestAssistant(4, "第三个回答")}, "\n")+"\n")
	appendRaw(t, path, string(pending[:len(pending)/2]))

	partial := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token})
	if !partial.Supported || len(partial.Messages) != 2 {
		t.Fatalf("partial tail leaked: %+v", partial.Messages)
	}

	// 补齐剩下的字节：整帧重读，两条记录都出来，且没有重复。
	appendRaw(t, path, string(pending[len(pending)/2:]))
	complete := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token, Cursor: partial.NextCursor})
	if complete.Reset || len(complete.Messages) != 2 {
		t.Fatalf("completed frame = %+v reset=%v", complete.Messages, complete.Reset)
	}
	if complete.Messages[0].ID != dshTestSessionID+":3" {
		t.Fatalf("first id = %q", complete.Messages[0].ID)
	}
}

// 只有 header 帧、一个事件都没有的会话：不能 panic，也不能给出永远点不完的「加载更早」。
func TestTranscriptDSHHeaderOnlySessionIsEmpty(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	path := dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd")
	dshWriteZstd(t, path, dshTestHeader(dshTestSessionID, dshTestCWD))
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)

	// header 行本身不是一条对话记录，也不能被判成格式错误。
	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token})
	if !page.Supported || page.Reason != "" {
		t.Fatalf("header-only page = %+v", page)
	}
	if len(page.Messages) != 0 || page.HasMore || page.PrevCursor != "" {
		t.Fatalf("header-only page = %+v has_more=%v prev=%q", page.Messages, page.HasMore, page.PrevCursor)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := nextOffset(t, codec, page.NextCursor); got != info.Size() {
		t.Fatalf("next cursor = %d, want file size %d", got, info.Size())
	}
}

// 整帧都是元数据（不产出任何记录）时，游标必须跨过整帧而不是停在上一帧，
// 否则下一页会把同一帧反复解一遍。
func TestTranscriptDSHMetadataOnlyFrameAdvancesCursor(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	path := dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd")

	// 尾部一帧只有 step/start，不含任何记录。
	dshWriteZstd(t, path, dshTestHeader(dshTestSessionID, dshTestCWD),
		dshTestExchange(1),
		[]string{`{"type":"step/start","seq":3,"time":1757742309000,"data":{"turn":1,"step":1}}`},
	)
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)
	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token})
	if !page.Supported || page.Reason != "" || len(page.Messages) != 2 {
		t.Fatalf("page = %+v reason=%q", page.Messages, page.Reason)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// 游标必须落在文件末尾（越过那一帧元数据），而不是停在最后一个**记录**之后。
	if got := nextOffset(t, codec, page.NextCursor); got != info.Size() {
		t.Fatalf("next cursor = %d, want file size %d", got, info.Size())
	}
	if page.HasMore || page.PrevCursor != "" {
		t.Fatalf("has_more=%v prev=%q", page.HasMore, page.PrevCursor)
	}

	// 用这个游标增量读：没有新帧，既不能重复投递也不能判成 reset。
	incremental := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token, Cursor: page.NextCursor})
	if incremental.Reset || len(incremental.Messages) != 0 {
		t.Fatalf("incremental = %+v", incremental.Messages)
	}
}

// 事件帧不以换行收尾：帧结构完整、永远不会「写完」，所以是格式错误，
// 绝不能放行成「一行都不产出但游标照推」。
func TestTranscriptDSHUnterminatedBatchFrameIsRejected(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	path := dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd")

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := zstdFrame(t, dshTestHeader(dshTestSessionID, dshTestCWD)+"\n")
	content = append(content, zstdFrame(t, dshTestUser(1, "没有换行结尾"))...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)
	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token})
	if page.Reason != ChatReasonUnrecognizedFormat || len(page.Messages) != 0 {
		t.Fatalf("unterminated batch reason=%q messages=%d", page.Reason, len(page.Messages))
	}
}

// 一帧明文远超旧实现里那个 4 KiB 的 dst cap：正常批次帧必须能解出来，
// 否则每份真实会话都会变成 unrecognized_format。
func TestTranscriptDSHLargeBatchFrameDecodes(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	path := dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd")

	// 一个批次里塞进足够多的长回答，明文远超 4 KiB。
	batch := make([]string, 0, 20)
	for index := 0; index < 20; index++ {
		batch = append(batch, dshTestAssistant(index+1, strings.Repeat("很长的回答内容。", 400)))
	}
	dshWriteZstd(t, path, dshTestHeader(dshTestSessionID, dshTestCWD), batch)
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)

	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token})
	if !page.Supported || page.Reason != "" {
		t.Fatalf("large batch page = %+v", page)
	}
	if len(page.Messages) != 20 {
		t.Fatalf("messages = %d, want 20", len(page.Messages))
	}
}

// 解码器的两道闸门：总预算与单帧上限。超预算必须是「读得太多」而不是「格式坏了」。
func TestDSHDecoderBudgetBoundaries(t *testing.T) {
	plaintext := strings.Repeat("abcdefghij", 4000) // 40 000 字节
	frame := zstdFrame(t, plaintext)

	// 预算充足：整帧解出来，剩余预算相应扣减。
	roomy, err := newDSHDecoder(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer roomy.Close()
	decoded, err := roomy.decode(frame)
	if err != nil || string(decoded) != plaintext {
		t.Fatalf("decode = %d bytes, err=%v", len(decoded), err)
	}
	if roomy.remaining != (1<<20)-len(plaintext) {
		t.Fatalf("remaining = %d", roomy.remaining)
	}

	// 预算比明文小：明确的超限，不是格式错误。
	tight, err := newDSHDecoder(4096)
	if err != nil {
		t.Fatal(err)
	}
	defer tight.Close()
	if _, err := tight.decode(frame); !errors.Is(err, errDSHDecodeLimit) {
		t.Fatalf("over-budget decode err = %v, want errDSHDecodeLimit", err)
	}

	// 帧头没有声明内容大小时同样要由 dst cap 兜住。
	streamed := zstdStreamFrame(t, plaintext)
	small, err := newDSHDecoder(4096)
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	if _, err := small.decode(streamed); !errors.Is(err, errDSHDecodeLimit) {
		t.Fatalf("undeclared-size over-budget err = %v, want errDSHDecodeLimit", err)
	}

	// 校验和坏了是「帧坏了」，不能伪装成超限。
	broken := append([]byte{}, frame...)
	broken[len(broken)-1] ^= 0xFF
	corrupt, err := newDSHDecoder(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer corrupt.Close()
	if _, err := corrupt.decode(broken); err == nil || errors.Is(err, errDSHDecodeLimit) {
		t.Fatalf("corrupt frame err = %v", err)
	}
}

// zstdStreamFrame 走流式编码器，得到帧头**不声明**内容大小的帧。
func zstdStreamFrame(t *testing.T, plaintext string) []byte {
	t.Helper()
	var buffer strings.Builder
	encoder, err := zstd.NewWriter(&buffer,
		zstd.WithEncoderCRC(true),
		zstd.WithEncoderConcurrency(1),
		zstd.WithSingleSegment(false),
		zstd.WithWindowSize(1<<16),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encoder.Write([]byte(plaintext)); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	return []byte(buffer.String())
}

// 明文代际没有帧语义，走的是通用字节分页引擎，同样必须能读。
func TestTranscriptDSHPlaintextGenerationReads(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	lines := append([]string{dshTestHeader(dshTestSessionID, dshTestCWD)}, dshTestExchange(2)...)
	writeLines(t, dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl"), lines)
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)

	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token})
	if !page.Supported || page.Reason != "" || len(page.Messages) != 4 {
		t.Fatalf("plaintext page = %+v reason=%q", page.Messages, page.Reason)
	}
	if page.Messages[0].ID != dshTestSessionID+":1" {
		t.Fatalf("first id = %q", page.Messages[0].ID)
	}
	// header 行本身绝不能变成一条对话记录。
	for _, message := range page.Messages {
		if message.Role == agentlog.RoleSystem && strings.Contains(message.Blocks[0].Text, "session") {
			t.Fatalf("header leaked into messages: %+v", message)
		}
	}
}

// ── 降级 ──

// isSeeded 的会话继承了父会话上下文，只有整份文件读完才知道边界，因此必须拒绝。
// 这个拒绝只发生在 header 行上，而分页通常只读尾部窗口 —— 所以必须有单独的检查，
// 否则「从尾部开始读」就会绕过它。
func TestTranscriptDSHSeededHeaderIsRejected(t *testing.T) {
	for _, compressed := range []bool{true, false} {
		name := "plaintext"
		if compressed {
			name = "zstd"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			files := dshLocalFS(home)
			codec := newTranscriptCodecForTest(t)
			header := dshTestSeededHeader(dshTestSessionID, dshTestCWD)
			events := dshTestExchange(6)
			name := "session.v3.jsonl"
			if compressed {
				name = "session.v3.jsonl.zstd"
				dshWriteZstd(t, dshTestSessionPath(home, dshTestCWD, dshTestSessionID, name), header,
					events[:2], events[2:4], events[4:6], events[6:8], events[8:10], events[10:12])
			} else {
				writeLines(t, dshTestSessionPath(home, dshTestCWD, dshTestSessionID, name),
					append([]string{header}, events...))
			}

			// 归属仍然可判定，所以它照样是一个可选候选。
			token, _ := dshBinding(t, files, codec, dshTestCWD, 1)
			page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token})
			if page.Reason != ChatReasonUnrecognizedFormat {
				t.Fatalf("seeded session reason = %q, want unrecognized_format", page.Reason)
			}
			if len(page.Messages) != 0 {
				t.Fatalf("seeded session published %d messages: %+v", len(page.Messages), page.Messages)
			}
		})
	}
}

func TestTranscriptDSHCorruptFrameIsUnrecognized(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	path := dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd")
	dshWriteZstd(t, path, dshTestHeader(dshTestSessionID, dshTestCWD), dshTestExchange(1))
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 翻转校验和字节：帧结构仍然自洽，但校验必然失败。
	raw[len(raw)-1] ^= 0xFF
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token})
	if page.Reason != ChatReasonUnrecognizedFormat || len(page.Messages) != 0 {
		t.Fatalf("corrupt frame page reason=%q messages=%d", page.Reason, len(page.Messages))
	}
}

func TestTranscriptDSHReadLimitExceeded(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	path := dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd")

	// 单个批次帧就超过契约的记录硬上限：切碎这一帧等于丢记录，所以如实报超限。
	oversized := make([]string, 0, 600)
	for index := 0; index < 600; index++ {
		oversized = append(oversized, dshTestUser(index+1, fmt.Sprintf("第 %d 个问题", index+1)))
	}
	dshWriteZstd(t, path, dshTestHeader(dshTestSessionID, dshTestCWD), oversized)
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)
	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token})
	if page.Reason != ChatReasonReadLimitExceeded || len(page.Messages) != 0 {
		t.Fatalf("oversized frame reason=%q messages=%d", page.Reason, len(page.Messages))
	}

	// 整个产物超过压缩预算：不静默截断。
	//
	// 头部仍然是合法产物（候选枚举只读首帧 header），超限发生在整份读取那一步。
	big := t.TempDir()
	bigPath := dshTestSessionPath(big, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd")
	dshWriteZstd(t, bigPath, dshTestHeader(dshTestSessionID, dshTestCWD), dshTestExchange(1))
	appendRaw(t, bigPath, strings.Repeat("\x00", transcriptDSHMaxCompressedBytes))
	bigToken, _ := dshBinding(t, dshLocalFS(big), codec, dshTestCWD, 1)
	bigPage := readTranscript(context.Background(), dshLocalFS(big), codec, dshScope(dshTestCWD), TranscriptRequest{Session: bigToken})
	if bigPage.Reason != ChatReasonReadLimitExceeded || len(bigPage.Messages) != 0 {
		t.Fatalf("oversized artifact reason = %q messages = %d", bigPage.Reason, len(bigPage.Messages))
	}
}

func TestTranscriptDSHRejectsSymlinksAndEscapes(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)

	// 根外有一份内容完全合法的产物。
	secret := filepath.Join(outside, "session.v3.jsonl")
	writeLines(t, secret, []string{dshTestHeader(dshTestSessionID, dshTestCWD), dshTestUser(1, "外部内容")})

	// 会话目录里的产物是指向根外的符号链接。
	linkPath := dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, linkPath); err != nil {
		t.Fatal(err)
	}
	page := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{})
	if !page.Supported || len(page.Candidates) != 0 {
		t.Fatalf("symlinked session became a candidate: %+v", page)
	}

	// 父目录（会话目录本身）是符号链接时同样拒绝。
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(linkPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Dir(linkPath)); err != nil {
		t.Fatal(err)
	}
	page = readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{})
	if !page.Supported || len(page.Candidates) != 0 {
		t.Fatalf("symlinked parent became a candidate: %+v", page)
	}

	// 路径穿越：token 里的相对路径必须落回它自己那一个文件。
	traversal := dshTestSessionID
	rel := "--srv-app--/" + traversal + "/../../../../etc/passwd"
	if transcriptRelAllowed(agentlog.AgentDSH, dshTestCWD, traversal, rel) {
		t.Fatal("path traversal accepted")
	}
}

func TestTranscriptDSHTokenIsBoundToSessionAndSource(t *testing.T) {
	home := t.TempDir()
	files := dshLocalFS(home)
	codec := newTranscriptCodecForTest(t)
	dshWriteZstd(t, dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd"),
		dshTestHeader(dshTestSessionID, dshTestCWD), dshTestExchange(1))
	token, _ := dshBinding(t, files, codec, dshTestCWD, 1)

	// 换一个 pane 的 cwd：token 里的相对路径不再属于这个 pane。
	other := TranscriptScope{PaneID: "pane-2", Agent: agentlog.AgentDSH, CWD: "/srv/elsewhere"}
	page := readTranscript(context.Background(), files, codec, other, TranscriptRequest{Session: token})
	if page.Reason != ChatReasonSessionUnavailable || !page.Reset {
		t.Fatalf("cross-pane token accepted: %+v", page)
	}

	// 换成另一个读取源：token 绑定的是 agent，不能用 DSH 的 token 去读 Codex。
	crossSource := readTranscript(context.Background(), files, codec, codexScope(dshTestCWD), TranscriptRequest{Session: token})
	if crossSource.Supported {
		t.Fatalf("cross-source token accepted: %+v", crossSource)
	}

	// 篡改的 token 一律拒绝。
	tampered := readTranscript(context.Background(), files, codec, dshScope(dshTestCWD), TranscriptRequest{Session: token + "A"})
	if tampered.Reason != ChatReasonSessionUnavailable || !tampered.Reset {
		t.Fatalf("tampered token accepted: %+v", tampered)
	}
}

// ── 远端受限读取器 ──

// 远端读取器必须认得 DSH 的固定根，并且沿用同样的路径校验。
func TestTranscriptDSHRemoteReaderServesFixedRoot(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("受限读取器测试需要 python3")
	}
	home := t.TempDir()
	dshWriteZstd(t, dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl.zstd"),
		dshTestHeader(dshTestSessionID, dshTestCWD), dshTestExchange(1))

	listRequest, err := json.Marshal(transcriptRemoteRequest{Op: "list", Agent: "dsh", Rel: dshProjectDirForTest(t, dshTestCWD), Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	response := runRemoteReader(t, home, string(listRequest))
	if !response.OK {
		t.Fatalf("list failed: %+v", response)
	}
	wanted := dshProjectDirForTest(t, dshTestCWD) + "/" + dshEncodeSegmentForTest(t, dshTestSessionID) + "/session.v3.jsonl.zstd"
	found := false
	for _, entry := range response.Entries {
		if entry.Rel == wanted && !entry.Dir && entry.Size > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("DSH root did not list %q: %+v", wanted, response.Entries)
	}

	// 同一个读取器读得回字节，且带上设备+inode 身份。
	readRequest, err := json.Marshal(transcriptRemoteRequest{Op: "read", Agent: "dsh", Reads: []transcriptRemoteRead{{Rel: wanted, Offset: 0, Length: 4096}}})
	if err != nil {
		t.Fatal(err)
	}
	response = runRemoteReader(t, home, string(readRequest))
	if !response.OK || len(response.Reads) != 1 || !response.Reads[0].Exists || response.Reads[0].Identity == "" {
		t.Fatalf("read failed: %+v", response)
	}

	// 路径穿越在远端同样拒绝。
	escape, err := json.Marshal(transcriptRemoteRequest{Op: "read", Agent: "dsh", Reads: []transcriptRemoteRead{{Rel: "../../../etc/passwd", Offset: 0, Length: 16}}})
	if err != nil {
		t.Fatal(err)
	}
	response = runRemoteReader(t, home, string(escape))
	if !response.OK || len(response.Reads) != 1 || response.Reads[0].Error != "denied" {
		t.Fatalf("remote reader accepted traversal: %+v", response)
	}

	// 根不存在时是 root_unavailable，不是「没有候选」。
	empty := t.TempDir()
	response = runRemoteReader(t, empty, string(listRequest))
	if response.OK || response.Error != "root_unavailable" {
		t.Fatalf("missing root = %+v", response)
	}
}

func runRemoteReader(t *testing.T, home, payload string) transcriptRemoteResponse {
	t.Helper()
	command := exec.Command("bash", "-c", transcriptReaderCommand)
	command.Env = append(os.Environ(), "HOME="+home)
	command.Stdin = strings.NewReader(payload)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("remote reader failed: %v", err)
	}
	response := transcriptRemoteResponse{}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("remote reader returned invalid JSON %q: %v", output, err)
	}
	return response
}
