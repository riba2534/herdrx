package tunnel

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/agentlog"
	"golang.org/x/crypto/ssh"
)

func TestPermanentTranscriptCommandIsWhitelisted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/srv/permanent"
	body := []byte("permanent-line\n")
	path := filepath.Join(home, ".claude", "projects", agentlog.ClaudeProjectDir(cwd), "sess.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, dial := permanentSSHFixture(t, false)
	prepared, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if err := startTranscript(t, prepared, `{"op":"probe","agent":"claude"}`); err == nil {
		t.Fatal("prepared connection ran the transcript reader")
	}

	_, _, dial = permanentSSHFixture(t, true)
	active, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	for _, command := range []string{"python3 -c 'print(1)'", agent.TranscriptCommand + " /tmp/log", "sh -c " + agent.TranscriptCommand} {
		session, err := active.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		err = session.Start(command)
		_ = session.Close()
		if err == nil {
			t.Fatalf("active daemon accepted %q", command)
		}
	}

	output, err := runTranscript(t, active, `{"op":"read","agent":"claude","reads":[{"rel":"`+agentlog.ClaudeProjectDir(cwd)+`/sess.jsonl","offset":0,"length":64}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output, []byte(`"cwd"`)) || !bytes.Contains(output, []byte(`"ok":true`)) {
		t.Fatalf("transcript response = %s", output)
	}
	var response struct {
		Home  string `json:"home"`
		Reads []struct {
			Data string `json:"data"`
			Rel  string `json:"rel"`
		} `json:"reads"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	if response.Home != home {
		t.Fatalf("home spelling = %q", response.Home)
	}
	for _, read := range response.Reads {
		if strings.Contains(read.Data, home) || strings.Contains(read.Rel, home) {
			t.Fatalf("read leaked home: %+v", read)
		}
	}
}

func startTranscript(t *testing.T, client *ssh.Client, payload string) error {
	t.Helper()
	_, err := runTranscript(t, client, payload)
	return err
}

func runTranscript(t *testing.T, client *ssh.Client, payload string) ([]byte, error) {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	session.Stdout = &stdout
	if err := session.Start(agent.TranscriptCommand); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(stdin, payload); err != nil {
		_ = session.Close()
		return nil, err
	}
	_ = stdin.Close()
	err = session.Wait()
	return stdout.Bytes(), err
}
