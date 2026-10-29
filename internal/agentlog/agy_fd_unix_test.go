//go:build unix

package agentlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 只用 /dev/fd 列 fd 号（macOS 的路径），身份照样来自 fstat，正常读取不被误拒。
func TestQueryAgyDBWorksWithTheDevFDListing(t *testing.T) {
	if _, err := os.ReadDir("/dev/fd"); err != nil {
		t.Skipf("/dev/fd unavailable: %v", err)
	}
	saved := agyFDDirs
	agyFDDirs = []string{"/dev/fd"}
	defer func() { agyFDDirs = saved }()
	path := filepath.Join(t.TempDir(), "conv.db")
	writeAgyDB(t, path, []agyDBStep{{Idx: 1, StepType: 14, Payload: pbBytesField(19, pbStringField(1, "via /dev/fd"))}})
	verified, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := QueryAgyDB(context.Background(), path, verified, AgyBounds{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	agyFDDirs = []string{"/nonexistent-fd-dir"}
	if _, _, err := QueryAgyDB(context.Background(), path, verified, AgyBounds{Limit: 10}); !errors.Is(err, ErrAgyDenied) {
		t.Fatalf("a missing fd table must fail closed: %v", err)
	}
}
