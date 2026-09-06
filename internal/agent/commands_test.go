package agent

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestParseCommandAllowlist(t *testing.T) {
	allowed := []string{
		`sh -c 'printf "%s\n%s\n" "$HOME" "${XDG_CONFIG_HOME:-}"'`,
		"uname -sm",
		"confirm-pair abc_DEF-12345678",
		"confirm-pair -abcDEF_12345678",
		"confirm-pair _xyzABC-98765432",
		"herdr terminal session observe w1:p1 --cols 80 --rows 24",
		"herdr --session work terminal session control w1:p1 --takeover --cols 120 --rows 40",
		"herdrx-stage-image png",
		"herdrx-stage-image jpg",
		"herdrx-stage-image jpeg",
		"herdrx-stage-image webp",
		"herdrx-stage-image gif",
		"herdrx-terminal-geometry 1234",
	}
	for _, command := range allowed {
		if _, err := parseCommand(command); err != nil {
			t.Errorf("allowed command %q was rejected: %v", command, err)
		}
	}
	rejected := []string{
		"herdr",
		"herdr --session work",
		"herdr server stop",
		"herdr terminal session control w1:p1 --cols 80 --rows 24; id",
		"sh -c id",
		"herdr terminal session control ../../tmp/x --cols 80 --rows 24",
		"herdr terminal session control w1:p1 --cols 9999 --rows 24",
		"herdrx-stage-image",
		"herdrx-stage-image sh",
		"herdrx-stage-image exe",
		"herdrx-stage-image ../test.png",
		"herdrx-stage-image png; id",
		"herdrx-stage-image png extra",
		"confirm-pair short",
		"confirm-pair invalid!char@12345",
		"herdrx-terminal-geometry 0",
		"herdrx-terminal-geometry 1",
		"herdrx-terminal-geometry -2",
		"herdrx-terminal-geometry +123",
		"herdrx-terminal-geometry 123 extra",
		"herdrx-terminal-geometry ../../123",
		"herdrx-terminal-geometry 999999999999999",
	}
	for _, command := range rejected {
		if _, err := parseCommand(command); err == nil {
			t.Errorf("forbidden command %q was accepted", command)
		}
	}
}

func TestAllowedSocket(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	allowed := []string{
		configDir + "/herdr/herdr.sock",
		configDir + "/herdr/herdr-client.sock",
		configDir + "/herdr/sessions/work/herdr.sock",
		configDir + "/herdr/sessions/work/herdr-client.sock",
	}
	for _, path := range allowed {
		if !allowedSocket(path) {
			t.Errorf("allowed socket %q was rejected", path)
		}
	}
	for _, path := range []string{configDir + "/herdr-client.sock", configDir + "/herdr/sessions/../other.sock", "/tmp/herdr.sock"} {
		if allowedSocket(path) {
			t.Errorf("forbidden socket %q was accepted", path)
		}
	}
}

func TestStageImageFromReader(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tempDir)

	content := []byte("fake-png-binary-content-12345")
	path, err := stageImageFromReader(bytes.NewReader(content), "png")
	if err != nil {
		t.Fatalf("stageImageFromReader failed: %v", err)
	}
	if !strings.HasPrefix(path, tempDir) {
		t.Fatalf("expected path to start with tempDir %q, got %q", tempDir, path)
	}
	if !strings.HasSuffix(path, ".png") {
		t.Fatalf("expected path to end with .png, got %q", path)
	}
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if !bytes.Equal(read, content) {
		t.Fatalf("staged content mismatch: got %s, want %s", read, content)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat staged file: %v", err)
	}
	if perm := stat.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected file perm 0600, got %o", perm)
	}

	// Test oversize image limit (>20MB)
	oversize := bytes.Repeat([]byte("a"), (20<<20)+1024)
	if _, err := stageImageFromReader(bytes.NewReader(oversize), "png"); err == nil {
		t.Fatal("expected oversize image to fail, but succeeded")
	}
}
