package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testArchive(t *testing.T, binary []byte, version, extra string, kind byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	tarball := tar.NewWriter(z)
	for _, entry := range []struct{ name, value string }{{"herdrx", string(binary)}, {"VERSION", version + "\n"}, {"README.md", "instructions"}, {extra, "unexpected"}} {
		if entry.name == "" {
			continue
		}
		h := &tar.Header{Name: entry.name, Size: int64(len(entry.value)), Mode: 0o755, Typeflag: tar.TypeReg}
		if entry.name == "herdrx" && kind != 0 {
			h.Typeflag, h.Linkname, h.Size = kind, "outside", 0
		}
		if err := tarball.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size != 0 {
			if _, err := tarball.Write([]byte(entry.value)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDownloadAuthenticatesMetadataBeforeRequestingPinnedBinary(t *testing.T) {
	binary := []byte("signed program bytes")
	base, pub, private := trustedManifest(t, binary)
	base.Version = "v0.2.0"
	for _, tc := range []struct {
		name       string
		mutate     func(*Manifest)
		version    string
		check      bool
		badSign    bool
		wantErr    bool
		wantBinary bool
	}{
		{name: "latest", wantBinary: true},
		{name: "explicit", version: "v0.2.0", wantBinary: true},
		{name: "check only", check: true},
		{name: "bad signature", badSign: true, wantErr: true},
		{name: "expired", mutate: func(m *Manifest) { m.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339) }, wantErr: true},
		{name: "incompatible", mutate: func(m *Manifest) { m.MinState = 2; m.MaxState = 2 }, wantErr: true},
		{name: "different tag", version: "v0.1.0", wantErr: true},
		{name: "rc on stable", mutate: func(m *Manifest) { m.Version = "v0.2.0-rc.1" }, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			if tc.mutate != nil {
				tc.mutate(&m)
			}
			m.Signature = hex.EncodeToString(ed25519.Sign(private, CanonicalPayload(m)))
			if tc.badSign {
				m.Signature = strings.Repeat("0", 128)
			}
			var archiveRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				name := "herdrx-" + runtime.GOOS + "-" + runtime.GOARCH
				if strings.HasSuffix(r.URL.Path, name+".manifest.json") {
					_ = json.NewEncoder(w).Encode(m)
					return
				}
				archiveRequests.Add(1)
				if r.URL.Path != "/download/"+m.Version+"/"+name+".tar.gz" {
					t.Errorf("download was not pinned to authenticated version: %s", r.URL.Path)
				}
				_, _ = w.Write(testArchive(t, binary, m.Version, "", 0))
			}))
			defer server.Close()
			got, program, err := download(context.Background(), server.Client(), server.URL, tc.version, pub, tc.check)
			if (err != nil) != tc.wantErr {
				t.Fatalf("download error = %v", err)
			}
			wantRequests := int32(0)
			if tc.wantBinary {
				wantRequests = 1
				if got.Version != m.Version || !bytes.Equal(program, binary) {
					t.Fatal("wrong downloaded release")
				}
			}
			if archiveRequests.Load() != wantRequests {
				t.Fatal("program requested before metadata validation, or during check-only")
			}
		})
	}
}

func TestReleaseArchiveRejectsLinksTraversalDuplicatesAndTruncation(t *testing.T) {
	binary := []byte("binary")
	good := testArchive(t, binary, "v0.2.0", "", 0)
	for name, archive := range map[string][]byte{
		"symlink":        testArchive(t, binary, "v0.2.0", "", tar.TypeSymlink),
		"traversal":      testArchive(t, binary, "v0.2.0", "../file", 0),
		"duplicate":      testArchive(t, binary, "v0.2.0", "herdrx", 0),
		"wrong version":  testArchive(t, binary, "v0.1.0", "", 0),
		"truncated gzip": good[:len(good)-4],
		"trailing gzip":  append(append([]byte{}, good...), good...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := releaseBinary(archive, "v0.2.0"); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
}

func TestFetchReleaseLimitsAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/error" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("a", 1025)))
	}))
	defer server.Close()
	for _, path := range []string{"/large", "/error"} {
		if _, err := fetchRelease(context.Background(), server.Client(), server.URL+path, 1024); err == nil {
			t.Fatal("accepted oversized or unsuccessful response")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fetchRelease(ctx, server.Client(), server.URL, 1024); err == nil {
		t.Fatal("ignored cancellation")
	}
}
