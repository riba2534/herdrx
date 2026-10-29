package herdr

import (
	"context"
	"strconv"
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
)

type countingTranscriptFS struct {
	transcriptFS
	reads int
	specs int
}

func (f *countingTranscriptFS) Read(ctx context.Context, root transcriptRootKind, specs []transcriptReadSpec) ([]transcriptReadResult, error) {
	f.reads++
	f.specs += len(specs)
	return f.transcriptFS.Read(ctx, root, specs)
}

// flakyReadFS 让某个文件第二次及以后的读取失败（例如读到一半被替换）。
type flakyReadFS struct {
	transcriptFS
	rel   string
	calls int
}

func (f *flakyReadFS) Read(ctx context.Context, root transcriptRootKind, specs []transcriptReadSpec) ([]transcriptReadResult, error) {
	results, err := f.transcriptFS.Read(ctx, root, specs)
	for index, spec := range specs {
		if spec.Rel == f.rel && index < len(results) {
			f.calls++
			if f.calls > 1 {
				results[index] = transcriptReadResult{Err: errTranscriptDenied}
			}
		}
	}
	return results, err
}

// 单个 Gemini 会话在回放时读失败只跳过它，其它候选照常列出。
func TestGeminiCandidateReadErrorSkipsOnlyThatFile(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/proj"
	hash := agentlog.GeminiProjectHash(cwd)
	writeGeminiSession(t, home, hash, "session-good.json", `{"sessionId":"good","projectHash":"`+hash+`","messages":[{"type":"user","content":"x"}]}`)
	writeGeminiSession(t, home, hash, "session-flaky.json", `{"sessionId":"flaky","projectHash":"`+hash+`","messages":[{"type":"user","content":"y"}]}`)
	files := &flakyReadFS{transcriptFS: &localTranscriptFS{home: home}, rel: hash + "/chats/session-flaky.json"}
	candidates, _, err := collectCandidates(context.Background(), files, newTranscriptCodecForTest(t), agentlog.AgentGemini, cwd)
	if err != nil || len(candidates) != 1 || candidates[0].SessionID != "good" || files.calls < 2 {
		t.Fatalf("candidates = %+v err = %v calls = %d", candidates, err, files.calls)
	}
}

// 各项目的 `.workspace-trusted` 按 64 个一批读；同一项目下的多份 transcript 不再重读它。
func TestCursorCandidatesBatchWorkspaceReads(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/example/workspace/foo"
	const line = `{"role":"user","message":{"content":[{"type":"text","text":"hi"}]}}`
	for index := 0; index < 70; index++ {
		writeCursorTranscript(t, home, "other-"+strconv.Itoa(index), "sess-o"+strconv.Itoa(index), "/Users/example/other/"+strconv.Itoa(index), line)
	}
	writeCursorTranscript(t, home, "mine", "sess-1", cwd, line)
	writeCursorTranscript(t, home, "mine", "sess-2", cwd, line)
	writeCursorTranscript(t, home, "mine", "sess-3", cwd, line)
	files := &countingTranscriptFS{transcriptFS: &localTranscriptFS{home: home}}
	candidates, _, err := collectCursorCandidates(context.Background(), files, newTranscriptCodecForTest(t), cwd)
	if err != nil || len(candidates) != 3 {
		t.Fatalf("candidates = %+v %v", candidates, err)
	}
	for _, candidate := range candidates {
		if candidate.Agent != agentlog.AgentCursor {
			t.Fatalf("candidate = %+v", candidate)
		}
	}
	if files.reads != 2 || files.specs != 71 {
		t.Fatalf("reads = %d specs = %d, want 2 batched requests for 71 projects", files.reads, files.specs)
	}
}
