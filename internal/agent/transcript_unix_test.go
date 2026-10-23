//go:build unix

package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/agentlog"
	"golang.org/x/sys/unix"
)

func readTranscriptRequest(t *testing.T, home string, payload string) transcriptWireResponse {
	t.Helper()
	response, err := serveTranscript(home, []byte(payload))
	if err != nil {
		t.Fatalf("serve transcript: %v", err)
	}
	return response
}

func TestTranscriptReaderListsAndReadsFixedRoot(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/app"
	session := "sess-1"
	path := filepath.Join(home, ".claude", "projects", agentlog.ClaudeProjectDir(cwd), session+".jsonl")
	body := []byte(`{"type":"user","cwd":"/srv/app","message":{"content":"hello"}}` + "\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	// A Cursor directory must not become a readable root just because it exists.
	cursorPath := filepath.Join(home, ".cursor", "projects", "secret.jsonl")
	if err := os.MkdirAll(filepath.Dir(cursorPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cursorPath, []byte("secret-cursor-log"), 0o600); err != nil {
		t.Fatal(err)
	}

	cursor := readTranscriptRequest(t, home, `{"op":"list","agent":"cursor","depth":4}`)
	if cursor.OK || cursor.Error != "unknown_agent" {
		t.Fatalf("cursor root accepted: %+v", cursor)
	}

	rel := agentlog.ClaudeProjectDir(cwd)
	listed := readTranscriptRequest(t, home, `{"op":"list","agent":"claude","rel":"`+rel+`","depth":1}`)
	if !listed.OK || len(listed.Entries) != 1 {
		t.Fatalf("list = %+v", listed)
	}
	entry := listed.Entries[0]
	if entry.Dir || entry.Link || entry.Rel != rel+"/"+session+".jsonl" || entry.Size != int64(len(body)) {
		t.Fatalf("entry = %+v", entry)
	}
	if strings.Contains(entry.Rel, home) || strings.HasPrefix(entry.Rel, "/") {
		t.Fatalf("entry leaked an absolute path: %+v", entry)
	}

	read := readTranscriptRequest(t, home, `{"op":"read","agent":"claude","reads":[{"rel":"`+entry.Rel+`","offset":0,"length":4096}]}`)
	if !read.OK || len(read.Reads) != 1 || !read.Reads[0].Exists || read.Reads[0].Error != "" || read.Reads[0].Identity == "" {
		t.Fatalf("read = %+v", read)
	}
	decoded, err := base64.StdEncoding.DecodeString(read.Reads[0].Data)
	if err != nil || !bytes.Equal(decoded, body) {
		t.Fatalf("data = %q err=%v", decoded, err)
	}
	assertTranscriptJSONHasNoCWD(t, read)
}

func TestTranscriptReaderEnforcesBoundsAndRejectsEscape(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".codex", "sessions")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("a"), transcriptMaxRead+32)
	if err := os.WriteFile(filepath.Join(root, "rollout-big.jsonl"), big, 0o600); err != nil {
		t.Fatal(err)
	}

	escaped := readTranscriptRequest(t, home, `{"op":"read","agent":"codex","reads":[{"rel":"../../.codex/../etc/passwd","offset":0,"length":16},{"rel":"/etc/passwd","offset":0,"length":16},{"rel":"linked/secret","offset":0,"length":16}]}`)
	if !escaped.OK || len(escaped.Reads) != 3 {
		t.Fatalf("escape = %+v", escaped)
	}
	for _, read := range escaped.Reads {
		if read.Error != "denied" || read.Exists || read.Data != "" {
			t.Fatalf("escape read accepted: %+v", read)
		}
	}

	linked := readTranscriptRequest(t, home, `{"op":"list","agent":"codex","depth":1}`)
	foundLink := false
	for _, entry := range linked.Entries {
		if entry.Rel == "linked" {
			foundLink = entry.Link && !entry.Dir
		}
		if strings.Contains(entry.Rel, "secret") {
			t.Fatalf("listing followed a symlink: %+v", linked.Entries)
		}
	}
	if !linked.OK || !foundLink {
		t.Fatalf("symlink not marked: %+v", linked)
	}

	bounded := readTranscriptRequest(t, home, `{"op":"read","agent":"codex","reads":[{"rel":"rollout-big.jsonl","offset":0,"length":99999999}]}`)
	if !bounded.OK || len(bounded.Reads) != 1 || !bounded.Reads[0].Exists {
		t.Fatalf("bounded read = %+v", bounded)
	}
	decoded, err := base64.StdEncoding.DecodeString(bounded.Reads[0].Data)
	if err != nil || len(decoded) != transcriptMaxRead {
		t.Fatalf("read length = %d err=%v", len(decoded), err)
	}

	extra := make([]string, 0, transcriptMaxReads+1)
	for i := 0; i < transcriptMaxReads+1; i++ {
		extra = append(extra, `{"rel":"missing-`+string(rune('a'+i%26))+`.jsonl","offset":0,"length":1}`)
	}
	capped := readTranscriptRequest(t, home, `{"op":"read","agent":"codex","reads":[`+strings.Join(extra, ",")+`]}`)
	if !capped.OK || len(capped.Reads) != transcriptMaxReads {
		t.Fatalf("read cap = %d", len(capped.Reads))
	}
}

