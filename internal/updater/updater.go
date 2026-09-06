package updater

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ProtocolVersion and StateVersion describe the compatibility of this release,
// independently of Herdr's own terminal protocol and the website's SQL schema.
const ProtocolVersion = 1
const StateVersion = 1
const MaxBinarySize = 128 << 20

//go:embed release.pub
var publishedSigningKey string

// SigningPublicKeyHex is the repository-published trust root, never a key from
// the download response. Changing it requires an explicitly trusted release.
func SigningPublicKeyHex() string { return strings.TrimSpace(publishedSigningKey) }

var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(?:-rc\.(0|[1-9][0-9]{0,8}))?$`)

type Manifest struct {
	Version   string `json:"version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	SHA256    string `json:"sha256"`
	MinProto  int    `json:"min_proto"`
	MaxProto  int    `json:"max_proto"`
	MinState  int    `json:"min_state"`
	MaxState  int    `json:"max_state"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	Signature string `json:"signature"`
}

func CanonicalPayload(m Manifest) []byte {
	m.Signature = ""
	encoded, _ := json.Marshal(m)
	return encoded
}

func ValidVersion(version string) bool { return releaseVersion.MatchString(version) }

// CompareVersions accepts exactly the stable/RC tags accepted by the publisher.
func CompareVersions(a, b string) (int, error) {
	aa, bb := releaseVersion.FindStringSubmatch(a), releaseVersion.FindStringSubmatch(b)
	if aa == nil || bb == nil {
		return 0, errors.New("versions must use vX.Y.Z or vX.Y.Z-rc.N")
	}
	for i := 1; i <= 3; i++ {
		av, _ := strconv.Atoi(aa[i])
		bv, _ := strconv.Atoi(bb[i])
		if av < bv {
			return -1, nil
		}
		if av > bv {
			return 1, nil
		}
	}
	if aa[4] == bb[4] {
		return 0, nil
	}
	if aa[4] == "" {
		return 1, nil
	}
	if bb[4] == "" {
		return -1, nil
	}
	av, _ := strconv.Atoi(aa[4])
	bv, _ := strconv.Atoi(bb[4])
	if av < bv {
		return -1, nil
	}
	return 1, nil
}

func Verify(m Manifest, pub ed25519.PublicKey, binary []byte) error {
	return VerifyAt(m, pub, binary, time.Now())
}

func VerifyAt(m Manifest, pub ed25519.PublicKey, binary []byte, now time.Time) error {
	if err := VerifyManifestAt(m, pub, now); err != nil {
		return err
	}
	if len(binary) == 0 || len(binary) > MaxBinarySize {
		return errors.New("invalid binary size")
	}
	sum := sha256.Sum256(binary)
	if !strings.EqualFold(m.SHA256, hex.EncodeToString(sum[:])) {
		return errors.New("binary sha256 mismatch")
	}
	return nil
}

// VerifyManifestAt authenticates metadata before requesting any program bytes.
func VerifyManifestAt(m Manifest, pub ed25519.PublicKey, now time.Time) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid signing public key")
	}
	sig, err := hex.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, CanonicalPayload(m), sig) {
		return errors.New("manifest signature verification failed")
	}
	if !ValidVersion(m.Version) {
		return errors.New("invalid release version")
	}
	if m.GOOS != runtime.GOOS || m.GOARCH != runtime.GOARCH {
		return fmt.Errorf("manifest platform %s/%s does not match %s/%s", m.GOOS, m.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
	if m.MinProto < 1 || m.MaxProto < m.MinProto || ProtocolVersion < m.MinProto || ProtocolVersion > m.MaxProto {
		return errors.New("update is incompatible with this access protocol")
	}
	if m.MinState < 1 || m.MaxState < m.MinState || StateVersion < m.MinState || StateVersion > m.MaxState {
		return errors.New("update is incompatible with the current identity state format")
	}
	created, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil || created.After(now.Add(5*time.Minute)) {
		return errors.New("manifest creation time is invalid or in the future")
	}
	expires, err := time.Parse(time.RFC3339, m.ExpiresAt)
	if err != nil || !expires.After(created) || !expires.After(now) || expires.Sub(created) > 366*24*time.Hour {
		return errors.New("manifest is expired or has an invalid validity period")
	}
	if hash, err := hex.DecodeString(m.SHA256); err != nil || len(hash) != sha256.Size {
		return errors.New("invalid binary sha256")
	}
	return nil
}

func LoadManifest(r io.Reader) (Manifest, error) {
	var m Manifest
	raw, err := io.ReadAll(io.LimitReader(r, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return Manifest{}, errors.New("manifest exceeds size limit or could not be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return Manifest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Manifest{}, errors.New("manifest must contain one JSON object")
	}
	if !ValidVersion(m.Version) || len(m.SHA256) != 64 || m.CreatedAt == "" || m.ExpiresAt == "" {
		return Manifest{}, errors.New("manifest is incomplete")
	}
	return m, nil
}
