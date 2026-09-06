package updater

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var ErrBusy = errors.New("another update or rollback is in progress")

// Release is immutable. Its bytes are checked again before switching/rollback.
type Release struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
}
type installState struct {
	StableLink string   `json:"stable_link"`
	Current    *Release `json:"current,omitempty"`
	Previous   *Release `json:"previous,omitempty"`
	Pending    *Release `json:"pending,omitempty"`
}

type Manager struct {
	dir, stable string
	lock        *os.File
	state       installState
	Recovered   bool
}

func (m *Manager) Pending() bool { return m.state.Pending != nil }

func Open(releasesDir, stableLink string) (*Manager, error) {
	dir, err := filepath.Abs(releasesDir)
	if err != nil {
		return nil, err
	}
	stable, err := filepath.Abs(stableLink)
	if err != nil {
		return nil, err
	}
	if err := ownedDirectory(dir); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".update.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open update lock: %w", err)
	}
	fi, err := lock.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 || int(fi.Sys().(*syscall.Stat_t).Uid) != os.Getuid() {
		_ = lock.Close()
		return nil, errors.New("update lock must be a private regular file owned by the current user")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	m := &Manager{dir: dir, stable: stable, lock: lock, state: installState{StableLink: stable}}
	ok := false
	defer func() {
		if !ok {
			_ = m.Close()
		}
	}()
	raw, err := readRegular(filepath.Join(dir, "state.json"), 64<<10)
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m.state); err != nil {
			return nil, fmt.Errorf("read update state: %w", err)
		}
		if m.state.StableLink != stable {
			return nil, errors.New("release directory belongs to a different stable entrypoint")
		}
		for _, release := range []*Release{m.state.Current, m.state.Previous, m.state.Pending} {
			if release != nil && !m.validPath(release.Path) {
				return nil, errors.New("update state contains an unmanaged binary path")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if m.state.Pending != nil {
		if err := m.Abort(); err != nil {
			return nil, fmt.Errorf("recover interrupted update: %w", err)
		}
		m.Recovered = true
	}
	ok = true
	return m, nil
}

func (m *Manager) Close() error {
	if m.lock == nil {
		return nil
	}
	err := m.lock.Close()
	m.lock = nil
	return err
}

func (m *Manager) validPath(path string) bool {
	rel, err := filepath.Rel(m.dir, path)
	return err == nil && filepath.IsAbs(path) && !strings.HasPrefix(rel, "..") && filepath.Base(path) == "herdrx" && strings.Count(rel, string(os.PathSeparator)) == 1
}

func (m *Manager) Prepare(version string, binary []byte) (*Release, error) {
	if !ValidVersion(version) {
		return nil, errors.New("invalid release version")
	}
	return m.saveRelease(version, version, binary)
}

func (m *Manager) saveRelease(directory, version string, binary []byte) (*Release, error) {
	if len(binary) == 0 || len(binary) > MaxBinarySize {
		return nil, errors.New("invalid binary size")
	}
	dir := filepath.Join(m.dir, directory)
	if err := ownedDirectory(dir); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(binary)
	release := &Release{Path: filepath.Join(dir, "herdrx"), SHA256: hex.EncodeToString(sum[:]), Version: version}
	existing, err := readRegular(release.Path, MaxBinarySize)
	if err == nil {
		if !bytes.Equal(existing, binary) {
			return nil, errors.New("this release version already contains different bytes; choose a new version")
		}
		return release, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := atomicWrite(release.Path, binary, 0o755); err != nil {
		return nil, err
	}
	if err := syncDir(m.dir); err != nil {
		return nil, err
	}
	return release, nil
}

func (m *Manager) verify(release *Release) error {
	if release == nil || !m.validPath(release.Path) {
		return errors.New("invalid release record")
	}
	raw, err := readRegular(release.Path, MaxBinarySize)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != release.SHA256 {
		return errors.New("stored binary hash changed")
	}
	fi, err := os.Stat(release.Path)
	if err != nil || fi.Mode().Perm()&0o111 == 0 {
		return errors.New("stored binary is not executable")
	}
	return nil
}

// Snapshot the actual stable binary, including first installs by the standalone
// installer (a regular file), before any rename can overwrite it.
func (m *Manager) captureCurrent(version string) (*Release, error) {
	fi, err := os.Lstat(m.stable)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	path := m.stable
	if fi.Mode()&os.ModeSymlink != 0 {
		path, err = filepath.EvalSymlinks(m.stable)
		if err != nil {
			return nil, err
		}
	}
	raw, err := readRegular(path, MaxBinarySize)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if m.state.Current != nil && m.state.Current.SHA256 == digest {
		if err := m.verify(m.state.Current); err == nil {
			return m.state.Current, nil
		}
	}
	return m.saveRelease("initial-"+digest, version, raw)
}

func (m *Manager) Switch(candidate *Release, currentVersion string) (bool, error) {
	if m.state.Pending != nil {
		return false, errors.New("an update transaction is already pending")
	}
	if err := m.verify(candidate); err != nil {
		return false, err
	}
	current, err := m.captureCurrent(currentVersion)
	if err != nil {
		return false, err
	}
	if current != nil && current.SHA256 == candidate.SHA256 {
		return false, nil
	}
	if current != nil && current.Version == candidate.Version {
		return false, errors.New("refusing different bytes for the installed version")
	}
	m.state.Current = current
	m.state.Pending = candidate
	// Persist recovery information before changing the only stable entrypoint.
	if err := m.persist(); err != nil {
		return false, err
	}
	if err := replaceLink(m.stable, candidate.Path); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) Commit() error {
	if m.state.Pending == nil {
		return nil
	}
	next := installState{StableLink: m.stable, Current: m.state.Pending, Previous: m.state.Current}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(m.dir, "state.json"), raw, 0o600); err != nil {
		return err
	}
	m.state = next
	return nil
}

func (m *Manager) Previous() (*Release, error) {
	if m.state.Previous == nil {
		return nil, errors.New("no previous compatible binary recorded")
	}
	if err := m.verify(m.state.Previous); err != nil {
		return nil, fmt.Errorf("previous binary: %w", err)
	}
	copy := *m.state.Previous
	return &copy, nil
}

// Abort restores only executable files. Identity/configuration is never copied.
func (m *Manager) Abort() error {
	if m.state.Pending == nil {
		return nil
	}
	fi, err := os.Lstat(m.stable)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		path := m.stable
		if fi.Mode()&os.ModeSymlink != 0 {
			path, err = filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
		}
		raw, err := readRegular(path, MaxBinarySize)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		if digest != m.state.Pending.SHA256 && (m.state.Current == nil || digest != m.state.Current.SHA256) {
			return errors.New("stable entrypoint changed outside the pending transaction; inspect it before recovery")
		}
	}
	if m.state.Current != nil {
		if err := m.verify(m.state.Current); err != nil {
			return fmt.Errorf("cannot recover previous binary: %w", err)
		}
		if err := replaceLink(m.stable, m.state.Current.Path); err != nil {
			return err
		}
	} else if err == nil {
		if err := os.Remove(m.stable); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(m.stable)); err != nil {
			return err
		}
	}
	pending := m.state.Pending
	m.state.Pending = nil
	if err := m.persist(); err != nil {
		m.state.Pending = pending
		return err
	}
	return nil
}

