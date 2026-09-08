package agentcli

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type ServiceRunner interface {
	CheckLinger(username string) (bool, error)
	DaemonReload() error
	EnableAndStart(serviceName string) error
	Stop(serviceName string) error
	Restart(serviceName string) error
	DisableAndStop(serviceName string) error
	WriteUnitFile(path string, content []byte) error
	RemoveUnitFile(path string) error
}

// serviceLayout 描述单个平台的服务定义：标识、单元位置与「本单元由 herdrx 生成」
// 的判据。平台差异集中在这里，InstallService 等流程不再直接假设 systemd。
type serviceLayout struct {
	GOOS     string
	Label    string
	UnitPath string
	// Marker 用于区分自动生成与用户自定义的单元；缺少它一律拒绝覆盖。
	Marker string
	// LingerHint 为空表示该平台不需要 linger 之类的登出保活设置。
	LingerHint func(username string) string
	// KeepAliveOK 是保活已就绪时展示给用户的说明。
	KeepAliveOK string
	// KeepAliveCheck 提示用户如何自查保活设置。
	KeepAliveCheck string
}

const (
	systemdUnitMarker = "Description=herdrx remote access agent"
	launchdLabel      = "com.riba2534.herdrx"
	// launchdPlistMarker 同时出现在生成的 plist 中，作为生成物判据。
	launchdPlistMarker = "<string>" + launchdLabel + "</string>"
)

// layoutFor 按目标平台给出服务布局。goos 显式传入而非直接读 runtime.GOOS，
// 这样两个平台的单元内容和路径都能在任意一种 CI 机器上被测试覆盖。
func layoutFor(goos string, env Environment) serviceLayout {
	switch goos {
	case "darwin":
		return serviceLayout{
			GOOS:     goos,
			Label:    launchdLabel,
			UnitPath: filepath.Join(env.HomeDir, "Library", "LaunchAgents", launchdLabel+".plist"),
			Marker:   launchdPlistMarker,
			// LaunchAgent 随图形登录启动；纯 SSH 登录的 Mac 没有 Aqua 会话，
			// 需要用户至少登录一次桌面，launchd 才会加载 gui 域。
			LingerHint: func(string) string {
				return "当前用户没有活动的图形登录会话，launchd 的 gui 域不可用，后台服务不会随登录自动启动。请在这台 Mac 上登录一次桌面后重新执行 herdrx setup"
			},
			KeepAliveOK:    "登录后自动启动: 已启用 (launchd LaunchAgent 已加载)",
			KeepAliveCheck: "登录后自动启动请按安装引导检查 launchd LaunchAgent",
		}
	default:
		return serviceLayout{
			GOOS:     goos,
			Label:    "herdrx.service",
			UnitPath: systemdUnitPath(env),
			Marker:   systemdUnitMarker,
			LingerHint: func(username string) string {
				return fmt.Sprintf("当前用户未开启 loginctl linger，SSH 登出后后台服务可能会被系统终止。建议执行: loginctl enable-linger %s", username)
			},
			KeepAliveOK:    "开机与登出保活: 已启用 (loginctl linger ok)",
			KeepAliveCheck: "开机与登出保活请按安装引导检查 loginctl linger",
		}
	}
}

// Content 生成该平台的服务单元内容。
func (l serviceLayout) Content(binaryPath, configPath, runtimeDir string) string {
	if l.GOOS == "darwin" {
		return GenerateLaunchdPlist(binaryPath, configPath, runtimeDir)
	}
	return GenerateSystemdUnit(binaryPath, configPath, runtimeDir)
}

// ConfigReference 返回单元内容中引用该配置路径时应出现的片段，用于确认
// 待修复的单元指向同一身份。两个平台的引号/转义规则不同，所以由布局给出。
func (l serviceLayout) ConfigReference(configPath string) string {
	if l.GOOS == "darwin" {
		return "<string>" + xmlEscapeText(configPath) + "</string>"
	}
	return " --config " + systemdArgument(configPath)
}

