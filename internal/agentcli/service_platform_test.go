package agentcli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLaunchdPlistIsValidAndPinsGeneratedFields 验证生成的 plist 结构合法、
// 参数逐项独立且带上运行目录。plutil 在 macOS 上是权威解析器，能抓住手写
// XML 的语法错误；非 macOS 平台只校验内容。
func TestLaunchdPlistIsValidAndPinsGeneratedFields(t *testing.T) {
	binary := "/Users/tester/.local/bin/herdrx"
	config := "/Users/tester/Library/Application Support/herdrx/config.json"
	runtimeDir := "/Users/tester/.local/state/herdrx/run"
	plist := GenerateLaunchdPlist(binary, config, runtimeDir)

	for _, want := range []string{
		launchdPlistMarker,
		"<string>" + binary + "</string>",
		"<string>serve</string>",
		"<string>--config</string>",
		"<string>--runtime-dir</string>",
		"<string>" + runtimeDir + "</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("generated plist is missing %q:\n%s", want, plist)
		}
	}
	// 配置路径含空格，必须作为单个 <string> 出现，不能被拆成两个参数。
	if !strings.Contains(plist, "<string>"+config+"</string>") {
		t.Fatalf("config path was not kept as one argument:\n%s", plist)
	}
	if runtime.GOOS != "darwin" {
		return
	}
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil unavailable")
	}
	path := filepath.Join(t.TempDir(), "probe.plist")
	if err := os.WriteFile(path, []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil rejected the generated plist: %v\n%s\n%s", err, out, plist)
	}
}

// TestLaunchdPlistEscapesXMLMetacharacters 确认路径中的 & < > 被转义，
// 否则 plist 会解析失败而服务静默起不来。
func TestLaunchdPlistEscapesXMLMetacharacters(t *testing.T) {
	plist := GenerateLaunchdPlist("/Users/a&b/bin/herdrx", "/Users/a&b/<cfg>/config.json", "")
	if strings.Contains(plist, "a&b") || strings.Contains(plist, "<cfg>") {
		t.Fatalf("XML metacharacters were not escaped:\n%s", plist)
	}
	for _, want := range []string{"a&amp;b", "&lt;cfg&gt;"} {
		if !strings.Contains(plist, want) {
			t.Fatalf("expected escaped %q in:\n%s", want, plist)
		}
	}
	if runtime.GOOS != "darwin" {
		return
	}
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil unavailable")
	}
	path := filepath.Join(t.TempDir(), "escaped.plist")
	if err := os.WriteFile(path, []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("escaped plist is not valid: %v\n%s", err, out)
	}
}

// TestLaunchdPlistOmitsRuntimeDirWhenUnset 未指定运行目录时不应出现空参数。
func TestLaunchdPlistOmitsRuntimeDirWhenUnset(t *testing.T) {
	plist := GenerateLaunchdPlist("/bin/herdrx", "/cfg/config.json", "")
	if strings.Contains(plist, "--runtime-dir") {
		t.Fatalf("unset runtime dir must not appear:\n%s", plist)
	}
	if strings.Contains(plist, "<string></string>") {
		t.Fatalf("empty argument leaked into ProgramArguments:\n%s", plist)
	}
}

// TestServiceLayoutSeparatesPlatforms 锁定两个平台各自的标识、单元位置与
// 生成物判据，并确认互不误判——把 systemd 单元当成 launchd 的生成物会导致
// 覆盖用户文件。两个方向都在任意 CI 机器上可测。
func TestServiceLayoutSeparatesPlatforms(t *testing.T) {
	home := "/home/tester"
	env := Environment{HomeDir: home, ConfigDir: filepath.Join(home, ".config", "herdrx"), RuntimeDir: filepath.Join(home, "run")}

	linux := layoutFor("linux", env)
	if linux.Label != "herdrx.service" {
		t.Fatal(linux.Label)
	}
	if linux.UnitPath != filepath.Join(home, ".config", "systemd", "user", "herdrx.service") {
		t.Fatal(linux.UnitPath)
	}

	mac := layoutFor("darwin", env)
	if mac.Label != launchdLabel {
		t.Fatal(mac.Label)
	}
	if mac.UnitPath != filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist") {
		t.Fatal(mac.UnitPath)
	}

	config := filepath.Join(env.ConfigDir, "config.json")
	linuxUnit := linux.Content("/bin/herdrx", config, env.RuntimeDir)
	macUnit := mac.Content("/bin/herdrx", config, env.RuntimeDir)
	if strings.Contains(linuxUnit, mac.Marker) {
		t.Fatal("systemd unit must not satisfy the launchd generated-file marker")
	}
	if strings.Contains(macUnit, linux.Marker) {
		t.Fatal("launchd plist must not satisfy the systemd generated-file marker")
	}
	if !strings.Contains(linuxUnit, linux.ConfigReference(config)) {
		t.Fatal("systemd unit lost its own config reference")
	}
	if !strings.Contains(macUnit, mac.ConfigReference(config)) {
		t.Fatal("launchd plist lost its own config reference")
	}
	// 不同平台的 config 引用格式不能互相匹配，否则 update 的身份校验会失效。
	if strings.Contains(macUnit, linux.ConfigReference(config)) || strings.Contains(linuxUnit, mac.ConfigReference(config)) {
		t.Fatal("config reference is not platform specific")
	}
}

// TestInstallServiceUsesLaunchdLayoutOnDarwin 验证 macOS 上 InstallService
// 写的是 LaunchAgent plist、用 launchd 标签启动，而不是 systemd 单元。
func TestInstallServiceUsesLaunchdLayoutOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only service layout")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "herdrx")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &FakeServiceRunner{LingerOk: true}
	env := Environment{
		HomeDir:       dir,
		ConfigDir:     filepath.Join(dir, "Library", "Application Support", "herdrx"),
		RuntimeDir:    filepath.Join(dir, "run"),
		ServiceRunner: runner,
	}
	config := filepath.Join(env.ConfigDir, "config.json")
	result, err := InstallService(env, binary, config)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "Library", "LaunchAgents", launchdLabel+".plist")
	if result.UnitPath != want {
		t.Fatalf("expected LaunchAgent at %s, got %s", want, result.UnitPath)
	}
	unit := string(runner.Units[result.UnitPath])
	if !strings.Contains(unit, launchdPlistMarker) || strings.Contains(unit, "ExecStart=") {
		t.Fatalf("expected a launchd plist, got:\n%s", unit)
	}
	var started bool
	for _, call := range runner.Calls {
		if call == "EnableAndStart:"+launchdLabel {
			started = true
		}
		if strings.Contains(call, "herdrx.service") {
			t.Fatalf("systemd label leaked into darwin install: %v", runner.Calls)
		}
	}
	if !started {
		t.Fatalf("service was not started with the launchd label: %v", runner.Calls)
	}
}

// TestInstallServiceRefusesForeignUnit 确认已有的自定义单元不会被覆盖。
func TestInstallServiceRefusesForeignUnit(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "herdrx")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := Environment{
		HomeDir:       dir,
		ConfigDir:     filepath.Join(dir, "config", "herdrx"),
		RuntimeDir:    filepath.Join(dir, "run"),
		ServiceRunner: &FakeServiceRunner{LingerOk: true},
	}
	unitPath := layoutFor(runtime.GOOS, env).UnitPath
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("hand written by the operator\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallService(env, binary, filepath.Join(env.ConfigDir, "config.json")); err == nil {
		t.Fatal("expected refusal to overwrite a custom unit")
	}
}
