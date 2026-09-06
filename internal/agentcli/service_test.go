package agentcli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type FakeServiceRunner struct {
	LingerOk bool
	Active   bool
	Calls    []string
	Units    map[string][]byte
}

func (f *FakeServiceRunner) IsActive(name string) (bool, error) {
	f.Calls = append(f.Calls, "IsActive:"+name)
	return f.Active, nil
}

func (f *FakeServiceRunner) CheckLinger(user string) (bool, error) {
	f.Calls = append(f.Calls, "CheckLinger:"+user)
	return f.LingerOk, nil
}

func (f *FakeServiceRunner) DaemonReload() error {
	f.Calls = append(f.Calls, "DaemonReload")
	return nil
}

func (f *FakeServiceRunner) EnableAndStart(name string) error {
	f.Calls = append(f.Calls, "EnableAndStart:"+name)
	return nil
}

func (f *FakeServiceRunner) Stop(name string) error {
	f.Calls = append(f.Calls, "Stop:"+name)
	return nil
}

func (f *FakeServiceRunner) Restart(name string) error {
	f.Calls = append(f.Calls, "Restart:"+name)
	return nil
}

func (f *FakeServiceRunner) DisableAndStop(name string) error {
	f.Calls = append(f.Calls, "DisableAndStop:"+name)
	return nil
}

func (f *FakeServiceRunner) WriteUnitFile(path string, content []byte) error {
	f.Calls = append(f.Calls, "WriteUnitFile:"+path)
	if f.Units == nil {
		f.Units = make(map[string][]byte)
	}
	f.Units[path] = content
	return nil
}

func (f *FakeServiceRunner) RemoveUnitFile(path string) error {
	f.Calls = append(f.Calls, "RemoveUnitFile:"+path)
	delete(f.Units, path)
	return nil
}

func TestInstallService_LingerWarningAndCallSequence(t *testing.T) {
	tempDir := t.TempDir()
	fakeRunner := &FakeServiceRunner{LingerOk: false}
	env := Environment{
		HomeDir:       tempDir,
		ServiceRunner: fakeRunner,
	}

	binPath := filepath.Join(tempDir, "bin", "herdrx")
	cfgPath := filepath.Join(tempDir, "config", "config.json")

	res, err := InstallService(env, binPath, cfgPath)
	if err != nil {
		t.Fatalf("InstallService failed: %v", err)
	}

	// 1. 验证 Linger 告警
	if res.LingerEnabled {
		t.Fatalf("linger must be false")
	}
	if !strings.Contains(res.LingerWarning, "loginctl enable-linger") {
		t.Fatalf("expected linger warning with fix command, got: %s", res.LingerWarning)
	}

	// 2. 验证调用顺序: WriteUnitFile -> DaemonReload -> EnableAndStart
	expectedSuffixes := []string{"WriteUnitFile", "DaemonReload", "EnableAndStart:herdrx.service"}
	callIdx := 0
	for _, call := range fakeRunner.Calls {
		if strings.HasPrefix(call, "CheckLinger") {
			continue
		}
		if !strings.Contains(call, expectedSuffixes[callIdx]) {
			t.Fatalf("unexpected call sequence at step %d: got %s, want to contain %s", callIdx, call, expectedSuffixes[callIdx])
		}
		callIdx++
	}

	// 3. 验证 Unit 文件内容安全性与完整性
	unitBytes := fakeRunner.Units[res.UnitPath]
	unitStr := string(unitBytes)
	for _, expectedSnippet := range []string{
		`ExecStart="` + binPath + `" serve --config "` + cfgPath + `"`,
		"Restart=always",
		"RestartSec=3",
		"NoNewPrivileges=true",
		`Environment="PATH=`,
	} {
		if !strings.Contains(unitStr, expectedSnippet) {
			t.Fatalf("unit file missing expected snippet: %s", expectedSnippet)
		}
	}

	// 4. 验证卸载流程
	if err := UninstallService(env); err != nil {
		t.Fatalf("UninstallService failed: %v", err)
	}
	lastCalls := fakeRunner.Calls[len(fakeRunner.Calls)-3:]
	if !strings.Contains(lastCalls[0], "DisableAndStop:herdrx.service") ||
		!strings.Contains(lastCalls[1], "RemoveUnitFile") ||
		!strings.Contains(lastCalls[2], "DaemonReload") {
		t.Fatalf("unexpected uninstall call sequence: %v", lastCalls)
	}
}

