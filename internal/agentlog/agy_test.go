package agentlog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func pbVarintBytes(value uint64) []byte {
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

func pbKey(field, wire int) []byte {
	return pbVarintBytes(uint64(field<<3 | wire))
}

func pbIntField(field int, value uint64) []byte {
	return append(pbKey(field, 0), pbVarintBytes(value)...)
}

func pbBytesField(field int, raw []byte) []byte {
	out := append(pbKey(field, 2), pbVarintBytes(uint64(len(raw)))...)
	return append(out, raw...)
}

func pbStringField(field int, text string) []byte {
	return pbBytesField(field, []byte(text))
}

func agyTimestampFields(seconds int64) []byte {
	created := append(pbIntField(1, uint64(seconds)), pbIntField(2, 0)...)
	return pbBytesField(5, pbBytesField(1, created))
}

func TestDecodeAgyStepsUsesDescriptorRoles(t *testing.T) {
	const (
		userText   = "用户问了一句"
		configText = "CONFIG_SECRET_DO_NOT_SHOW"
		thought    = "THOUGHT_SECRET_DO_NOT_SHOW"
		plannerAlt = "PLANNER_ALT_SECRET"
		detail     = "DETAIL_SECRET_DO_NOT_SHOW"
		unknown    = "UNKNOWN_SECRET_DO_NOT_SHOW"
	)
	when := time.Unix(1_700_000_000, 0).UTC().Format(time.RFC3339)
	userBody := append(pbStringField(2, userText), pbBytesField(12, pbStringField(1, configText))...)
	userPayload := append(append(pbIntField(1, 14), agyTimestampFields(1_700_000_000)...), pbBytesField(19, userBody)...)
	user, userView := DecodeAgyStep(3, 14, 3, 0, userPayload)
	if user == nil || user.Role != RoleUser || user.ID != "3" || user.At != when || len(user.Blocks) != 1 || user.Blocks[0].Text != userText {
		t.Fatalf("user = %+v", user)
	}
	if strings.Contains(string(mustJSON(t, user)), configText) {
		t.Fatal("user config leaked into the record")
	}
	if userView.Role != RoleUser || userView.Kinds[0] != BlockText || userView.Lengths[0] != len(userText) {
		t.Fatalf("user view = %+v", userView)
	}

	queryFirst := append(pbStringField(1, userText), pbStringField(2, configText)...)
	preferred, _ := DecodeAgyStep(4, 14, 3, 0, append(pbIntField(1, 14), pbBytesField(19, queryFirst)...))
	if preferred == nil || preferred.Blocks[0].Text != userText || strings.Contains(preferred.Blocks[0].Text, configText) {
		t.Fatalf("query should win over user_response: %+v", preferred)
	}

	tool := append(pbStringField(1, "call_1"), pbStringField(2, "read_file")...)
	tool = append(tool, pbStringField(3, `{"path":"a.go"}`)...)
	planner := append(pbBytesField(6, pbStringField(12, "planner says")), pbStringField(3, thought)...)
	planner = append(planner, pbStringField(1, "descriptor response")...)
	planner = append(planner, pbBytesField(7, tool)...)
	planner = append(planner, pbBytesField(6, pbStringField(12, plannerAlt))...)
	plannerPayload := append(pbIntField(1, 15), pbBytesField(20, planner)...)
	assistant, assistantView := DecodeAgyStep(5, 15, 3, 0, plannerPayload)
	if assistant == nil || assistant.Role != RoleAssistant || len(assistant.Blocks) != 2 {
		t.Fatalf("assistant = %+v", assistant)
	}
	if assistant.Blocks[0].Type != BlockText || assistant.Blocks[0].Text != "descriptor response" {
		t.Fatalf("response text = %+v", assistant.Blocks[0])
	}
	call := assistant.Blocks[1]
	if call.Type != BlockToolCall || call.CallID != "call_1" || call.Name != "read_file" || string(call.Input) != `{"path":"a.go"}` {
		t.Fatalf("tool call = %+v", call)
	}
	encoded := string(mustJSON(t, assistant))
	if strings.Contains(encoded, thought) || strings.Contains(encoded, plannerAlt) {
		t.Fatal("thinking or alternate planner text leaked")
	}
	if len(assistantView.Kinds) != 2 || assistantView.Kinds[1] != BlockToolCall {
		t.Fatalf("assistant view = %+v", assistantView)
	}

	nestedOnly := pbBytesField(20, pbBytesField(6, pbStringField(12, "nested planner")))
	nested, _ := DecodeAgyStep(6, 15, 3, 0, append(pbIntField(1, 15), nestedOnly...))
	if nested == nil || nested.Blocks[0].Text != "nested planner" {
		t.Fatalf("nested planner text = %+v", nested)
	}

	metaTool := pbBytesField(4, append(append(pbStringField(1, "call_1"), pbStringField(2, "read_file")...), pbStringField(3, `{}`)...))
	result := pbBytesField(140, pbBytesField(2, pbStringField(1, "tool output")))
	toolPayload := append(pbBytesField(5, metaTool), result...)
	toolRecord, _ := DecodeAgyStep(7, 132, 7, 0, toolPayload)
	if toolRecord == nil || toolRecord.Role != RoleTool || len(toolRecord.Blocks) != 1 {
		t.Fatalf("tool = %+v", toolRecord)
	}
	got := toolRecord.Blocks[0]
	if got.Type != BlockToolResult || got.CallID != "call_1" || got.Output != "tool output" || !got.IsError {
		t.Fatalf("tool result = %+v", got)
	}

	system, _ := DecodeAgyStep(8, 101, 3, 0, pbBytesField(114, pbStringField(1, "system notice")))
	if system == nil || system.Role != RoleSystem || system.Blocks[0].Text != "system notice" {
		t.Fatalf("system = %+v", system)
	}
	errorBody := pbBytesField(3, append(pbStringField(2, "short error"), pbStringField(6, detail)...))
	errorRecord, _ := DecodeAgyStep(9, 17, 3, 0, pbBytesField(24, errorBody))
	if errorRecord == nil || errorRecord.Role != RoleSystem || errorRecord.Blocks[0].Text != "short error" {
		t.Fatalf("error = %+v", errorRecord)
	}
	if strings.Contains(string(mustJSON(t, errorRecord)), detail) {
		t.Fatal("error detail leaked")
	}
	checkpoint, _ := DecodeAgyStep(10, 23, 3, 0, pbBytesField(30, pbStringField(5, "checkpoint body")))
	if checkpoint == nil || checkpoint.Role != RoleSystem || checkpoint.Blocks[0].Text != "checkpoint body" {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}

	skipped, skippedView := DecodeAgyStep(11, 99, 3, 0, pbStringField(2, unknown))
	if skipped != nil || skippedView.Role != "" {
		t.Fatalf("unknown step emitted %+v %+v", skipped, skippedView)
	}
	formatted, _ := DecodeAgyStep(12, 14, 3, 1, userPayload)
	if formatted != nil {
		t.Fatal("non-zero step_format was decoded")
	}
	line := FormatAgyDebugLine(userView) + "\n" + FormatAgyDebugLine(skippedView)
	for _, secret := range []string{userText, configText, thought, unknown} {
		if strings.Contains(line, secret) {
			t.Fatalf("debug line contains text %q: %s", secret, line)
		}
	}
	if !strings.Contains(line, "idx=3 step_type=14 role=user blocks=text text_lengths=") || !strings.Contains(line, "idx=11 step_type=99 role=- blocks=- text_lengths=-") {
		t.Fatalf("debug line = %s", line)
	}
}

func TestWriteAgyDebugReadsSQLiteWithoutText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "11111111-2222-4333-8444-555555555555.db")
	secret := "DEBUG_SECRET_DO_NOT_PRINT"
	payload := append(pbIntField(1, 14), pbBytesField(19, pbStringField(2, secret))...)
	writeAgyDB(t, path, []agyDBStep{{Idx: 1, StepType: 14, Status: 3, Payload: payload}})
	var output bytes.Buffer
	if err := WriteAgyDebug(&output, path); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Contains(text, secret) {
		t.Fatalf("debug printed text: %s", text)
	}
	if !strings.Contains(text, "idx=1 step_type=14 role=user blocks=text text_lengths=") {
		t.Fatalf("debug = %s", text)
	}
	if !strings.Contains(text, "text_lengths="+itoa(len(secret))) {
		t.Fatalf("debug length = %s", text)
	}
}

func TestQueryAgyDBSeesWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conv.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE steps (idx integer primary key, step_type integer, status integer, step_format integer, step_payload blob)`); err != nil {
		t.Fatal(err)
	}
	payload := append(pbIntField(1, 14), pbBytesField(19, pbStringField(1, "from wal"))...)
	if _, err := db.Exec(`INSERT INTO steps (idx, step_type, status, step_format, step_payload) VALUES (1, 14, 3, 0, ?)`, payload); err != nil {
		t.Fatal(err)
	}
	verified, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, end, err := QueryAgyDB(context.Background(), path, verified, AgyBounds{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !end || len(rows) != 1 {
		t.Fatalf("rows=%d end=%v", len(rows), end)
	}
	record, _ := DecodeAgyStep(rows[0].Idx, rows[0].StepType, rows[0].Status, rows[0].StepFormat, rows[0].Payload)
	if record == nil || record.Blocks[0].Text != "from wal" {
		t.Fatalf("wal record = %+v", record)
	}
}

// 按路径名打开的库不是调用方按 fd 核对过的那个 inode（中间被换掉）时拒绝，不返回任何行。
func TestQueryAgyDBRejectsAnotherInode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conv.db")
	writeAgyDB(t, path, []agyDBStep{{Idx: 1, StepType: 14, Payload: pbBytesField(19, pbStringField(1, "SWAPPED_SECRET"))}})
	other := filepath.Join(dir, "other.db")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	verified, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := QueryAgyDB(context.Background(), path, verified, AgyBounds{Limit: 10})
	if !errors.Is(err, ErrAgyDenied) || len(rows) != 0 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
}

// 核对通过之后、sqlite 按路径名打开之前文件被换掉：打开的不是核对过的 inode，结果丢弃。
func TestQueryAgyDBRejectsASwapBeforeOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conv.db")
	writeAgyDB(t, path, []agyDBStep{{Idx: 1, StepType: 14, Payload: pbBytesField(19, pbStringField(1, "ORIGINAL"))}})
	swap := filepath.Join(dir, "swap.db")
	writeAgyDB(t, swap, []agyDBStep{{Idx: 1, StepType: 14, Payload: pbBytesField(19, pbStringField(1, "SWAPPED_SECRET"))}})
	verified, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	agyBeforeOpen = func(target string) {
		if err := os.Rename(swap, target); err != nil {
			t.Error(err)
		}
	}
	defer func() { agyBeforeOpen = func(string) {} }()
	rows, _, err := QueryAgyDB(context.Background(), path, verified, AgyBounds{Limit: 10})
	if !errors.Is(err, ErrAgyDenied) || len(rows) != 0 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
}

// 核对过的 inode 恰好仍被本进程打开着，也不能替代「路径名指向同一个 inode」的检查。
func TestQueryAgyDBRejectsAPathToAnotherOpenInode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conv.db")
	writeAgyDB(t, path, []agyDBStep{{Idx: 1, StepType: 14, Payload: pbBytesField(19, pbStringField(1, "OTHER"))}})
	held, err := os.Create(filepath.Join(dir, "held"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	verified, _ := held.Stat()
	rows, _, err := QueryAgyDB(context.Background(), path, verified, AgyBounds{Limit: 10})
	if !errors.Is(err, ErrAgyDenied) || len(rows) != 0 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
}

func TestAgyOpenedInodeSeesOnlyOpenFiles(t *testing.T) {
	dir := t.TempDir()
	open, err := os.Create(filepath.Join(dir, "open"))
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	openInfo, _ := open.Stat()
	closed := filepath.Join(dir, "closed")
	if err := os.WriteFile(closed, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	closedInfo, _ := os.Stat(closed)
	if !agyOpenedInode(openInfo) || agyOpenedInode(closedInfo) {
		t.Fatal("fd scan did not tell an open inode from a closed one")
	}
}

type agyDBStep struct {
	Idx      int64
	StepType int
	Status   int
	Payload  []byte
}

func writeAgyDB(t *testing.T, path string, steps []agyDBStep) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE steps (
		idx integer primary key, step_type integer, status integer, has_subtrajectory numeric,
		metadata blob, error_details blob, permissions blob, task_details blob,
		render_info blob, step_payload blob, step_format integer)`); err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if _, err := db.Exec(`INSERT INTO steps (idx, step_type, status, step_format, step_payload) VALUES (?, ?, ?, 0, ?)`, step.Idx, step.StepType, step.Status, step.Payload); err != nil {
			t.Fatal(err)
		}
	}
}

func mustJSON(t *testing.T, record *Record) []byte {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