type RealServiceRunner struct{}

// NewRealServiceRunner 按运行平台选择服务管理器实现。
func NewRealServiceRunner() ServiceRunner {
	if runtime.GOOS == "darwin" {
		return &LaunchdServiceRunner{}
	}
	return &RealServiceRunner{}
}

func (r *RealServiceRunner) CheckLinger(username string) (bool, error) {
	out, err := RunBoundedCommand(context.Background(), 5*time.Second, 64<<10, "loginctl", "show-user", username, "--property=Linger")
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), "Linger=yes"), nil
}

func (r *RealServiceRunner) DaemonReload() error {
	return runSystemctl("daemon-reload")
}

func (r *RealServiceRunner) EnableAndStart(serviceName string) error {
	return runSystemctl("enable", "--now", serviceName)
}

func (r *RealServiceRunner) Stop(serviceName string) error {
	return runSystemctl("stop", serviceName)
}

func (r *RealServiceRunner) Restart(serviceName string) error {
	return runSystemctl("restart", serviceName)
}

func (r *RealServiceRunner) DisableAndStop(serviceName string) error {
	return runSystemctl("disable", "--now", serviceName)
}

func (r *RealServiceRunner) IsActive(serviceName string) (bool, error) {
	out, err := RunBoundedCommand(context.Background(), 5*time.Second, 64<<10, "systemctl", "--user", "show", "--property=LoadState,ActiveState", serviceName)
	if err != nil {
		return false, fmt.Errorf("inspect legacy service: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "ActiveState=") {
			state := strings.TrimPrefix(line, "ActiveState=")
			return state != "inactive" && state != "failed", nil
		}
	}
	return false, errors.New("systemctl did not report the legacy service state")
}

