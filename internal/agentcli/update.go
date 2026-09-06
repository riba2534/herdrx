package agentcli

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/riba2534/herdrx/internal/updater"
)

// Explicit --pubkey is reserved for a separately trusted private distribution.
var UpdateVerifyKeyHex = updater.SigningPublicKeyHex()

func defaultReleasesDir(env Environment) string {
	data := env.DataDir
	if data == "" {
		data = filepath.Join(env.HomeDir, ".local", "share", "herdrx")
	}
	return filepath.Join(data, "releases")
}

type updateFlags struct{ releases, stable, config, runtime *string }

func addUpdateFlags(fs *flag.FlagSet, env Environment) updateFlags {
	return updateFlags{
		releases: fs.String("releases-dir", defaultReleasesDir(env), "versioned binary directory"),
		stable:   fs.String("stable-link", filepath.Join(env.HomeDir, ".local", "bin", "herdrx"), "stable herdrx entrypoint"),
		config:   fs.String("config", filepath.Join(env.ConfigDir, "config.json"), "existing identity configuration"),
		runtime:  fs.String("runtime-dir", env.RuntimeDir, "existing service runtime directory"),
	}
}
func (f updateFlags) normalize(env *Environment) error {
	for _, path := range []*string{f.releases, f.stable, f.config, f.runtime} {
		if *path == "" {
			return errors.New("update paths must not be empty")
		}
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		*path = absolute
	}
	env.RuntimeDir = *f.runtime
	return nil
}

func runUpdate(args []string, stdout, stderr io.Writer, env Environment) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifestPath := fs.String("manifest", "", "signed manifest JSON")
	binaryPath := fs.String("binary", "", "candidate herdrx binary")
	version := fs.String("version", "", "download a specific signed release; default: latest stable")
	checkOnly := fs.Bool("check", false, "verify release metadata without installing")
	pubkeyHex := fs.String("pubkey", UpdateVerifyKeyHex, "trusted Ed25519 public key hex")
	allowDowngrade := fs.Bool("allow-downgrade", false, "explicitly allow an older signed compatible release")
	paths := addUpdateFlags(fs, env)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || (*manifestPath == "") != (*binaryPath == "") || *manifestPath != "" && (*version != "" || *checkOnly) {
		fmt.Fprintln(stderr, "use update [--version vX.Y.Z] [--check], or update --manifest FILE --binary FILE")
		return 2
	}
	pub, err := hex.DecodeString(*pubkeyHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		fmt.Fprintln(stderr, "invalid signing public key")
		return 2
	}
	var manifest updater.Manifest
	var binary []byte
	if *manifestPath == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		manifest, binary, err = updater.Download(ctx, *version, ed25519.PublicKey(pub), *checkOnly)
	} else {
		manifest, binary, err = readUpdateFiles(*manifestPath, *binaryPath)
	}
	if err != nil {
		fmt.Fprintf(stderr, "load update: %v\n", err)
		return 1
	}
	if *checkOnly {
		command := "herdrx update"
		if *version != "" {
			command += " --version " + *version
		}
		fmt.Fprintf(stdout, "verified release: %s (installed: %s); run %s to install\n", manifest.Version, Version, command)
		return 0
	}
	if err := paths.normalize(&env); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return installVerifiedUpdate(manifest, binary, ed25519.PublicKey(pub), *allowDowngrade, paths, stdout, stderr, env)
}

func readUpdateFiles(manifestPath, binaryPath string) (updater.Manifest, []byte, error) {
	manFile, err := openUpdateFile(manifestPath, 16<<10)
	if err != nil {
		return updater.Manifest{}, nil, fmt.Errorf("open manifest: %w", err)
	}
	defer manFile.Close()
	manifest, err := updater.LoadManifest(manFile)
	if err != nil {
		return updater.Manifest{}, nil, fmt.Errorf("manifest: %w", err)
	}
	binFile, err := openUpdateFile(binaryPath, updater.MaxBinarySize)
	if err != nil {
		return updater.Manifest{}, nil, fmt.Errorf("read binary: %w", err)
	}
	defer binFile.Close()
	binary, err := io.ReadAll(io.LimitReader(binFile, updater.MaxBinarySize+1))
	return manifest, binary, err
}

