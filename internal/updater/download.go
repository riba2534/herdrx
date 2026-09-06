package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const releaseURL = "https://github.com/riba2534/herdrx/releases"

// Download obtains a signed release from the fixed official repository. An
// empty version selects the latest stable release. checkOnly fetches metadata.
func Download(ctx context.Context, version string, pub ed25519.PublicKey, checkOnly bool) (Manifest, []byte, error) {
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("release redirect must use HTTPS and stay within five hops")
		}
		return nil
	}}
	return download(ctx, client, releaseURL, version, pub, checkOnly)
}

func download(ctx context.Context, client *http.Client, base, version string, pub ed25519.PublicKey, checkOnly bool) (Manifest, []byte, error) {
	if version != "" && !ValidVersion(version) {
		return Manifest{}, nil, errors.New("version must use vX.Y.Z or vX.Y.Z-rc.N")
	}
	path := "/latest/download/"
	if version != "" {
		path = "/download/" + version + "/"
	}
	name := "herdrx-" + runtime.GOOS + "-" + runtime.GOARCH
	raw, err := fetchRelease(ctx, client, base+path+name+".manifest.json", 16<<10)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("download signed manifest: %w", err)
	}
	m, err := LoadManifest(bytes.NewReader(raw))
	if err != nil {
		return Manifest{}, nil, err
	}
	if err := VerifyManifestAt(m, pub, time.Now()); err != nil {
		return Manifest{}, nil, err
	}
	if version != "" && m.Version != version || version == "" && strings.Contains(m.Version, "-") {
		return Manifest{}, nil, errors.New("manifest does not match the requested release channel")
	}
	if checkOnly {
		return m, nil, nil
	}
	// Pin the archive to the authenticated tag, avoiding a moving latest alias
	// between requests. Its binary digest remains the authority for integrity.
	compressed, err := fetchRelease(ctx, client, base+"/download/"+m.Version+"/"+name+".tar.gz", MaxBinarySize+(1<<20))
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("download release archive: %w", err)
	}
	binary, err := releaseBinary(compressed, m.Version)
	if err != nil {
		return Manifest{}, nil, err
	}
	if err := Verify(m, pub, binary); err != nil {
		return Manifest{}, nil, err
	}
	return m, binary, nil
}

func fetchRelease(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "herdrx-updater/1")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release download returned HTTP %d", res.StatusCode)
	}
	if res.ContentLength > limit {
		return nil, errors.New("release download exceeds size limit")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("release download is incomplete or exceeds size limit")
	}
	return raw, nil
}

func releaseBinary(compressed []byte, version string) ([]byte, error) {
	raw := bytes.NewReader(compressed)
	z, err := gzip.NewReader(raw)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	z.Multistream(false)
	limited := &io.LimitedReader{R: z, N: MaxBinarySize + (2 << 20)}
	archive := tar.NewReader(limited)
	seen := map[string]bool{}
	var binary []byte
	for {
		member, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid release archive: %w", err)
		}
		maxSize := int64(512 << 10)
		if member.Name == "herdrx" {
			maxSize = MaxBinarySize
		} else if member.Name == "THIRD_PARTY_NOTICES.md" {
			maxSize = 1 << 20
		}
		if (member.Name != "herdrx" && member.Name != "VERSION" && member.Name != "README.md" && member.Name != "LICENSE" && member.Name != "THIRD_PARTY_NOTICES.md") || seen[member.Name] || member.Typeflag != tar.TypeReg || member.Size <= 0 || member.Size > maxSize {
			return nil, errors.New("unexpected, duplicate, non-regular or oversized release archive member")
		}
		seen[member.Name] = true
		content, err := io.ReadAll(archive)
		if err != nil {
			return nil, err
		}
		if member.Name == "herdrx" {
			binary = content
		}
		if member.Name == "VERSION" && string(content) != version+"\n" {
			return nil, errors.New("archive version differs from signed manifest")
		}
	}
	// Consume the gzip trailer to validate its checksum, bounded even if a
	// malicious tar appends arbitrarily large padding after its end marker.
	if _, err := io.Copy(io.Discard, limited); err != nil || limited.N == 0 || raw.Len() != 0 {
		return nil, errors.New("invalid, trailing or oversized release archive data")
	}
	if !seen["herdrx"] || !seen["VERSION"] || !seen["README.md"] {
		return nil, errors.New("release archive is incomplete")
	}
	return binary, nil
}
