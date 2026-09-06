package updater

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func trustedManifest(t *testing.T, binary []byte) (Manifest, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	now := time.Now().UTC()
	m := Manifest{Version: "v0.1.0-rc.1", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, SHA256: hex.EncodeToString(sum[:]), MinProto: 1, MaxProto: 1, MinState: 1, MaxState: 1, CreatedAt: now.Add(-time.Minute).Format(time.RFC3339), ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339)}
	m.Signature = hex.EncodeToString(ed25519.Sign(priv, CanonicalPayload(m)))
	return m, pub, priv
}
func TestVerifyRejectsUntrustedOrIncompatiblePackages(t *testing.T) {
	binary := []byte("candidate")
	valid, pub, priv := trustedManifest(t, binary)
	if err := Verify(valid, pub, binary); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"protocol", func(m *Manifest) { m.MinProto = 999; m.MaxProto = 999 }},
		{"state", func(m *Manifest) { m.MinState = 2; m.MaxState = 2 }},
		{"missing compatibility", func(m *Manifest) { m.MinState = 0 }},
		{"platform", func(m *Manifest) { m.GOARCH = "unsupported" }},
		{"missing platform", func(m *Manifest) { m.GOOS = "" }},
		{"expired", func(m *Manifest) { m.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339) }},
		{"future", func(m *Manifest) { m.CreatedAt = time.Now().Add(time.Hour).Format(time.RFC3339) }},
		{"missing creation", func(m *Manifest) { m.CreatedAt = "" }},
		{"path traversal", func(m *Manifest) { m.Version = "../../outside" }},
		{"hash", func(m *Manifest) { m.SHA256 = strings.Repeat("0", 64) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := valid
			tc.mutate(&m)
			m.Signature = hex.EncodeToString(ed25519.Sign(priv, CanonicalPayload(m)))
			if err := Verify(m, pub, binary); err == nil {
				t.Fatal("accepted invalid signed package")
			}
		})
	}
	if err := Verify(valid, pub, []byte("tampered")); err == nil {
		t.Fatal("accepted changed binary")
	}
	if err := Verify(valid, nil, binary); err == nil {
		t.Fatal("accepted invalid public key")
	}
	valid.Signature = strings.Repeat("0", 128)
	if err := Verify(valid, pub, binary); err == nil {
		t.Fatal("accepted invalid signature")
	}
}
func TestLoadManifestDoesNotRepairSignedFields(t *testing.T) {
	m, _, _ := trustedManifest(t, []byte("candidate"))
	encoded, _ := json.Marshal(m)
	if _, err := LoadManifest(bytes.NewReader(encoded)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{string(encoded) + "{}", string(encoded) + strings.Repeat(" ", 17<<10), strings.Replace(string(encoded), `"min_proto":1`, `"unknown":1`, 1), strings.Replace(string(encoded), `"created_at":"`+m.CreatedAt+`"`, `"created_at":""`, 1)} {
		if _, err := LoadManifest(strings.NewReader(raw)); err == nil {
			t.Fatal("accepted malformed manifest")
		}
	}
}
func TestVersionOrdering(t *testing.T) {
	versions := []string{"v0.1.0-rc.1", "v0.1.0-rc.2", "v0.1.0-rc.10", "v0.1.0", "v0.2.0", "v1.0.0"}
	for i, a := range versions {
		for j, b := range versions {
			got, err := CompareVersions(a, b)
			if err != nil || (i < j && got >= 0) || (i == j && got != 0) || (i > j && got <= 0) {
				t.Fatalf("compare %s %s = %d, %v", a, b, got, err)
			}
		}
	}
	for _, bad := range []string{"../escape", "v1", "v01.1.0", "v1.1.0-rc.01", "v1.1.0/other", "v999999999999.0.0"} {
		if ValidVersion(bad) {
			t.Fatal("accepted", bad)
		}
	}
}
func checkBinary(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != want {
		t.Fatalf("binary=%q want=%q err=%v", raw, want, err)
	}
}
func TestSameVersionRetryPreservesRollbackAndIdentity(t *testing.T) {
	dir := t.TempDir()
	releases, stable := filepath.Join(dir, "releases"), filepath.Join(dir, "bin", "herdrx")
	identity := filepath.Join(dir, "identity.json")
	os.WriteFile(identity, []byte(`{"revoked":true,"epoch":42}`), 0o600)
	for _, v := range []string{"v0.1.0", "v0.2.0", "v0.2.0"} {
		if _, err := InstallAtomic(releases, stable, v, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	checkBinary(t, stable, "v0.2.0")
	if err := Rollback(releases, stable); err != nil {
		t.Fatal(err)
	}
	checkBinary(t, stable, "v0.1.0")
	checkBinary(t, identity, `{"revoked":true,"epoch":42}`)
	if err := Rollback(releases, stable); err != nil {
		t.Fatal(err)
	}
	checkBinary(t, stable, "v0.2.0")
	if _, err := InstallAtomic(releases, stable, "v0.2.0", []byte("replaced")); err == nil {
		t.Fatal("replaced immutable version")
	}
	checkBinary(t, stable, "v0.2.0")
}
func TestRegularInstallerBinaryAndInterruptedUpdate(t *testing.T) {
	dir := t.TempDir()
	stable := filepath.Join(dir, "bin", "herdrx")
	os.MkdirAll(filepath.Dir(stable), 0o755)
	os.WriteFile(stable, []byte("original"), 0o755)
	releases := filepath.Join(dir, "releases")
	m, err := Open(releases, stable)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Prepare("v0.2.0", []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Switch(r, "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	checkBinary(t, stable, "new")
	m.Close() // A process dies before local readiness/Commit.
	m, err = Open(releases, stable)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Recovered {
		t.Fatal("pending transaction not recovered")
	}
	checkBinary(t, stable, "original")
	if _, err = m.Switch(r, "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err = m.Commit(); err != nil {
		t.Fatal(err)
	}
	m.Close()
	if err = Rollback(releases, stable); err != nil {
		t.Fatal(err)
	}
	checkBinary(t, stable, "original")
}
func TestUpdateLockAndFailedSwitchLeaveOriginal(t *testing.T) {
	dir := t.TempDir()
	stable, releases := filepath.Join(dir, "bin", "herdrx"), filepath.Join(dir, "releases")
	if _, err := InstallAtomic(releases, stable, "v0.1.0", []byte("one")); err != nil {
		t.Fatal(err)
	}
	m, err := Open(releases, stable)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := Open(releases, stable); !errors.Is(err, ErrBusy) {
		t.Fatalf("parallel update allowed: %v", err)
	}
	r, err := m.Prepare("v0.2.0", []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	// A filesystem error while recording the transaction occurs before the link changes.
	if err := os.Remove(filepath.Join(releases, "state.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(releases, "state.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Switch(r, "v0.1.0"); err == nil {
		t.Fatal("ignored metadata write failure")
	}
	checkBinary(t, stable, "one")
	os.Remove(filepath.Join(releases, "state.json"))
	if err = m.Abort(); err != nil {
		t.Fatal(err)
	}
	checkBinary(t, stable, "one")
}
func TestRejectManagedPathSymlinksAndCorruptRollback(t *testing.T) {
	dir := t.TempDir()
	stable, releases := filepath.Join(dir, "bin", "herdrx"), filepath.Join(dir, "releases")
	for _, v := range []string{"v0.1.0", "v0.2.0"} {
		if _, err := InstallAtomic(releases, stable, v, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(releases, "v0.1.0", "herdrx"), []byte("corrupted"), 0o755)
	if err := Rollback(releases, stable); err == nil {
		t.Fatal("rolled back to corrupt executable")
	}
	checkBinary(t, stable, "v0.2.0")
	outside := filepath.Join(dir, "outside")
	os.MkdirAll(outside, 0o755)
	os.Symlink(outside, filepath.Join(releases, "v0.3.0"))
	if _, err := InstallAtomic(releases, stable, "v0.3.0", []byte("bad")); err == nil {
		t.Fatal("followed version directory symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "herdrx")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote outside managed directory")
	}
}
