package agentcli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLaunchdServiceRunnerAgainstRealLaunchd 用真实的 launchctl 走一遍
// bootstrap → kickstart → 查询 → bootout，确认生成的 plist 能被 launchd 接受、
// 进程真的被拉起、幂等重复执行不报错、卸载后确实消失。
//
// 只在 macOS 且 gui 域可用时运行：CI 的 Linux runner 会跳过，纯 SSH 登录的 Mac
// 也会跳过（那里没有 Aqua 会话，正是 setup 会告警的场景）。
//
// 用独立的 HOME 和独立标签，绝不碰这台机器上真实的 com.riba2534.herdrx 服务。
func TestLaunchdServiceRunnerAgainstRealLaunchd(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd is macOS-only")
	}
	if _, err := exec.LookPath("launchctl"); err != nil {
		t.Skip("launchctl unavailable")
	}
	if ok, err := (&LaunchdServiceRunner{}).CheckLinger(""); err != nil || !ok {
		t.Skipf("launchd gui domain unavailable (no desktop login session): ok=%v err=%v", ok, err)
	}

	home := shortTempDir(t)
	t.Setenv("HOME", home)
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o700); err != nil {
		t.Fatal(err)
	}

	// 独立标签，避免与真实服务同名；同时确认测试自己不会留下残留。
	label := "com.riba2534.herdrx-selftest"
	marker := filepath.Join(home, "ran.txt")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + label + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/sh</string>
    <string>-c</string>
    <string>echo running &gt;&gt; ` + xmlEscapeText(marker) + `; sleep 30</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <false/>
  <key>ProcessType</key>
  <string>Background</string>
</dict>
</plist>
`
	path := filepath.Join(agents, label+".plist")
	runner := &LaunchdServiceRunner{}
	if err := runner.WriteUnitFile(path, []byte(plist)); err != nil {
		t.Fatal(err)
	}
	// launchctl 以自己的权限读取 plist；0600 + 独立目录已足够，这里确认可读。
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = runLaunchctl(20*time.Second, "bootout", launchdTarget(label))
	})

	if err := runner.EnableAndStart(label); err != nil {
		t.Fatalf("EnableAndStart against real launchd: %v", err)
	}

	// 进程确实被拉起：等 marker 文件出现，而不是只看 launchctl 的退出码。
	deadline := time.Now().Add(15 * time.Second)
	var ran bool
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil && strings.Contains(string(data), "running") {
			ran = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ran {
		out, _ := runLaunchctl(5*time.Second, "print", launchdTarget(label))
		t.Fatalf("service was loaded but never executed; launchctl print:\n%s", out)
	}

	active, err := runner.IsActive(label)
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if !active {
		t.Fatal("expected the freshly started service to report active")
	}

	// 幂等：EnableAndStart 再来一次不能因为 already loaded 而失败。
	if err := runner.EnableAndStart(label); err != nil {
		t.Fatalf("EnableAndStart must be idempotent: %v", err)
	}
	if err := runner.Restart(label); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	if err := runner.DisableAndStop(label); err != nil {
		t.Fatalf("DisableAndStop: %v", err)
	}
	if active, err := runner.IsActive(label); err != nil || active {
		t.Fatalf("service still active after stop (active=%v, err=%v)", active, err)
	}
	// 已经停掉后再 Stop 必须是无操作而不是报错，否则 uninstall 无法重跑。
	if err := runner.DisableAndStop(label); err != nil {
		t.Fatalf("stopping an already-stopped service must be a no-op: %v", err)
	}
}

// TestLaunchdRestartLoadsWhenNotYetBootstrapped 服务尚未加载时 Restart 应回退到
// 完整加载路径，而不是因为 kickstart 找不到对象而失败。
func TestLaunchdRestartFallsBackWhenNotLoaded(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd is macOS-only")
	}
	if _, err := exec.LookPath("launchctl"); err != nil {
		t.Skip("launchctl unavailable")
	}
	// 一个绝不存在的标签：kickstart 必然报 "Could not find service"，
	// launchdNotLoaded 必须认出它，Restart 才会走 EnableAndStart。
	_, err := runLaunchctl(5*time.Second, "kickstart", launchdTarget("com.riba2534.herdrx-absent-"+t.Name()))
	if err == nil {
		t.Skip("unexpectedly found the placeholder service")
	}
	if !launchdNotLoaded(err) {
		t.Fatalf("launchdNotLoaded failed to classify a missing service: %v", err)
	}
}
