package herdr

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
)

func geminiDoc(hash string, ids ...string) string {
	messages := make([]string, 0, len(ids))
	for _, id := range ids {
		messages = append(messages, `{"id":"`+id+`","type":"user","content":"text-`+id+`"}`)
	}
	return `{"sessionId":"s","projectHash":"` + hash + `","startTime":"2026-10-30T00:00:00Z","messages":[` + strings.Join(messages, ",") + `]}`
}

// 整份文档被 /rewind 截短后又长回原来的条数：游标下标还在范围内，但前一条已不是原来那条，必须 reset。
func TestGeminiDocumentCursorResetsAfterRewindAndRegrowth(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/proj"
	hash := agentlog.GeminiProjectHash(cwd)
	writeGeminiSession(t, home, hash, "session-s.json", geminiDoc(hash, "a", "b", "c"))
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	scope := TranscriptScope{PaneID: "p", Agent: agentlog.AgentGemini, CWD: cwd}
	token, err := codec.seal(agentlog.AgentGemini, "s", hash+"/chats/session-s.json")
	if err != nil {
		t.Fatal(err)
	}
	first := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: token})
	if len(first.Messages) != 3 {
		t.Fatalf("first = %+v", first)
	}
	// 单纯增长：只拿到新记录。
	writeGeminiSession(t, home, hash, "session-s.json", geminiDoc(hash, "a", "b", "c", "d"))
	grown := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: token, Cursor: first.NextCursor})
	if grown.Reset || len(grown.Messages) != 1 || grown.Messages[0].ID != "d" {
		t.Fatalf("grown = %+v", grown)
	}
	// rewind 到 a 之后再长出 x、y、z：条数回到 4，但下标 3 前面已不是 c。
	writeGeminiSession(t, home, hash, "session-s.json", geminiDoc(hash, "a", "x", "y", "z"))
	rewound := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: token, Cursor: first.NextCursor})
	if !rewound.Reset || len(rewound.Messages) != 4 || rewound.Messages[1].ID != "x" {
		t.Fatalf("rewound = reset %v ids %s", rewound.Reset, idsOf(rewound.Messages))
	}
}

// 尾部窗口模式：同一个 inode 原地重写会让游标前的字节变掉，必须 reset；单纯追加不 reset。
func TestGeminiTailCursorChecksTheBytesBeforeIt(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/proj"
	hash := agentlog.GeminiProjectHash(cwd)
	var body bytes.Buffer
	body.WriteString(`{"kind":"main","sessionId":"big","projectHash":"` + hash + `","startTime":"2026-10-30T00:00:00Z"}` + "\n")
	for body.Len() <= transcriptGeminiMaxBytes {
		body.WriteString(`{"type":"gemini","content":"` + strings.Repeat("y", 4000) + `"}` + "\n")
	}
	writeGeminiSession(t, home, hash, "session-big.jsonl", body.String())
	path := filepath.Join(home, ".gemini", "tmp", hash, "chats", "session-big.jsonl")
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	token, err := codec.seal(agentlog.AgentGemini, "big", hash+"/chats/session-big.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	scope := TranscriptScope{PaneID: "p", Agent: agentlog.AgentGemini, CWD: cwd}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: token})
	idle := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: token, Cursor: page.NextCursor})
	if idle.Reset || len(idle.Messages) != 0 || idle.NextCursor == "" {
		t.Fatalf("idle = %+v", idle)
	}
	appendLine := func(text string) {
		t.Helper()
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := file.WriteString(`{"type":"user","content":"` + text + `"}` + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	appendLine("追加")
	grown := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: token, Cursor: idle.NextCursor})
	// 尾部模式的正向页重读整个尾部窗口（客户端按 id 去重），这里只看没有 reset、新记录在末尾。
	if grown.Reset || len(grown.Messages) == 0 || grown.Messages[len(grown.Messages)-1].Blocks[0].Text != "追加" {
		t.Fatalf("grown = reset %v messages %d", grown.Reset, len(grown.Messages))
	}
	// 原地改写游标前的一行（同一 inode、同样长度），再追加一行。
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := bytes.Replace(data, []byte(`"content":"追加"`), []byte(`"content":"改写"`), 1)
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(rewritten, 0); err != nil {
		t.Fatal(err)
	}
	file.Close()
	appendLine("之后")
	moved := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: token, Cursor: grown.NextCursor})
	if !moved.Reset {
		t.Fatalf("in-place rewrite kept the byte cursor: %+v", moved.Messages)
	}
}
