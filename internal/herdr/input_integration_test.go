package herdr

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Uses a separate Herdr server and raw PTY under t.TempDir. It never writes
// into the user's existing panes. Opt in with HERDRX_TEST_HERDR.
func TestInputWithRealHerdr(t *testing.T) {
	binary := os.Getenv("HERDRX_TEST_HERDR")
	if binary == "" {
		t.Skip("HERDRX_TEST_HERDR is not set")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HERDR_CONFIG_PATH", filepath.Join(dir, "config.toml"))
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("onboarding = false\n[terminal]\ndefault_shell = \"/bin/sh\"\nshell_mode = \"non_login\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	server := exec.CommandContext(ctx, binary, "--session", "input-test", "server")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command(binary, "--session", "input-test", "server", "stop").Run()
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	endpoint, err := NewLocalEndpoint(binary, "input-test")
	if err != nil {
		t.Fatal(err)
	}
	until := func(check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatal("isolated PTY timed out")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	until(func() bool { _, err := endpoint.Snapshot(ctx); return err == nil })
	created, err := endpoint.Call(ctx, "workspace.create", map[string]any{"cwd": dir, "label": "input-test", "focus": false})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		RootPane Pane `json:"root_pane"`
	}
	if err := json.Unmarshal(created, &result); err != nil || result.RootPane.ID == "" {
		t.Fatalf("create fixture: %s, %v", created, err)
	}
	program := "import os,tty\ntty.setraw(0)\nopen('ready','w').close()\nwhile True:\n data=os.read(0,4096)\n with open('received','ab') as f: f.write(data)\n os.write(1,data)\n"
	if err := os.WriteFile(filepath.Join(dir, "echo.py"), []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.Call(ctx, "pane.send_text", map[string]any{"pane_id": result.RootPane.ID, "text": "python3 echo.py\r"}); err != nil {
		t.Fatal(err)
	}
	until(func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil })
	before, err := endpoint.TerminalGeometry(ctx, result.RootPane.ID)
	if err != nil {
		t.Fatal(err)
	}
	q := NewInputQueue(ctx, endpoint, result.RootPane.ID, func(err error) { t.Error(err) })
	parts := []string{"abc", "中文🙂", "\x1b[A", "\x1b[13;2u", "\x03", "\x1b[200~multi\nline\x1b[201~", "\r"}
	for _, part := range parts {
		if err := q.Enqueue([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	expected := strings.Join(parts, "")
	until(func() bool { data, _ := os.ReadFile(filepath.Join(dir, "received")); return string(data) == expected })
	q.Close()
	after, err := endpoint.TerminalGeometry(ctx, result.RootPane.ID)
	if err != nil || before != after {
		t.Fatalf("input changed PTY geometry: %+v -> %+v (%v)", before, after, err)
	}
	snapshot, err := endpoint.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pane := range snapshot.Panes {
		if pane.ID == result.RootPane.ID {
			return
		}
	}
	t.Fatal("detaching input removed the remote pane")
}

// Pins herdr's framing contract for a single `pane.send_input` call: the paste
// markers wrap the text and the Enter is appended inside the same write. The
// composer no longer sends text and Enter together (see the two-leg test below);
// its first leg relies on exactly this framing, and its second leg relies on an
// empty text with `keys: ['Enter']` producing a bare carriage return.
func TestComposerSendInputWithRealHerdr(t *testing.T) {
	binary := os.Getenv("HERDRX_TEST_HERDR")
	if binary == "" {
		t.Skip("HERDRX_TEST_HERDR is not set")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HERDR_CONFIG_PATH", filepath.Join(dir, "config.toml"))
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("onboarding = false\n[terminal]\ndefault_shell = \"/bin/sh\"\nshell_mode = \"non_login\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	session := "c"
	server := exec.CommandContext(ctx, binary, "--session", session, "server")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command(binary, "--session", session, "server", "stop").Run()
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	endpoint, err := NewLocalEndpoint(binary, session)
	if err != nil {
		t.Fatal(err)
	}
	until := func(check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatal("isolated composer PTY timed out")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	until(func() bool { _, err := endpoint.Snapshot(ctx); return err == nil })
	created, err := endpoint.Call(ctx, "workspace.create", map[string]any{"cwd": dir, "label": "composer-input-test", "focus": false})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		RootPane Pane `json:"root_pane"`
	}
	if err := json.Unmarshal(created, &result); err != nil || result.RootPane.ID == "" {
		t.Fatalf("create fixture: %s, %v", created, err)
	}
	program := "import os,tty\ntty.setraw(0)\nos.write(1,b'\\x1b[?2004h')\nopen('ready','w').close()\nwhile True:\n data=os.read(0,4096)\n with open('received','ab') as f: f.write(data)\n"
	if err := os.WriteFile(filepath.Join(dir, "echo.py"), []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.Call(ctx, "pane.send_text", map[string]any{"pane_id": result.RootPane.ID, "text": "python3 echo.py\r"}); err != nil {
		t.Fatal(err)
	}
	until(func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil })
	text := "第一行\n中文 🙂\n第三行"
	if _, err := endpoint.Call(ctx, "pane.send_input", map[string]any{"pane_id": result.RootPane.ID, "text": text, "keys": []string{"Enter"}}); err != nil {
		t.Fatal(err)
	}
	expected := "\x1b[200~" + text + "\x1b[201~\r"
	until(func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "received"))
		return string(data) == expected
	})

}

// Prologue: raw mode, bracketed paste on, then a ready marker. The target reads
// stdin in a tight loop from the moment it writes `ready`.
const chatFixtureHead = `import json, os, tty
tty.setraw(0)
os.write(1, b'\x1b[?2004h')
open('ready', 'w').close()
`

// The reader loop shared by every chat fixture below: it records the raw bytes of
// each read(), keeps a draft, treats bracketed-paste content as text, and treats a
// carriage return outside a paste as one submit. The byte stream decides the
// outcome, so a fixture fails when the text never arrives, when the Enter leg is
// dropped or swallowed by the paste, when the draft submits twice, or when the
// text is mangled.
const chatFixtureTail = `chunks = open('chunks.jsonl', 'a', buffering=1)
submits = open('submissions.jsonl', 'a', buffering=1)
draft = ''
in_paste = False
pending = ''
for data in iter(lambda: os.read(0, 65536), b''):
    chunks.write(json.dumps(data.hex()) + '\n')
    pending += data.decode('utf-8', 'replace')
    while pending:
        if pending.startswith('\x1b[200~'):
            in_paste, pending = True, pending[6:]
            continue
        if pending.startswith('\x1b[201~'):
            in_paste, pending = False, pending[6:]
            continue
        ch, pending = pending[0], pending[1:]
        if ch == '\x1b':
            # Unknown escape: wait for the rest of the sequence before deciding.
            if pending and (pending[0].isalpha() or pending[0] == '\x1b' and len(pending) < 2):
                continue
            if not pending:
                pending = ch
                break
            continue
        if ch == '\r' and not in_paste:
            submits.write(json.dumps({'text': draft}) + '\n')
            draft = ''
        elif ch == '\n' or ch == '\t' or ch >= ' ':
            draft += ch
`

// A chat-like target that is reading stdin the whole time.
const chatFixture = chatFixtureHead + chatFixtureTail

// jsonLines splits an append-only JSONL file. A writer may be mid-append, so
// callers tolerate an unparsable last line instead of failing the test.
func jsonLines(body string) []string {
	lines := []string{}
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// chatChunks returns the raw bytes of every read() the target recorded, in
// order, so a test can assert which bytes shared one PTY read.
func chatChunks(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "chunks.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	chunks := []string{}
	lines := jsonLines(string(data))
	for index, line := range lines {
		var encoded string
		if err := json.Unmarshal([]byte(line), &encoded); err != nil {
			// The target is still appending; a half-written trailing line is not a failure.
			if index == len(lines)-1 {
				break
			}
			t.Fatalf("decode chunk %q: %v", line, err)
		}
		decoded, err := hex.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, string(decoded))
	}
	return chunks
}

func chatChunkIndexes(t *testing.T, dir string, needles ...string) map[string][]int {
	t.Helper()
	found := map[string][]int{}
	for _, needle := range needles {
		for index, chunk := range chatChunks(t, dir) {
			if strings.Contains(chunk, needle) {
				found[needle] = append(found[needle], index)
			}
		}
	}
	return found
}

func chatSubmissions(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "submissions.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	out := []string{}
	lines := jsonLines(string(data))
	for index, line := range lines {
		var record struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			if index == len(lines)-1 {
				break
			}
			t.Fatalf("decode submission %q: %v", line, err)
		}
		out = append(out, record.Text)
	}
	return out
}

// The composer submits in two legs: the whole text first (keys empty, so herdr
// owns the bracketed-paste framing), then one Enter on its own. This asserts on
// the target side that the paste-end and the Enter land in *different* PTY
// reads, that the text leg alone never submits, and that the Enter leg submits
// exactly once with the exact text. Collapsing the two legs back into a single
// `{text, keys:['Enter']}` call makes the paste-end and the Enter share one
// read again, which is the state that swallows the Enter on a chat target.
func TestComposerTwoLegSubmitReachesChatTargetOnceWithRealHerdr(t *testing.T) {
	binary := os.Getenv("HERDRX_TEST_HERDR")
	if binary == "" {
		t.Skip("HERDRX_TEST_HERDR is not set")
	}
	// Keep the Unix socket path below the platform limit despite the long test name.
	dir, err := os.MkdirTemp("", "herdr-twoleg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HERDR_CONFIG_PATH", filepath.Join(dir, "config.toml"))
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("onboarding = false\n[terminal]\ndefault_shell = \"/bin/sh\"\nshell_mode = \"non_login\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Generous headroom: this test spawns a server, a PTY and a fixture, and the
	// whole suite runs under load in CI.
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	// Unique per process so parallel runs in one checkout never share a session.
	session := fmt.Sprintf("twoleg-%d", os.Getpid())
	server := exec.CommandContext(ctx, binary, "--session", session, "server")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command(binary, "--session", session, "server", "stop").Run()
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	endpoint, err := NewLocalEndpoint(binary, session)
	if err != nil {
		t.Fatal(err)
	}
	until := func(what string, check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatalf("isolated chat PTY timed out waiting for %s", what)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	until("the isolated herdr server", func() bool { _, err := endpoint.Snapshot(ctx); return err == nil })
	created, err := endpoint.Call(ctx, "workspace.create", map[string]any{"cwd": dir, "label": "composer-two-leg", "focus": false})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		RootPane Pane `json:"root_pane"`
	}
	if err := json.Unmarshal(created, &result); err != nil || result.RootPane.ID == "" {
		t.Fatalf("create fixture: %s, %v", created, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat.py"), []byte(chatFixture), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.Call(ctx, "pane.send_text", map[string]any{"pane_id": result.RootPane.ID, "text": "python3 chat.py\r"}); err != nil {
		t.Fatal(err)
	}
	until("the chat fixture", func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil })

	text := "第一行\n中文 🙂\n第三行"
	framed := "\x1b[200~" + text + "\x1b[201~"

	// First leg: the text alone. Nothing may be submitted yet, otherwise a later
	// Enter leg would be the second submit.
	if _, err := endpoint.Call(ctx, "pane.send_input", map[string]any{"pane_id": result.RootPane.ID, "text": text, "keys": []string{}}); err != nil {
		t.Fatal(err)
	}
	until("the framed paste", func() bool { return len(chatChunkIndexes(t, dir, "\x1b[201~")["\x1b[201~"]) > 0 })
	if got := chatSubmissions(t, dir); len(got) != 0 {
		t.Fatalf("text leg submitted on its own: %q", got)
	}

	// Second leg: one bare Enter, no text.
	if _, err := endpoint.Call(ctx, "pane.send_input", map[string]any{"pane_id": result.RootPane.ID, "text": "", "keys": []string{"Enter"}}); err != nil {
		t.Fatal(err)
	}
	until("the chat target to submit", func() bool { return len(chatSubmissions(t, dir)) > 0 })
	if got := chatSubmissions(t, dir); len(got) != 1 || got[0] != text {
		t.Fatalf("expected exactly one submit with the exact text, got %q", got)
	}

	// The whole point of the split: the paste-end and the Enter are separate reads.
	found := chatChunkIndexes(t, dir, "\x1b[201~", "\r")
	pasteEnd, enter := found["\x1b[201~"], found["\r"]
	if len(pasteEnd) != 1 || len(enter) != 1 {
		t.Fatalf("expected one paste-end chunk and one Enter chunk, got %v", found)
	}
	if pasteEnd[0] == enter[0] {
		t.Fatalf("paste-end and Enter shared PTY read %d; the Enter is inside the paste", pasteEnd[0])
	}
	if enter[0] < pasteEnd[0] {
		t.Fatalf("Enter read %d arrived before paste-end read %d", enter[0], pasteEnd[0])
	}
	if received := strings.Join(chatChunks(t, dir), ""); !strings.Contains(received, framed) {
		t.Fatalf("target did not receive a framed paste of the exact text: %q", received)
	}

	// A failed Enter leg must not redeliver the text: a wrong pane is rejected and
	// the target keeps exactly one submission.
	if _, err := endpoint.Call(ctx, "pane.send_input", map[string]any{"pane_id": "w9:p9", "text": "", "keys": []string{"Enter"}}); err == nil {
		t.Fatal("expected an unknown pane to be rejected")
	}
	if got := chatSubmissions(t, dir); len(got) != 1 {
		t.Fatalf("failed Enter leg replayed the text: %q", got)
	}
}

// Same parsing contract as chatFixture, but the target does **not** read stdin
// until a `release` file appears. That models a TUI that is mid-render: its
// stdin buffer collects whatever herdr writes, and its next read() returns all
// of it at once. The handshake makes the timing deterministic instead of
// dependent on how loaded the machine is.
const busyChatFixture = `import json, os, time, tty
tty.setraw(0)
os.write(1, b'\x1b[?2004h')
open('ready', 'w').close()
while not os.path.exists('release'):
    time.sleep(0.005)
` + chatFixtureTail

// The two-leg split is not self-guaranteeing: a PTY has no message boundaries,
// so the legs only land in different read() calls if the target actually reads
// the text in between. This pins both halves of that contract against a busy
// target, which is the exact state the client-side settle exists for.
//
//   - merged: both legs written while the target is busy -> the target sees one
//     read() holding paste-end and Enter together, i.e. the byte stream that
//     swallows the Enter. Waiting zero time cannot fix this.
//   - settled: the Enter is only sent after the target has read the paste ->
//     the target sees two reads and submits exactly once. This is the outcome the
//     client settle (`COMPOSER_SUBMIT_SETTLE_MS`) is buying.
//
// The waiting here is driven by the target's own recorded chunks rather than by
// a timer, so the assertion holds regardless of machine load. The client-side
// delay itself is pinned by web/src/lib/composerDrafts.test.ts.
func TestComposerTwoLegSplitNeedsTheTargetToReadFirstWithRealHerdr(t *testing.T) {
	binary := os.Getenv("HERDRX_TEST_HERDR")
	if binary == "" {
		t.Skip("HERDRX_TEST_HERDR is not set")
	}
	dir, err := os.MkdirTemp("", "herdr-twoleg-busy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HERDR_CONFIG_PATH", filepath.Join(dir, "config.toml"))
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("onboarding = false\n[terminal]\ndefault_shell = \"/bin/sh\"\nshell_mode = \"non_login\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	session := fmt.Sprintf("twolegbusy-%d", os.Getpid())
	server := exec.CommandContext(ctx, binary, "--session", session, "server")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command(binary, "--session", session, "server", "stop").Run()
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	endpoint, err := NewLocalEndpoint(binary, session)
	if err != nil {
		t.Fatal(err)
	}
	until := func(what string, check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatalf("isolated busy PTY timed out waiting for %s", what)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	until("the isolated herdr server", func() bool { _, err := endpoint.Snapshot(ctx); return err == nil })

	// Each phase gets its own workspace so the fixtures never share chunk files.
	start := func(name string) (string, string) {
		t.Helper()
		work := filepath.Join(dir, name)
		if err := os.MkdirAll(work, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, "chat.py"), []byte(busyChatFixture), 0600); err != nil {
			t.Fatal(err)
		}
		created, err := endpoint.Call(ctx, "workspace.create", map[string]any{"cwd": work, "label": name, "focus": false})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			RootPane Pane `json:"root_pane"`
		}
		if err := json.Unmarshal(created, &result); err != nil || result.RootPane.ID == "" {
			t.Fatalf("create %s: %s, %v", name, created, err)
		}
		if _, err := endpoint.Call(ctx, "pane.send_text", map[string]any{"pane_id": result.RootPane.ID, "text": "python3 chat.py\r"}); err != nil {
			t.Fatal(err)
		}
		until("the "+name+" fixture", func() bool {
			_, err := os.Stat(filepath.Join(work, "ready"))
			return err == nil
		})
		return work, result.RootPane.ID
	}
	sendLeg := func(pane, text string, keys []string) {
		t.Helper()
		if _, err := endpoint.Call(ctx, "pane.send_input", map[string]any{"pane_id": pane, "text": text, "keys": keys}); err != nil {
			t.Fatal(err)
		}
	}
	release := func(work string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, "release"), []byte("go"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 1 (negative control): no settle at all. Both legs are already in the
	// PTY buffer before the busy target reads anything.
	mergedText := "繁忙目标：第一行\n第二行"
	mergedWork, mergedPane := start("busy-merged")
	sendLeg(mergedPane, mergedText, []string{})
	sendLeg(mergedPane, "", []string{"Enter"})
	release(mergedWork)
	until("the busy target to process the merged input", func() bool { return len(chatSubmissions(t, mergedWork)) > 0 })
	mergedFound := chatChunkIndexes(t, mergedWork, "\x1b[201~", "\r")
	mergedPaste, mergedEnter := mergedFound["\x1b[201~"], mergedFound["\r"]
	if len(mergedPaste) != 1 || len(mergedEnter) != 1 || mergedPaste[0] != mergedEnter[0] {
		t.Fatalf("a busy target did not merge the legs into one read (paste-end %v, Enter %v); the settle rationale is stale", mergedPaste, mergedEnter)
	}
	if got := strings.Join(chatChunks(t, mergedWork), ""); !strings.Contains(got, "\x1b[200~"+mergedText+"\x1b[201~\r") {
		t.Fatalf("merged legs did not reproduce the single-call byte stream: %q", got)
	}

	// Phase 2: the same target, but the Enter waits until the target has read the
	// paste. Both legs still separate, and the target submits exactly once.
	settledText := "繁忙目标：第三行\n第四行"
	settledWork, settledPane := start("busy-settled")
	sendLeg(settledPane, settledText, []string{})
	release(settledWork)
	until("the busy target to read the paste", func() bool {
		return len(chatChunkIndexes(t, settledWork, "\x1b[201~")["\x1b[201~"]) > 0
	})
	if got := chatSubmissions(t, settledWork); len(got) != 0 {
		t.Fatalf("text leg submitted on its own: %q", got)
	}
	sendLeg(settledPane, "", []string{"Enter"})
	until("the settled target to submit", func() bool { return len(chatSubmissions(t, settledWork)) > 0 })
	if got := chatSubmissions(t, settledWork); len(got) != 1 || got[0] != settledText {
		t.Fatalf("expected exactly one submit with the exact text, got %q", got)
	}
	settledFound := chatChunkIndexes(t, settledWork, "\x1b[201~", "\r")
	settledPaste, settledEnter := settledFound["\x1b[201~"], settledFound["\r"]
	if len(settledPaste) != 1 || len(settledEnter) != 1 {
		t.Fatalf("expected one paste-end chunk and one Enter chunk, got %v", settledFound)
	}
	if settledPaste[0] == settledEnter[0] {
		t.Fatalf("paste-end and Enter shared PTY read %d; the Enter is inside the paste", settledPaste[0])
	}
}
