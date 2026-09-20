package agentcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type PreflightStatus string

const (
	PreflightOK           PreflightStatus = "ok"
	PreflightMissing      PreflightStatus = "missing"
	PreflightNotRunning   PreflightStatus = "not_running"
	PreflightIncompatible PreflightStatus = "incompatible"
	PreflightPermission   PreflightStatus = "permission"
)

type PreflightResult struct {
	Status                PreflightStatus `json:"status"`
	HerdrPath             string          `json:"herdr_path,omitempty"`
	Version               string          `json:"version,omitempty"`
	CLIProtocol           int             `json:"cli_protocol,omitempty"`
	DaemonVersion         string          `json:"daemon_version,omitempty"`
	DaemonProtocol        int             `json:"daemon_protocol,omitempty"`
	DaemonVerified        bool            `json:"daemon_verified"`
	TerminalProtocolMatch *bool           `json:"terminal_protocol_match,omitempty"`
	ServerRunning         bool            `json:"server_running"`
	SocketPath            string          `json:"socket_path,omitempty"`
	Details               string          `json:"details"`
	Suggestion            string          `json:"suggestion,omitempty"`
}

var versionSemverRegex = regexp.MustCompile(`\b\d+\.\d+\.\d+\b`)

// RunPreflight 严格执行受控端 Herdr 真实可用性、绝对路径、版本能力与后台运行状态检测
func RunPreflight(env Environment) PreflightResult {
	binPath := env.HerdrBin
	if binPath == "" {
		if env.LookPath == nil {
			return PreflightResult{
				Status:     PreflightMissing,
				Details:    "未配置 PATH 查找器",
				Suggestion: "请参考官方文档安装 Herdr: https://herdr.dev/docs/install/",
			}
		}
		found, err := env.LookPath("herdr")
		if err != nil || found == "" {
			return PreflightResult{
				Status:     PreflightMissing,
				Details:    "系统 PATH 中未找到 herdr 程序",
				Suggestion: "请参考官方安装文档安装 Herdr: https://herdr.dev/docs/install/",
			}
		}
		binPath = found
	}

	absPath, err := filepath.Abs(binPath)
	if err != nil {
		return PreflightResult{
			Status:     PreflightMissing,
			Details:    fmt.Sprintf("无法解析 herdr 绝对路径: %v", err),
			Suggestion: "请使用 --herdr-bin 指定有效绝对路径",
		}
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PreflightResult{
				Status:     PreflightMissing,
				Details:    fmt.Sprintf("指定的可执行文件不存在: %s", absPath),
				Suggestion: "请确认文件存在或参考官方文档安装: https://herdr.dev/docs/install/",
			}
		}
		return PreflightResult{
			Status:     PreflightPermission,
			Details:    fmt.Sprintf("无法读取 herdr 文件状态: %v", err),
			Suggestion: "请检查该文件读取与执行权限",
		}
	}

	if info.IsDir() {
		return PreflightResult{
			Status:     PreflightMissing,
			Details:    fmt.Sprintf("指定的路径是一个目录: %s", absPath),
			Suggestion: "请指定有效的文件路径",
		}
	}

	// 检查当前用户是否有执行权限
	if info.Mode()&0o111 == 0 {
		return PreflightResult{
			Status:     PreflightPermission,
			HerdrPath:  absPath,
			Details:    fmt.Sprintf("文件无执行权限: %s", absPath),
			Suggestion: "请为该文件添加执行权限: chmod +x " + absPath,
		}
	}

	cmdRunner := env.CommandRunner
	if cmdRunner == nil {
		cmdRunner = func(name string, args ...string) ([]byte, error) {
			return RunBoundedCommand(context.Background(), 3*time.Second, 1<<20, name, args...)
		}
	}

	// 1. 检查版本输出（拒绝非 Herdr 或空版本输出）
	verOut, err := cmdRunner(absPath, "--version")
	if err != nil {
		return PreflightResult{
			Status:     PreflightPermission,
			HerdrPath:  absPath,
			Details:    fmt.Sprintf("执行 herdr --version 失败: %v", err),
			Suggestion: "请检查文件执行权限或确认程序架构是否匹配",
		}
	}
	verStr := strings.TrimSpace(string(verOut))
	if !strings.Contains(strings.ToLower(verStr), "herdr") || !versionSemverRegex.MatchString(verStr) {
		return PreflightResult{
			Status:     PreflightIncompatible,
			HerdrPath:  absPath,
			Version:    verStr,
			Details:    fmt.Sprintf("程序版本输出不符合 Herdr 语义: %q", verStr),
			Suggestion: "请确认该程序是否为真正的 Herdr 二进制 (https://herdr.dev/)",
		}
	}

	// 2. 检查 api schema JSON 及关键方法
	schemaOut, err := cmdRunner(absPath, "api", "schema", "--json")
	if err != nil || len(bytes.TrimSpace(schemaOut)) == 0 {
		return PreflightResult{
			Status:     PreflightIncompatible,
			HerdrPath:  absPath,
			Version:    verStr,
			Details:    fmt.Sprintf("执行 herdr api schema --json 失败或输出为空: %v", err),
			Suggestion: "请核对 Herdr 安装文件，并检查工作台的 Herdr 兼容性支持矩阵",
		}
	}

	var rootSchema struct {
		Protocol int `json:"protocol"`
		Schemas  struct {
			Request struct {
				OneOf []struct {
					Properties struct {
						Method struct {
							Const string `json:"const"`
						} `json:"method"`
					} `json:"properties"`
				} `json:"oneOf"`
			} `json:"request"`
		} `json:"schemas"`
	}

	if err := json.Unmarshal(schemaOut, &rootSchema); err != nil {
		return PreflightResult{
			Status:     PreflightIncompatible,
			HerdrPath:  absPath,
			Version:    verStr,
			Details:    fmt.Sprintf("herdr api schema JSON 格式无法解析: %v", err),
			Suggestion: "请升级 Herdr 至最新版本",
		}
	}

	// 收集 schema 中声明的全部 method
	availableMethods := make(map[string]bool)
	for _, one := range rootSchema.Schemas.Request.OneOf {
		if m := one.Properties.Method.Const; m != "" {
			availableMethods[m] = true
		}
	}

	// 必须包含核心方法之一
	requiredMethods := []string{"session.snapshot", "workspace.list", "tab.list", "ping"}
	hasRequired := false
	for _, reqM := range requiredMethods {
		if availableMethods[reqM] {
			hasRequired = true
			break
		}
	}
	if !hasRequired {
		return PreflightResult{
			Status:     PreflightIncompatible,
			HerdrPath:  absPath,
			Version:    verStr,
			Details:    "herdr api schema 缺少关键服务接口方法 (如 session.snapshot / workspace.list)",
			Suggestion: "请确认 Herdr 安装完整并升级至支持 API 的版本",
		}
	}

	// 3. 探活实际后台服务状态
	statusOut, err := cmdRunner(absPath, "status", "server")
	if err != nil {
		return PreflightResult{
			Status:        PreflightNotRunning,
			HerdrPath:     absPath,
			Version:       verStr,
			ServerRunning: false,
			Details:       fmt.Sprintf("herdr status server 报错: %v", err),
			Suggestion:    "请启动 Herdr 后台服务: herdr server",
		}
	}

	lines := strings.Split(string(statusOut), "\n")
	var statusVal, socketVal string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "status:") {
			statusVal = strings.TrimSpace(strings.TrimPrefix(l, "status:"))
		} else if strings.HasPrefix(l, "socket:") {
			socketVal = strings.TrimSpace(strings.TrimPrefix(l, "socket:"))
		}
	}

	if statusVal != "running" {
		return PreflightResult{
			Status:        PreflightNotRunning,
			HerdrPath:     absPath,
			Version:       verStr,
			ServerRunning: false,
			Details:       fmt.Sprintf("Herdr 服务未运行 (status: %s)", statusVal),
			Suggestion:    "请在后台启动服务: herdr server",
		}
	}

	if socketVal == "" {
		// 备用：检查默认 socket 路径
		socketVal = filepath.Join(env.HomeDir, ".config", "herdr", "herdr.sock")
	}

	// 真实连通性测试：必须成功 Dial 该 Unix socket，不能文件存在即健康
	conn, dialErr := net.DialTimeout("unix", socketVal, 1*time.Second)
	if dialErr != nil {
		return PreflightResult{
			Status:        PreflightNotRunning,
			HerdrPath:     absPath,
			Version:       verStr,
			ServerRunning: false,
			SocketPath:    socketVal,
			Details:       fmt.Sprintf("Herdr socket 无法连接探活: %v", dialErr),
			Suggestion:    "Herdr 服务可能异常中断，请重新启动: herdr server",
		}
	}
	defer conn.Close()
	result := PreflightResult{
		Status:        PreflightOK,
		HerdrPath:     absPath,
		Version:       verStr,
		CLIProtocol:   rootSchema.Protocol,
		ServerRunning: true,
		SocketPath:    socketVal,
		Details:       "Herdr 安装文件与 API socket 可用；尚未验证后台运行版本，请在工作台查看主机能力详情",
	}
	// The schema belongs to the installed executable. Only a read-only ping on
	// this exact socket can identify the daemon; `status` text is not substituted
	// when the daemon does not reply or omits optional fields.
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if err := json.NewEncoder(conn).Encode(map[string]any{"id": "herdrx-preflight", "method": "ping", "params": map[string]any{}}); err != nil {
		return result
	}
	var pong struct {
		Result struct {
			Version  string `json:"version"`
			Protocol int    `json:"protocol"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(conn, 64<<10)).Decode(&pong) != nil || (len(pong.Error) > 0 && string(pong.Error) != "null") || pong.Result.Version == "" || pong.Result.Protocol <= 0 {
		return result
	}
	result.DaemonVersion, result.DaemonProtocol, result.DaemonVerified = pong.Result.Version, pong.Result.Protocol, true
	result.Details = "Herdr 安装文件与后台运行版本已分别读取，功能兼容性由工作台按接口判断"
	if result.CLIProtocol > 0 {
		matches := result.CLIProtocol == result.DaemonProtocol
		result.TerminalProtocolMatch = &matches
		if !matches {
			// Pairing/JSON access remains useful even when a private terminal
			// handshake cannot work. The workbench gates that feature separately.
			result.Details = fmt.Sprintf("JSON 接口可连接，但已安装 CLI 的协议 %d 与后台协议 %d 不一致；基于 CLI 的终端功能受限，原生只读观察由工作台另行判断", result.CLIProtocol, result.DaemonProtocol)
			result.Suggestion = "请由管理员核对 CLI 与后台运行版本并安排兼容性处理；herdrx 不会升级、重启 Herdr 或停止任务"
		}
	}
	return result
}
