package herdr

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestLegacyTUIIsRejectedBeforeStartingAProcess(t *testing.T) {
	for _, endpoint := range []Endpoint{&LocalEndpoint{Binary: "must-not-run"}, &SSHEndpoint{}} {
		process, err := endpoint.OpenTerminal(context.Background(), TerminalOpen{PaneID: "w1:p1", Mode: "tui", Cols: 80, Rows: 24})
		if err == nil || process != nil {
			t.Fatal("legacy TUI must not be opened")
		}
	}
}

func TestLocalEndpointStageImage(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tempDir)

	endpoint := &LocalEndpoint{Binary: "herdr"}
	content := []byte("fake-image-bytes-54321")
	path, err := endpoint.StageImage(context.Background(), "png", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("StageImage failed: %v", err)
	}
	if !strings.HasPrefix(path, tempDir) {
		t.Fatalf("expected path in tempDir %q, got %q", tempDir, path)
	}
	if !strings.HasSuffix(path, ".png") {
		t.Fatalf("expected path ending in .png, got %q", path)
	}
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if !bytes.Equal(read, content) {
		t.Fatalf("content mismatch: got %s, want %s", read, content)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat staged file: %v", err)
	}
	if perm := stat.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected file perm 0600, got %o", perm)
	}

	// Test oversize image (>20MB)
	oversize := bytes.Repeat([]byte("x"), (20<<20)+1024)
	if _, err := endpoint.StageImage(context.Background(), "png", bytes.NewReader(oversize)); err == nil {
		t.Fatal("expected oversize image to fail, but succeeded")
	}
}
