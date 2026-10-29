package herdr

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// 同一行里的多条记录不拆到两页；单独一行就超过上限时整行算已读，只返回上限内的部分并计入 skipped。
func TestWholeLinePageKeepsLinesWholeWithinLimits(t *testing.T) {
	entry := func(start int64) agentlog.RangeEntry {
		return agentlog.RangeEntry{Start: start, End: start + 1, Record: &agentlog.Record{}}
	}
	// 行 0 有 1 条，行 10 有 3 条，行 20 有 1 条。
	records := []agentlog.RangeEntry{entry(0), entry(10), entry(10), entry(10), entry(20)}
	sizes := []int{1, 1, 1, 1, 1}
	if first, last, keep := wholeLinePage(records, sizes, 0, 2, false); first != 0 || last != 1 || keep != 1 {
		t.Fatalf("forward cut inside a line = %d %d %d", first, last, keep)
	}
	if first, last, keep := wholeLinePage(records, sizes, 2, 5, true); first != 4 || last != 5 || keep != 5 {
		t.Fatalf("backward cut inside a line = %d %d %d", first, last, keep)
	}
	// 只有一行且超过记录数上限：整行算已读，返回上限内的条目。
	huge := make([]agentlog.RangeEntry, transcriptPageRecords+50)
	hugeSizes := make([]int, len(huge))
	for index := range huge {
		huge[index] = entry(0)
		hugeSizes[index] = 1
	}
	first, last, keep := wholeLinePage(huge, hugeSizes, 0, transcriptPageRecords, false)
	if first != 0 || last != len(huge) || keep != transcriptPageRecords {
		t.Fatalf("oversized line = %d %d %d", first, last, keep)
	}
	// 字节上限同样生效。
	for index := range hugeSizes {
		hugeSizes[index] = transcriptMaxResponseBytes / 4
	}
	if _, _, keep := wholeLinePage(huge, hugeSizes, 0, 3, false); keep != 4 {
		t.Fatalf("byte-limited keep = %d", keep)
	}
}

func TestTraexOversizedLineHonorsThePageLimit(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/proj"
	const sessionID = "12121212-2222-4333-8444-555555555555"
	items := make([]string, 0, transcriptPageRecords+40)
	for index := 0; index < cap(items); index++ {
		items = append(items, `{"type":"message","id":"u`+strconv.Itoa(index)+`","role":"user","content":[{"type":"input_text","text":"q`+strconv.Itoa(index)+`"}]}`)
	}
	writeLines(t, traexSessionPath(home, "2026/10/30", "rollout-2026-10-30T00-00-00-"+sessionID), []string{
		`{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":"` + cwd + `"}}`,
		`{"type":"history_mutation","payload":{"operation":"append","items":[` + strings.Join(items, ",") + `]}}`,
	})
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	scope := TranscriptScope{PaneID: "p", Agent: agentlog.AgentTraex, CWD: cwd,
		Evidence: &AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: sessionID}}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || len(page.Messages) != transcriptPageRecords || page.Truncated != 40 || page.Skipped != 0 {
		t.Fatalf("messages %d truncated %d skipped %d binding %q reason %q", len(page.Messages), page.Truncated, page.Skipped, page.Binding, page.Reason)
	}
	next := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: page.Session, Cursor: page.NextCursor})
	if len(next.Messages) != 0 {
		t.Fatalf("the oversized line was served twice: %d", len(next.Messages))
	}
}
