package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTranscriptCandidateLimitAppliesAfterCWD(t *testing.T) {
	home := t.TempDir()
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	target := codexSessionPath(home, "2026/09/16", "rollout-target")
	writeLines(t, target, []string{codexMeta("target", "/srv/target"), codexMessage("user", "target")})
	old := time.Unix(1, 0)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		path := codexSessionPath(home, "2026/09/16", fmt.Sprintf("rollout-other-%02d", i))
		writeLines(t, path, []string{codexMeta(fmt.Sprintf("other-%d", i), "/srv/other"), codexMessage("user", "other")})
	}
	page := readTranscript(context.Background(), files, codec, codexScope("/srv/target"), TranscriptRequest{})
	if len(page.Candidates) != 1 {
		t.Fatalf("target session excluded by unrelated projects: candidates=%d reason=%s", len(page.Candidates), page.Reason)
	}
}
func TestTranscriptMetadataWindowAdvancesCursor(t *testing.T) {
	home := t.TempDir()
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	cwd := "/srv/app"
	path := claudeSessionPath(home, cwd, "sess-probe")
	writeLines(t, path, []string{claudeUser("u1", cwd, "start")})
	token := mustCandidates(t, readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{}), 1).Candidates[0].ID
	initial := readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{Session: token})
	appendRaw(t, path, strings.Repeat(`{"type":"system","text":"metadata"}`+"\n", 10000)+claudeAssistant("a2", cwd, "visible after metadata")+"\n")
	cursor := initial.NextCursor
	before := nextOffset(t, codec, cursor)
	page := readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{Session: token, Cursor: cursor})
	after := nextOffset(t, codec, page.NextCursor)
	if after <= before {
		t.Fatalf("cursor stalled across complete metadata: before=%d after=%d has_more=%v messages=%d", before, after, page.HasMore, len(page.Messages))
	}
	for tries := 0; tries < 3 && len(page.Messages) == 0; tries++ {
		page = readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{Session: token, Cursor: page.NextCursor})
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != "a2" {
		t.Fatalf("answer after metadata is unreachable: %+v", page)
	}
}
func TestTranscriptCodexCanonicalPathBeyondIdentityWindow(t *testing.T) {
	home := t.TempDir()
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	cwd := "/srv/app"
	path := codexSessionPath(home, "2026/09/16", "rollout-head")
	writeLines(t, path, []string{codexMeta("head", cwd), `{"type":"turn_context","payload":{"extra":"` + strings.Repeat("a", 4200) + `"}}`, codexMessage("user", "once"), `{"type":"event_msg","payload":{"type":"user_message","message":"once"}}`})
	token := mustCandidates(t, readTranscript(context.Background(), files, codec, codexScope(cwd), TranscriptRequest{}), 1).Candidates[0].ID
	page := readTranscript(context.Background(), files, codec, codexScope(cwd), TranscriptRequest{Session: token})
	if len(page.Messages) != 1 {
		t.Fatalf("dual provider records duplicated: messages=%d", len(page.Messages))
	}
}
func TestDSHSmallFramesHaveBoundedAllocations(t *testing.T) {
	frame := zstdFrame(t, dshTestUser(1, "tiny message")+"\n")
	decoder, err := newDSHDecoder(transcriptDSHMaxPlaintextBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 200; i++ {
		if _, err := decoder.decode(frame); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	t.Logf("decoded 200 frames of %d compressed bytes: allocated=%d MiB", len(frame), (after.TotalAlloc-before.TotalAlloc)>>20)
	if after.TotalAlloc-before.TotalAlloc > 128<<20 {
		t.Fatal("tiny page allocates over 128 MiB")
	}
}

func TestTranscriptRemoteReaderRejectsSymlinkedRootParent(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	for _, agent := range []string{"claude", "codex", "dsh"} {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			outside := t.TempDir()
			leaf := "sessions"
			if agent == "claude" {
				leaf = "projects"
			}
			if err := os.MkdirAll(filepath.Join(outside, leaf), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(home, "."+agent)); err != nil {
				t.Fatal(err)
			}
			req, _ := json.Marshal(transcriptRemoteRequest{Op: "list", Agent: agent, Depth: 1})
			got := runRemoteReader(t, home, string(req))
			if got.OK || got.Error != "denied" {
				t.Fatalf("symlinked root parent followed: %+v", got)
			}
		})
	}
}
func TestTranscriptReadersRejectFIFOWithoutBlocking(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/app"
	path := claudeSessionPath(home, cwd, "sess-fifo")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	rel := filepath.ToSlash(filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path)))
	done := make(chan error, 1)
	go func() {
		results, _ := (&localTranscriptFS{home: home}).Read(context.Background(), transcriptRootClaude, []transcriptReadSpec{{Rel: rel, Length: 4096}})
		done <- results[0].Err
	}()
	select {
	case err := <-done:
		if err != errTranscriptDenied {
			t.Fatalf("FIFO error=%v", err)
		}
	case <-time.After(time.Second):
		// Release a regressed blocking reader before failing the test.
		fd, _ := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK, 0)
		if fd >= 0 {
			unix.Close(fd)
		}
		t.Fatal("FIFO read blocked before type validation")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	for _, op := range []string{"list", "read"} {
		req, _ := json.Marshal(transcriptRemoteRequest{Op: op, Agent: "claude", Rel: filepath.Base(filepath.Dir(path)), Depth: 1, Reads: []transcriptRemoteRead{{Rel: rel, Length: 4096}}})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		cmd := exec.CommandContext(ctx, "python3", "-c", transcriptReaderSource)
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = strings.NewReader(string(req))
		output, err := cmd.Output()
		cancel()
		if err != nil {
			t.Fatalf("%s FIFO: %v", op, err)
		}
		var response transcriptRemoteResponse
		if err := json.Unmarshal(output, &response); err != nil {
			t.Fatal(err)
		}
		if !response.OK {
			t.Fatalf("%s failed: %+v", op, response)
		}
		if op == "read" && (len(response.Reads) != 1 || response.Reads[0].Error != "denied") {
			t.Fatalf("FIFO read not denied: %+v", response)
		}
	}
}
func TestTranscriptCodexSwitchToCanonicalPathResetsCursor(t *testing.T) {
	home := t.TempDir()
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	cwd := "/srv/app"
	path := codexSessionPath(home, "2026/09/16", "rollout-switch")
	writeLines(t, path, []string{codexMeta("switch", cwd), `{"type":"event_msg","payload":{"type":"user_message","message":"once"}}`})
	token := mustCandidates(t, readTranscript(context.Background(), files, codec, codexScope(cwd), TranscriptRequest{}), 1).Candidates[0].ID
	initial := readTranscript(context.Background(), files, codec, codexScope(cwd), TranscriptRequest{Session: token})
	if len(initial.Messages) != 1 {
		t.Fatalf("initial=%+v", initial)
	}
	appendRaw(t, path, codexMessage("user", "once")+"\n")
	page := readTranscript(context.Background(), files, codec, codexScope(cwd), TranscriptRequest{Session: token, Cursor: initial.NextCursor})
	if !page.Reset || len(page.Messages) != 1 {
		t.Fatalf("canonical transition did not replace old events: %+v", page)
	}
}