func installVerifiedUpdate(manifest updater.Manifest, binary []byte, pub ed25519.PublicKey, allowDowngrade bool, paths updateFlags, stdout, stderr io.Writer, env Environment) int {
	if err := updater.Verify(manifest, pub, binary); err != nil {
		fmt.Fprintf(stderr, "reject update: %v\n", err)
		return 1
	}
	manager, err := updater.Open(*paths.releases, *paths.stable)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer manager.Close()
	previous, err := probeExecutable(env, *paths.stable, *paths.config)
	if err != nil {
		fmt.Fprintf(stderr, "current binary self-test: %v\n", err)
		return 1
	}
	if err := prepareUpdateService(env, *paths.stable, *paths.config); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if manager.Recovered {
		if err := restartAndWait(env, previous, *paths.config, 30*time.Second); err != nil {
			fmt.Fprintf(stderr, "interrupted update files recovered, service recovery failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(stderr, "interrupted update recovered; the previous service is ready. Run the update again to install the requested release.")
		return 1
	}
	comparison, err := updater.CompareVersions(manifest.Version, previous.Version)
	if !allowDowngrade && (err != nil || comparison < 0) {
		fmt.Fprintln(stderr, "reject downgrade or unknown current version; use --allow-downgrade only for an explicitly trusted compatible package")
		return 1
	}
	candidate, err := manager.Prepare(manifest.Version, binary)
	if err != nil {
		fmt.Fprintf(stderr, "stage update: %v\n", err)
		return 1
	}
	ready, err := probeExecutable(env, candidate.Path, *paths.config)
	if err != nil || ready.Version != manifest.Version || ready.ProtocolVersion < manifest.MinProto || ready.ProtocolVersion > manifest.MaxProto || ready.StateVersion < manifest.MinState || ready.StateVersion > manifest.MaxState || ready.Identity != previous.Identity || ready.Epoch < previous.Epoch {
		fmt.Fprintf(stderr, "candidate self-test rejected (version, compatibility or identity): %v\n", err)
		return 1
	}
	return applyRelease(manager, candidate, previous, env, *paths.config, stdout, stderr)
}

func runRollback(args []string, stdout, stderr io.Writer, env Environment) int {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	fs.SetOutput(stderr)
	paths := addUpdateFlags(fs, env)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return 2
	}
	if err := paths.normalize(&env); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	manager, err := updater.Open(*paths.releases, *paths.stable)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer manager.Close()
	current, err := probeExecutable(env, *paths.stable, *paths.config)
	if err != nil {
		fmt.Fprintf(stderr, "current identity self-test: %v\n", err)
		return 1
	}
	if err := prepareUpdateService(env, *paths.stable, *paths.config); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if manager.Recovered {
		if err := restartAndWait(env, current, *paths.config, 30*time.Second); err != nil {
			fmt.Fprintf(stderr, "recovered files but service is not ready: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "interrupted update recovered; previous service is ready, identity and revocations preserved")
		return 0
	}
	previous, err := manager.Previous()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	candidate, err := probeExecutable(env, previous.Path, *paths.config)
	if err != nil || candidate.Identity != current.Identity || candidate.Epoch < current.Epoch || candidate.ProtocolVersion != current.ProtocolVersion || candidate.StateVersion != current.StateVersion || candidate.Version != previous.Version {
		fmt.Fprintf(stderr, "previous binary cannot safely read the current identity: %v\n", err)
		return 1
	}
	return applyRelease(manager, previous, current, env, *paths.config, stdout, stderr)
}

func applyRelease(manager *updater.Manager, candidate *updater.Release, previous Readiness, env Environment, configPath string, stdout, stderr io.Writer) int {
	// Abort on every pre-commit failure; report recovery failures rather than
	// letting deferred cleanup falsely claim that the old service is available.
	changed, err := manager.Switch(candidate, previous.Version)
	if err != nil && !manager.Pending() {
		fmt.Fprintf(stderr, "update rejected before switching: %v\n", err)
		return 1
	}
	if err == nil && !changed {
		if err := waitForReadiness(env, previous, configPath, time.Second); err != nil {
			if err := restartAndWait(env, previous, configPath, updateHealthTimeout(env)); err != nil {
				fmt.Fprintf(stderr, "binary unchanged but service is not ready: %v\n", err)
				return 1
			}
		}
		fmt.Fprintf(stdout, "%s is already installed; the previous rollback point is unchanged\n", candidate.Version)
		return 0
	}
	if err == nil {
		next := previous
		next.Version = candidate.Version
		err = restartAndWait(env, next, configPath, updateHealthTimeout(env))
	}
	if err == nil {
		err = manager.Commit()
	}
	if err != nil {
		fmt.Fprintf(stderr, "update failed: %v\n", err)
		if restoreErr := manager.Abort(); restoreErr != nil {
			fmt.Fprintf(stderr, "automatic file recovery failed: %v; keep the release directory and inspect the pending transaction\n", restoreErr)
			return 1
		}
		if restoreErr := restartAndWait(env, previous, configPath, updateHealthTimeout(env)); restoreErr != nil {
			fmt.Fprintf(stderr, "previous binary restored but service recovery failed: %v\n", restoreErr)
			return 1
		}
		fmt.Fprintf(stderr, "restored %s and verified the original service; identity and revocations preserved\n", previous.Version)
		return 1
	}
	fmt.Fprintf(stdout, "installed %s; local service version and identity verified\n", candidate.Version)
	fmt.Fprintln(stdout, "Herdr and its tasks were not stopped; rollback never restores revoked authorization")
	return 0
}

func updateHealthTimeout(env Environment) time.Duration {
	if env.UpdateHealthTimeout > 0 {
		return env.UpdateHealthTimeout
	}
	return 30 * time.Second
}

func probeExecutable(env Environment, executable, configPath string) (Readiness, error) {
	var result Readiness
	runner := env.CommandRunner
	if runner == nil {
		runner = func(name string, args ...string) ([]byte, error) {
			return RunBoundedCommand(context.Background(), 5*time.Second, 64<<10, name, args...)
		}
	}
	raw, err := runner(executable, "self-test", "--json", "--config", configPath)
	if err != nil {
		return result, fmt.Errorf("self-test command failed: %w: %s", err, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("invalid self-test response: %w", err)
	}
	if result.Version == "" || result.Identity == "" || result.ConfigPath != configPath || result.ProtocolVersion != updater.ProtocolVersion || result.StateVersion != updater.StateVersion {
		return result, errors.New("incomplete or incompatible self-test response")
	}
	return result, nil
}

func restartAndWait(env Environment, expected Readiness, configPath string, timeout time.Duration) error {
	runner := env.ServiceRunner
	if runner == nil {
		runner = NewRealServiceRunner()
	}
	if err := runner.Restart("herdrx.service"); err != nil {
		return err
	}
	return waitForReadiness(env, expected, configPath, timeout)
}

func waitForReadiness(env Environment, expected Readiness, configPath string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var lastErr error
	for {
		var actual Readiness
		callCtx, callCancel := context.WithTimeout(ctx, time.Second)
		err := clientCallIPCContext(callCtx, filepath.Join(env.RuntimeDir, "control.sock"), "GET", "/ready", configPath, nil, &actual)
		callCancel()
		if err == nil {
			cfg, loadErr := SafeLoadConfig(configPath)
			if loadErr != nil {
				return fmt.Errorf("read identity after restart: %w", loadErr)
			}
			disk, loadErr := readiness(&cfg, configPath)
			if loadErr != nil {
				return loadErr
			}
			if actual.Version == expected.Version && actual.Identity == expected.Identity && actual.Identity == disk.Identity && actual.ConfigPath == configPath && actual.PID > 0 && actual.StateVersion == expected.StateVersion && actual.ProtocolVersion == expected.ProtocolVersion && actual.Epoch >= expected.Epoch && actual.Epoch == disk.Epoch && actual.Revoked == disk.Revoked {
				return nil
			}
			err = errors.New("daemon version or loaded identity does not match the candidate/current state")
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("local readiness timeout: %w", lastErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func prepareUpdateService(env Environment, stable, configPath string) error {
	// Only repair the generated unit, preserving the chosen identity path. A
	// custom unit must be reviewed by its owner instead of silently overwritten.
	path := serviceUnitPath(env)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read herdrx.service; first run herdrx setup: %w", err)
	}
	if !strings.Contains(string(raw), "Description=herdrx remote access agent") || !strings.Contains(string(raw), " --config "+systemdArgument(configPath)) {
		return errors.New("service is custom or uses another config; use the matching --config and generated herdrx.service")
	}
	desired := GenerateSystemdUnit(stable, configPath, env.RuntimeDir)
	if string(raw) == desired {
		return nil
	}
	runner := env.ServiceRunner
	if runner == nil {
		runner = NewRealServiceRunner()
	}
	if err := runner.WriteUnitFile(path, []byte(desired)); err != nil {
		return err
	}
	return runner.DaemonReload()
}

func openUpdateFile(path string, limit int64) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > limit {
		file.Close()
		return nil, errors.New("update input must be a regular file within size limit")
	}
	return file, nil
}
