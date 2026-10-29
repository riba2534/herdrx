package agentcli

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestTranscriptDebugAgyOmitsText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"transcript-debug"}, &stdout, &stderr, Environment{}); code != 2 {
		t.Fatalf("usage code=%d stderr=%s", code, stderr.String())
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"transcript-debug", "--agy", missing}, &stdout, &stderr, Environment{}); code != 1 {
		t.Fatalf("missing code=%d stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "无法读取 Antigravity 会话文件") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	path := filepath.Join(t.TempDir(), "conv.db")
	secret := "CLI_SECRET_DO_NOT_PRINT"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE steps (idx integer primary key, step_type integer, status integer, step_format integer, step_payload blob)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	// field 19, subfield 2, the secret string. Wire: 19<<3|2 = 154, length, then 2<<3|2 = 18, length, bytes.
	payload := append([]byte{0x9a, 0x01}, byte(2+len(secret)))
	payload = append(payload, 0x12, byte(len(secret)))
	payload = append(payload, secret...)
	if _, err := db.Exec(`INSERT INTO steps (idx, step_type, status, step_format, step_payload) VALUES (1, 14, 3, 0, ?)`, payload); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"transcript-debug", "--agy", path}, &stdout, &stderr, Environment{}); code != 0 {
		t.Fatalf("debug code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("debug printed text stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "idx=1 step_type=14 role=user blocks=text") {
		t.Fatalf("debug=%s", stdout.String())
	}
}
