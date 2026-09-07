package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const cliRepositoryURL = "https://github.com/riba2534/herdrx"
const cliReleasesAPI = "https://api.github.com/repos/riba2534/herdrx/releases?per_page=20"

var cliReleaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

type cliReleaseInfo struct {
	Status      string `json:"status"`
	Version     string `json:"version,omitempty"`
	Prerelease  bool   `json:"prerelease,omitempty"`
	ReleasesURL string `json:"releases_url"`
	DownloadURL string `json:"download_url,omitempty"`
}

type cliReleaseCache struct {
	mu      sync.Mutex
	group   singleflight.Group
	client  *http.Client
	value   cliReleaseInfo
	expires time.Time
}

func newCLIReleaseCache() *cliReleaseCache {
	return &cliReleaseCache{client: &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (a *API) cliRelease(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.cliReleases.get(r.Context()))
}

func (c *cliReleaseCache) get(ctx context.Context) cliReleaseInfo {
	c.mu.Lock()
	if time.Now().Before(c.expires) {
		value := c.value
		c.mu.Unlock()
		return value
	}
	c.mu.Unlock()
	// The public metadata request is shared, bounded, and independent of an
	// individual browser closing its guide. No user credentials leave this API.
	result := c.group.DoChan("release", func() (any, error) {
		c.mu.Lock()
		if time.Now().Before(c.expires) {
			value := c.value
			c.mu.Unlock()
			return value, nil
		}
		c.mu.Unlock()
		value := c.fetch()
		ttl := 5 * time.Minute
		if value.Status != "available" {
			ttl = time.Minute
		}
		c.mu.Lock()
		c.value, c.expires = value, time.Now().Add(ttl)
		c.mu.Unlock()
		return value, nil
	})
	select {
	case value := <-result:
		return value.Val.(cliReleaseInfo)
	case <-ctx.Done():
		return cliReleaseInfo{Status: "unavailable", ReleasesURL: cliRepositoryURL + "/releases"}
	}
}

func (c *cliReleaseCache) fetch() cliReleaseInfo {
	info := cliReleaseInfo{Status: "unavailable", ReleasesURL: cliRepositoryURL + "/releases"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, cliReleasesAPI, nil)
	if err != nil {
		return info
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "herdrx-cli-install-guide")
	response, err := c.client.Do(request)
	if err != nil {
		return info
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return info
	}
	var releases []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name  string `json:"name"`
			State string `json:"state"`
			Size  int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&releases); err != nil {
		return info
	}
	info.Status = "unpublished"
	for _, release := range releases {
		if release.Draft || !cliReleaseVersion.MatchString(release.Tag) {
			continue
		}
		assets := make(map[string]bool)
		for _, asset := range release.Assets {
			assets[asset.Name] = asset.State == "uploaded" && asset.Size > 0
		}
		complete := true
		for _, name := range []string{"herdrx-linux-amd64.tar.gz", "herdrx-linux-arm64.tar.gz", "herdrx-linux-amd64.manifest.json", "herdrx-linux-arm64.manifest.json", "SHA256SUMS", "install-herdrx.sh", "README-CLI.md", "RELEASE-PUBLIC-KEY"} {
			if !assets[name] {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		if info.Status == "available" && release.Prerelease {
			continue
		}
		info.Status, info.Version, info.Prerelease = "available", release.Tag, release.Prerelease
		info.DownloadURL = cliRepositoryURL + "/releases/download/" + release.Tag
		if !release.Prerelease {
			break
		}
	}
	return info
}