func TestInstallService_LingerOK(t *testing.T) {
	tempDir := t.TempDir()
	fakeRunner := &FakeServiceRunner{LingerOk: true}
	env := Environment{
		HomeDir:       tempDir,
		ServiceRunner: fakeRunner,
	}

	res, err := InstallService(env, filepath.Join(tempDir, "bin"), filepath.Join(tempDir, "cfg"))
	if err != nil {
		t.Fatalf("InstallService failed: %v", err)
	}
	if !res.LingerEnabled || res.LingerWarning != "" {
		t.Fatalf("expected linger ok and no warning, got: %+v", res)
	}
}

func TestServiceCommand_StopRestartUninstall(t *testing.T) {
	tempDir := t.TempDir()
	fakeRunner := &FakeServiceRunner{}
	env := Environment{
		HomeDir:       tempDir,
		ServiceRunner: fakeRunner,
	}

	var stdout, stderr bytes.Buffer

	// 1. stop
	code := runService([]string{"stop"}, &stdout, &stderr, env, "")
	if code != 0 {
		t.Fatalf("runService stop failed: %s", stderr.String())
	}
	if len(fakeRunner.Calls) == 0 || !strings.Contains(fakeRunner.Calls[len(fakeRunner.Calls)-1], "Stop:herdrx.service") {
		t.Fatalf("expected runner Stop call, got: %v", fakeRunner.Calls)
	}

	// 2. restart
	code = runService([]string{"restart"}, &stdout, &stderr, env, "")
	if code != 0 {
		t.Fatalf("runService restart failed: %s", stderr.String())
	}
	if len(fakeRunner.Calls) == 0 || !strings.Contains(fakeRunner.Calls[len(fakeRunner.Calls)-1], "Restart:herdrx.service") {
		t.Fatalf("expected runner Restart call, got: %v", fakeRunner.Calls)
	}

	// 3. uninstall
	code = runService([]string{"uninstall"}, &stdout, &stderr, env, "")
	if code != 0 {
		t.Fatalf("runService uninstall failed: %s", stderr.String())
	}
}

func TestServiceUsesStableSymlinkAndXDGPaths(t *testing.T) {
	dir := t.TempDir()
	realBinary := filepath.Join(dir, "releases", "v0.1.0", "herdrx")
	stable := filepath.Join(dir, ".local", "bin", "herdrx")
	for _, path := range []string{filepath.Dir(realBinary), filepath.Dir(stable)} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(realBinary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realBinary, stable); err != nil {
		t.Fatal(err)
	}
	runner := &FakeServiceRunner{}
	env := Environment{HomeDir: dir, ConfigDir: filepath.Join(dir, "xdg-config", "herdrx"), RuntimeDir: filepath.Join(dir, "xdg-state", "run"), ServiceRunner: runner}
	config := filepath.Join(env.ConfigDir, "config.json")
	result, err := InstallService(env, realBinary, config)
	if err != nil {
		t.Fatal(err)
	}
	if result.UnitPath != filepath.Join(dir, "xdg-config", "systemd", "user", "herdrx.service") {
		t.Fatal(result.UnitPath)
	}
	unit := string(runner.Units[result.UnitPath])
	if !strings.Contains(unit, "ExecStart="+systemdArgument(stable)+" serve") || strings.Contains(unit, realBinary) || !strings.Contains(unit, "--runtime-dir "+systemdArgument(env.RuntimeDir)) {
		t.Fatal("unit pinned an immutable binary or lost runtime override:", unit)
	}
}
