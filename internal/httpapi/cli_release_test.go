package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func releaseFixture(tag string, prerelease bool) map[string]any {
	assets := []map[string]any{}
	for _, name := range []string{"herdrx-linux-amd64.tar.gz", "herdrx-linux-arm64.tar.gz", "install-herdrx.sh", "SHA256SUMS"} {
		assets = append(assets, map[string]any{"name": name, "state": "uploaded", "size": 100, "browser_download_url": "https://untrusted.example.test/ignored"})
	}
	return map[string]any{"tag_name": tag, "draft": false, "prerelease": prerelease, "assets": assets}
}

func TestCLIReleaseAvailability(t *testing.T) {
	stable, preview := releaseFixture("v0.1.0", false), releaseFixture("v0.2.0-rc.1", true)
	incomplete := releaseFixture("v0.3.0", false)
	incomplete["assets"] = []map[string]any{}
	draft := releaseFixture("v0.4.0", false)
	draft["draft"] = true
	for _, tc := range []struct {
		name, status, version string
		releases              []map[string]any
	}{
		{"empty", "unpublished", "", []map[string]any{}},
		{"incomplete", "unpublished", "", []map[string]any{incomplete, draft}},
		{"stable preferred", "available", "v0.1.0", []map[string]any{draft, incomplete, preview, stable}},
		{"preview fallback", "available", "v0.2.0-rc.1", []map[string]any{preview}},
		{"unsafe tag", "unpublished", "", []map[string]any{releaseFixture("v0.1.0; echo x", false)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.releases)
			cache := newCLIReleaseCache()
			cache.client.Transport = releaseTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != cliReleasesAPI || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("unexpected public metadata request")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			got := cache.get(context.Background())
			if got.Status != tc.status || got.Version != tc.version {
				t.Fatalf("unexpected release: %+v", got)
			}
			if got.Status == "available" && got.DownloadURL != cliRepositoryURL+"/releases/download/"+tc.version {
				t.Fatal("download URL must be constructed from fixed repository and validated version")
			}
		})
	}
}

func TestCLIReleaseUnavailableAndCache(t *testing.T) {
	for _, status := range []int{200, 403, 429, 500} {
		cache := newCLIReleaseCache()
		var count atomic.Int32
		cache.client.Transport = releaseTransport(func(*http.Request) (*http.Response, error) {
			count.Add(1)
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("not JSON"))}, nil
		})
		for range 3 {
			if cache.get(context.Background()).Status != "unavailable" {
				t.Fatal("metadata failure should not offer a broken install command")
			}
		}
		if count.Load() != 1 {
			t.Fatal("negative metadata result was not cached")
		}
	}
}

func TestCLIReleaseConcurrentAndCanceledReaders(t *testing.T) {
	cache := newCLIReleaseCache()
	started, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	cache.client.Transport = releaseTransport(func(*http.Request) (*http.Response, error) {
		if count.Add(1) == 1 {
			close(started)
		}
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("[]"))}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan cliReleaseInfo, 1)
	go func() { first <- cache.get(ctx) }()
	<-started
	cancel()
	select {
	case result := <-first:
		if result.Status != "unavailable" {
			t.Fatal("canceled reader should return promptly")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled browser blocked on GitHub")
	}
	var readers sync.WaitGroup
	for range 20 {
		readers.Go(func() {
			if cache.get(context.Background()).Status != "unpublished" {
				t.Error("shared fetch was canceled")
			}
		})
	}
	close(release)
	readers.Wait()
	if count.Load() != 1 {
		t.Fatalf("metadata request fan-out: %d", count.Load())
	}
}