func runSystemctl(args ...string) error {
	out, err := RunBoundedCommand(context.Background(), 35*time.Second, 64<<10, "systemctl", append([]string{"--user"}, args...)...)
	if err != nil {
		return fmt.Errorf("systemctl: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *RealServiceRunner) WriteUnitFile(path string, content []byte) error {
	cleanPath := filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cleanPath), ".unit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, cleanPath); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(cleanPath))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (r *RealServiceRunner) RemoveUnitFile(path string) error {
	cleanPath := filepath.Clean(path)
	err := os.Remove(cleanPath)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// LaunchdServiceRunner 用 launchctl 管理 macOS 的 per-user LaunchAgent。
// 沿用旧版受控端已有的 gui/<uid> 域与 com.riba2534.herdrx-* 标签约定。
type LaunchdServiceRunner struct{}

func launchdDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func launchdTarget(label string) string { return launchdDomain() + "/" + label }

func runLaunchctl(timeout time.Duration, args ...string) ([]byte, error) {
	return runLaunchctlLimited(timeout, 64<<10, args...)
}

func runLaunchctlLimited(timeout time.Duration, maxBytes int64, args ...string) ([]byte, error) {
	out, err := RunBoundedCommand(context.Background(), timeout, maxBytes, "launchctl", args...)
	if err != nil {
		return out, fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// CheckLinger 在 macOS 上检查 gui 域是否可用。launchd 的 LaunchAgent 依附图形
// 登录会话：纯 SSH 登录的机器没有 Aqua 会话，bootstrap 会失败，行为上等价于
// Linux 缺少 linger —— 登出/未登录时后台服务不存活，所以复用同一个告警位。
//
// 不能用 `launchctl print gui/<uid>` 判断：它会把域里所有服务全部打印出来，
// 正常 Mac 上轻易超过 10 万字节，撞上输出上限后会被误判成「没有图形会话」，
// 于是每台健康的 Mac 都收到一条错误告警。`launchctl managername` 只输出一行
// （有图形会话时为 Aqua），是这里真正要问的问题。
func (r *LaunchdServiceRunner) CheckLinger(username string) (bool, error) {
	out, err := runLaunchctl(5*time.Second, "managername")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "Aqua", nil
}

// DaemonReload launchd 没有全局 reload：plist 的变化在 bootout+bootstrap 时生效，
// 由 EnableAndStart 与 Restart 负责，这里无需动作。
func (r *LaunchdServiceRunner) DaemonReload() error { return nil }

func (r *LaunchdServiceRunner) EnableAndStart(serviceName string) error {
	plist := launchdPlistPathForLabel(serviceName)
	// 已加载时 bootstrap 会因 EEXIST 失败，先无条件 bootout 使其幂等。
	_, _ = runLaunchctl(20*time.Second, "bootout", launchdTarget(serviceName))
	if _, err := runLaunchctl(35*time.Second, "bootstrap", launchdDomain(), plist); err != nil {
		return err
	}
	// bootstrap 只保证已加载；RunAtLoad 之外显式 kickstart 一次，让 setup 之后
	// 立刻有进程，与 systemctl enable --now 的语义对齐。
	_, err := runLaunchctl(35*time.Second, "kickstart", launchdTarget(serviceName))
	return err
}

func (r *LaunchdServiceRunner) Stop(serviceName string) error {
	_, err := runLaunchctl(35*time.Second, "bootout", launchdTarget(serviceName))
	if err != nil && launchdNotLoaded(err) {
		return nil
	}
	return err
}

func (r *LaunchdServiceRunner) Restart(serviceName string) error {
	// -k 先杀掉正在运行的实例再拉起，等价于 systemctl restart。
	_, err := runLaunchctl(35*time.Second, "kickstart", "-k", launchdTarget(serviceName))
	if err == nil {
		return nil
	}
	// 服务尚未加载时 kickstart 无对象可操作，退回完整加载路径。
	if launchdNotLoaded(err) {
		return r.EnableAndStart(serviceName)
	}
	return err
}

func (r *LaunchdServiceRunner) DisableAndStop(serviceName string) error {
	return r.Stop(serviceName)
}

func (r *LaunchdServiceRunner) IsActive(serviceName string) (bool, error) {
	// 单个服务的 print 通常 3KB 左右，但环境变量多时会明显变大；给足上限，
	// 避免「输出超限」被下面的 launchdNotLoaded 误判成「服务不存在」。
	out, err := runLaunchctlLimited(5*time.Second, 1<<20, "print", launchdTarget(serviceName))
	if err != nil {
		if launchdNotLoaded(err) {
			return false, nil
		}
		return false, err
	}
	// 已加载但没有 pid 说明进程不在运行（例如 KeepAlive 退避中）。
	return strings.Contains(string(out), "pid = "), nil
}

func (r *LaunchdServiceRunner) WriteUnitFile(path string, content []byte) error {
	return (&RealServiceRunner{}).WriteUnitFile(path, content)
}

func (r *LaunchdServiceRunner) RemoveUnitFile(path string) error {
	return (&RealServiceRunner{}).RemoveUnitFile(path)
}

// launchdNotLoaded 判断错误是否为「服务未加载」。launchctl 用 113
// (Could not find service) / 3 (No such process) 表达这一类状态。
func launchdNotLoaded(err error) bool {
	text := err.Error()
	return strings.Contains(text, "Could not find service") ||
		strings.Contains(text, "No such process") ||
		strings.Contains(text, "not find specified service")
}

func launchdPlistPathForLabel(label string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("Library", "LaunchAgents", label+".plist")
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

// launchdLogPath 是 LaunchAgent 的 stdout/stderr 落盘位置，也是 herdrx logs
// 在 macOS 上读取的文件；两处必须一致，所以只在这里定义。
func launchdLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("Library", "Logs", "herdrx.log")
	}
	return filepath.Join(home, "Library", "Logs", "herdrx.log")
}

type InstallResult struct {
	UnitPath      string `json:"unit_path"`
	LingerEnabled bool   `json:"linger_enabled"`
	LingerWarning string `json:"linger_warning,omitempty"`
}

// GenerateSystemdUnit 构造固定绝对路径与环境变量注入的 systemd user unit 文件
func GenerateSystemdUnit(binaryPath, configPath string, runtimeDirs ...string) string {
	currentPath := os.Getenv("PATH")
	command := systemdArgument(binaryPath) + " serve --config " + systemdArgument(configPath)
	if len(runtimeDirs) > 0 && runtimeDirs[0] != "" {
		command += " --runtime-dir " + systemdArgument(runtimeDirs[0])
	}
	return `[Unit]
Description=herdrx remote access agent
After=network.target

[Service]
Type=simple
ExecStart=` + command + `
Restart=always
RestartSec=3
TimeoutStopSec=10
NoNewPrivileges=true
Environment=` + strconv.Quote(strings.ReplaceAll("PATH="+currentPath, "%", "%%")) + `

[Install]
WantedBy=default.target
`
}

func systemdArgument(value string) string {
	return strconv.Quote(strings.NewReplacer("%", "%%", "$", "$$").Replace(value))
}

// GenerateLaunchdPlist 构造 per-user LaunchAgent，语义对齐 systemd 单元：
// 固定绝对路径、注入 PATH、崩溃后自动重启。
func GenerateLaunchdPlist(binaryPath, configPath string, runtimeDirs ...string) string {
	arguments := []string{binaryPath, "serve", "--config", configPath}
	if len(runtimeDirs) > 0 && runtimeDirs[0] != "" {
		arguments = append(arguments, "--runtime-dir", runtimeDirs[0])
	}
	var program strings.Builder
	for _, argument := range arguments {
		program.WriteString("\n    <string>" + xmlEscapeText(argument) + "</string>")
	}
	logPath := launchdLogPath()
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  ` + launchdPlistMarker + `
  <key>ProgramArguments</key>
  <array>` + program.String() + `
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>3</integer>
  <key>ProcessType</key>
  <string>Background</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>` + xmlEscapeText(os.Getenv("PATH")) + `</string>
  </dict>
  <key>StandardOutPath</key>
  <string>` + xmlEscapeText(logPath) + `</string>
  <key>StandardErrorPath</key>
  <string>` + xmlEscapeText(logPath) + `</string>
</dict>
</plist>
`
}

// xmlEscapeText 转义 plist 文本节点。路径由用户目录和配置派生，可能含 & < >。
func xmlEscapeText(value string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(value)); err != nil {
		return ""
	}
	return buf.String()
}

func systemdUnitPath(env Environment) string {
	root := filepath.Join(env.HomeDir, ".config")
	if env.ConfigDir != "" {
		root = filepath.Dir(env.ConfigDir)
	}
	return filepath.Join(root, "systemd", "user", "herdrx.service")
}

func serviceUnitPath(env Environment) string {
	return layoutFor(runtime.GOOS, env).UnitPath
}

// os.Executable resolves symlinks on Linux and macOS alike. Prefer the stable
// entrypoint that actually refers to this executable, so subsequent service
// restarts upgrade.
func resolveServiceExecutable(env Environment, executable string) string {
	actual, err := os.Stat(executable)
	if err != nil {
		return executable
	}
	candidates := []string{filepath.Join(env.HomeDir, ".local", "bin", "herdrx")}
	if len(os.Args) > 0 {
		invoked := os.Args[0]
		if !strings.ContainsRune(invoked, os.PathSeparator) {
			if path, err := exec.LookPath(invoked); err == nil {
				invoked = path
			}
		}
		if path, err := filepath.Abs(invoked); err == nil {
			candidates = append(candidates, path)
		}
	}
	for _, candidate := range candidates {
		if fi, err := os.Stat(candidate); err == nil && os.SameFile(actual, fi) {
			return candidate
		}
	}
	return executable
}

func InstallService(env Environment, binaryPath, configPath string) (*InstallResult, error) {
	runner := env.ServiceRunner
	if runner == nil {
		runner = NewRealServiceRunner()
	}

	absBin, err := filepath.Abs(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve binary path: %w", err)
	}
	absCfg, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	if env.RuntimeDir != "" {
		env.RuntimeDir, err = filepath.Abs(env.RuntimeDir)
		if err != nil {
			return nil, fmt.Errorf("resolve runtime path: %w", err)
		}
	}

	// 检查后台服务在用户登出后能否存活：Linux 看 loginctl linger，
	// macOS 看 launchd 的 gui 域是否可用。
	layout := layoutFor(runtime.GOOS, env)
	u, err := user.Current()
	var lingerOk bool
	if err == nil {
		lingerOk, _ = runner.CheckLinger(u.Username)
	}

	var warning string
	if !lingerOk && layout.LingerHint != nil {
		username := "your-user"
		if u != nil {
			username = u.Username
		}
		warning = layout.LingerHint(username)
	}

	absBin = resolveServiceExecutable(env, absBin)
	unitContent := layout.Content(absBin, absCfg, env.RuntimeDir)
	unitPath := layout.UnitPath
	unitChanged := false
	if existing, err := os.ReadFile(unitPath); err == nil {
		if !strings.Contains(string(existing), layout.Marker) {
			return nil, fmt.Errorf("refusing to overwrite custom unit at %s", unitPath)
		}
		unitChanged = string(existing) != unitContent
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read service unit: %w", err)
	}

	if err := runner.WriteUnitFile(unitPath, []byte(unitContent)); err != nil {
		return nil, fmt.Errorf("write service unit: %w", err)
	}
	if err := runner.DaemonReload(); err != nil {
		return nil, fmt.Errorf("reload service manager: %w", err)
	}
	if err := runner.EnableAndStart(layout.Label); err != nil {
		return nil, fmt.Errorf("enable and start service: %w", err)
	}
	if unitChanged {
		if err := runner.Restart(layout.Label); err != nil {
			return nil, fmt.Errorf("restart changed unit: %w", err)
		}
	}

	result := &InstallResult{
		UnitPath:      unitPath,
		LingerEnabled: lingerOk,
		LingerWarning: warning,
	}
	if isRealServiceRunner(runner) {
		sock := filepath.Join(env.RuntimeDir, "control.sock")
		var status Readiness
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		callErr := clientCallIPCContext(ctx, sock, "GET", "/ready", absCfg, nil, &status)
		cancel()
		if !unitChanged && (callErr != nil || status.Version != Version) {
			if err := runner.Restart(layout.Label); err != nil {
				return result, fmt.Errorf("restart installed binary: %w", err)
			}
		}
		cfg, err := SafeLoadConfig(absCfg)
		if err != nil {
			return result, err
		}
		expected, err := readiness(&cfg, absCfg)
		if err != nil {
			return result, err
		}
		if err := waitForReadiness(env, expected, absCfg, 30*time.Second); err != nil {
			return result, err
		}
	}
	return result, nil
}

func UninstallService(env Environment) error {
	runner := env.ServiceRunner
	if runner == nil {
		runner = NewRealServiceRunner()
	}

	layout := layoutFor(runtime.GOOS, env)
	if err := runner.DisableAndStop(layout.Label); err != nil {
		return fmt.Errorf("stop and disable service: %w", err)
	}
	if err := runner.RemoveUnitFile(layout.UnitPath); err != nil {
		return fmt.Errorf("remove unit file: %w", err)
	}
	if err := runner.DaemonReload(); err != nil {
		return fmt.Errorf("reload service manager after uninstall: %w", err)
	}
	return nil
}

// isRealServiceRunner 判断是否为真实的平台服务管理器（而非测试替身），
// 只有这时才需要等待守护进程真正就绪。
func isRealServiceRunner(runner ServiceRunner) bool {
	switch runner.(type) {
	case *RealServiceRunner, *LaunchdServiceRunner:
		return true
	default:
		return false
	}
}

// ServiceLabel 返回当前平台的服务标识，供命令行提示与服务管理子命令使用。
func ServiceLabel(env Environment) string {
	return layoutFor(runtime.GOOS, env).Label
}