func TestTranscriptCandidateScanStartsWithNewestDate(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/target"
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	for i := 0; i < transcriptMaxListEntries; i++ {
		writeLines(t, codexSessionPath(home, "2025/01/01", fmt.Sprintf("rollout-old-%04d", i)), []string{codexMeta(fmt.Sprintf("old-%d", i), "/srv/old")})
	}
	writeLines(t, codexSessionPath(home, "2026/09/16", "rollout-new"), []string{codexMeta("new", cwd), codexMessage("user", "current")})
	page := readTranscript(context.Background(), files, codec, codexScope(cwd), TranscriptRequest{})
	if len(page.Candidates) != 1 || page.Candidates[0].SessionID != "new" {
		t.Fatalf("current session hidden by old logs: %+v", page)
	}
	absent := readTranscript(context.Background(), files, codec, codexScope("/srv/absent"), TranscriptRequest{})
	if absent.Reason != ChatReasonReadLimitExceeded {
		t.Fatalf("truncated scan must not claim no session: %+v", absent)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	got := runRemoteReader(t, home, `{"op":"list","agent":"codex","depth":4}`)
	found := false
	for _, entry := range got.Entries {
		if strings.HasSuffix(entry.Rel, "rollout-new.jsonl") {
			found = true
		}
	}
	if !got.OK || !found {
		t.Fatalf("remote listing did not prioritize new date: %+v", got)
	}
}
func TestTranscriptMetadataOnlyOlderPageRetainsCursor(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/app"
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	path := claudeSessionPath(home, cwd, "sess-older")
	writeLines(t, path, []string{claudeUser("u1", cwd, "old question")})
	appendRaw(t, path, strings.Repeat(`{"type":"system","text":"metadata"}`+"\n", 10000)+claudeAssistant("a2", cwd, "new answer")+"\n")
	token := mustCandidates(t, readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{}), 1).Candidates[0].ID
	page := readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{Session: token})
	found := false
	for tries := 0; tries < 5; tries++ {
		for _, message := range page.Messages {
			if message.ID == "u1" {
				found = true
			}
		}
		if found {
			break
		}
		if page.PrevCursor == "" {
			t.Fatal("metadata-only page lost access to earlier questions")
		}
		page = readTranscript(context.Background(), files, codec, claudeScope(cwd), TranscriptRequest{Session: token, Before: page.PrevCursor})
	}
	if !found {
		t.Fatal("older question not reachable")
	}
}