func (m *Manager) persist() error {
	raw, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(m.dir, "state.json"), raw, 0o600)
}

func ownedDirectory(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0o022 != 0 || int(fi.Sys().(*syscall.Stat_t).Uid) != os.Getuid() {
		return fmt.Errorf("%s must be a directory owned by the current user without group/other write access", path)
	}
	return nil
}

func readRegular(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > limit {
		return nil, errors.New("expected a regular file within size limit")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return raw, nil
}

func atomicWrite(path string, raw []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func replaceLink(stable, target string) error {
	if err := ownedDirectory(filepath.Dir(stable)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(stable), ".herdrx-link-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Remove(tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, stable); err != nil {
		return err
	}
	return syncDir(filepath.Dir(stable))
}

// InstallAtomic is the file-only operation; the CLI holds Manager across its
// candidate self-test, service restart and local readiness transaction.
func InstallAtomic(dir, stable, version string, binary []byte) (string, error) {
	m, err := Open(dir, stable)
	if err != nil {
		return "", err
	}
	defer m.Close()
	candidate, err := m.Prepare(version, binary)
	if err != nil {
		return "", err
	}
	defer m.Abort()
	changed, err := m.Switch(candidate, "unknown")
	if err != nil {
		return "", err
	}
	if changed {
		if err := m.Commit(); err != nil {
			return "", err
		}
	}
	previous, err := m.Previous()
	if err != nil {
		return "", nil
	}
	return previous.Path, nil
}
func Rollback(dir, stable string) error {
	m, err := Open(dir, stable)
	if err != nil {
		return err
	}
	defer m.Close()
	previous, err := m.Previous()
	if err != nil {
		return err
	}
	defer m.Abort()
	if _, err := m.Switch(previous, "unknown"); err != nil {
		return err
	}
	return m.Commit()
}