func TestTranscriptReaderCapsDepthAndEntryCount(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".dsh", "sessions", "a", "b", "c", "d", "e", "f")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	listed := readTranscriptRequest(t, home, `{"op":"list","agent":"dsh","depth":9}`)
	if !listed.OK {
		t.Fatalf("list = %+v", listed)
	}
	for _, entry := range listed.Entries {
		if strings.Contains(entry.Rel, "f") {
			t.Fatalf("depth cap walked %q", entry.Rel)
		}
	}
	if !hasRel(listed.Entries, "a/b/c/d/e") {
		t.Fatalf("depth 5 missing: %+v", listed.Entries)
	}

	flat := filepath.Join(home, ".claude", "projects", "flat")
	if err := os.MkdirAll(flat, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < transcriptEntryLimit+1; i++ {
		name := "f-" + strings.Repeat("x", 3) + "-" + itoa(i) + ".jsonl"
		if err := os.WriteFile(filepath.Join(flat, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	capped := readTranscriptRequest(t, home, `{"op":"list","agent":"claude","rel":"flat","depth":1}`)
	if !capped.OK || len(capped.Entries) != transcriptEntryLimit {
		t.Fatalf("entry cap = %d", len(capped.Entries))
	}
}

func TestTranscriptReaderRejectsSymlinkedRootAndMissingRoot(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	denied := readTranscriptRequest(t, home, `{"op":"list","agent":"claude"}`)
	if denied.OK || denied.Error != "denied" {
		t.Fatalf("symlinked root parent = %+v", denied)
	}
	missing := readTranscriptRequest(t, t.TempDir(), `{"op":"list","agent":"claude"}`)
	if missing.OK || missing.Error != "root_unavailable" {
		t.Fatalf("missing root = %+v", missing)
	}
}

func TestTranscriptReaderRejectsFIFOWithoutBlocking(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "sess.jsonl"), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(payload string) transcriptWireResponse {
		t.Helper()
		done := make(chan transcriptWireResponse, 1)
		failed := make(chan error, 1)
		go func() {
			response, err := serveTranscript(home, []byte(payload))
			if err != nil {
				failed <- err
				return
			}
			done <- response
		}()
		select {
		case response := <-done:
			return response
		case err := <-failed:
			t.Fatal(err)
		case <-time.After(time.Second):
			fd, _ := unix.Open(filepath.Join(dir, "sess.jsonl"), unix.O_WRONLY|unix.O_NONBLOCK, 0)
			if fd >= 0 {
				unix.Close(fd)
			}
			t.Fatal("FIFO read blocked")
		}
		return transcriptWireResponse{}
	}
	listed := call(`{"op":"list","agent":"claude","rel":"app","depth":1}`)
	read := call(`{"op":"read","agent":"claude","reads":[{"rel":"app/sess.jsonl","offset":0,"length":8}]}`)
	if !listed.OK {
		t.Fatalf("list failed: %+v", listed)
	}
	for _, entry := range listed.Entries {
		if strings.HasSuffix(entry.Rel, "sess.jsonl") && !entry.Link {
			t.Fatalf("FIFO listed as a regular file: %+v", entry)
		}
	}
	if !read.OK || len(read.Reads) != 1 || read.Reads[0].Error != "denied" || read.Reads[0].Exists {
		t.Fatalf("FIFO read = %+v", read)
	}
}

func TestTranscriptCommandOnPairedSSHServesBoundedRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/work/demo"
	path := filepath.Join(home, ".claude", "projects", agentlog.ClaudeProjectDir(cwd), "sess-ssh.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, server, clientConfig, _, _ := setupTestSSH(t, true, false)
	client, err := dialLocalTCP(t, server, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if err := session.Start(TranscriptCommand); err != nil {
		t.Fatalf("paired transcript command rejected: %v", err)
	}
	payload := `{"op":"read","agent":"claude","reads":[{"rel":"` + agentlog.ClaudeProjectDir(cwd) + `/sess-ssh.jsonl","offset":0,"length":16}]}`
	if _, err := stdin.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := session.Wait(); err != nil {
		t.Fatalf("wait: %v stderr=%s", err, stderr.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte(home)) || bytes.Contains(stdout.Bytes(), []byte(`"cwd"`)) {
		t.Fatalf("response leaked cwd or home: %s", stdout.Bytes())
	}
	var response transcriptWireResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("response %s: %v", stdout.Bytes(), err)
	}
	if !response.OK || len(response.Reads) != 1 || !response.Reads[0].Exists || response.Reads[0].Identity == "" {
		t.Fatalf("ssh response = %+v", response)
	}
}

func assertTranscriptJSONHasNoCWD(t *testing.T, response transcriptWireResponse) {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			if _, ok := typed["cwd"]; ok {
				t.Fatalf("reader response has a cwd field: %s", encoded)
			}
			for key, child := range typed {
				if key == "data" {
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
}

func hasRel(entries []transcriptWireEntry, rel string) bool {
	for _, entry := range entries {
		if entry.Rel == rel && entry.Dir {
			return true
		}
	}
	return false
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [16]byte
	pos := len(digits)
	for value > 0 {
		pos--
		digits[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[pos:])
}
