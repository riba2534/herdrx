package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

type CapabilityState string

const (
	CapabilityAvailable   CapabilityState = "available"
	CapabilityUnavailable CapabilityState = "unavailable"
	CapabilityUnknown     CapabilityState = "unknown"
)

type FeatureCapability struct {
	State    CapabilityState `json:"state"`
	Reason   string          `json:"reason"`
	Evidence []string        `json:"evidence"`
}

type CLIIdentity struct {
	Version  string   `json:"version"`
	Protocol int      `json:"protocol"`
	Methods  []string `json:"methods"`
}

type DaemonIdentity struct {
	Version      string                     `json:"version"`
	Protocol     int                        `json:"protocol"`
	Capabilities map[string]json.RawMessage `json:"capabilities,omitempty"`
}

type CapabilityReport struct {
	CLI        CLIIdentity                  `json:"cli"`
	Daemon     DaemonIdentity               `json:"daemon"`
	Generation string                       `json:"generation"`
	CheckedAt  time.Time                    `json:"checked_at"`
	Status     string                       `json:"status"`
	Coverage   string                       `json:"coverage"`
	Features   map[string]FeatureCapability `json:"features"`
}

// CapabilityProvider is optional so alternative transports can retain JSON API
// access without claiming support for a private terminal protocol.
type CapabilityProvider interface {
	HerdrCapabilities(context.Context) (CapabilityReport, error)
}

type RuntimeGenerationProvider interface {
	RuntimeGeneration() string
}

// CachedCapabilityProvider is for callers already polling Snapshot. A miss must
// use HerdrCapabilities; cached results are invalidated by live identity changes.
type CachedCapabilityProvider interface {
	CachedHerdrCapabilities() (CapabilityReport, bool)
}

// CLIIdentityProvider reads the installed executable, never the running daemon.
type CLIIdentityProvider interface {
	HerdrCLIIdentity(context.Context) (CLIIdentity, error)
}

var herdrVersionPattern = regexp.MustCompile(`(?i)^herdr\s+v?(\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?)\s*$`)

// ParseCLIIdentity deliberately does not infer the private protocol from semver.
// api schema describes the installed CLI and cannot advertise daemon methods.
func ParseCLIIdentity(version, schema []byte) (CLIIdentity, error) {
	identity := CLIIdentity{Methods: []string{}}
	match := herdrVersionPattern.FindStringSubmatch(strings.TrimSpace(string(version)))
	if len(match) != 2 {
		return identity, errors.New("Herdr CLI 版本响应无法识别，请检查配置的可执行文件")
	}
	identity.Version = match[1]
	var document struct {
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
	if err := json.Unmarshal(schema, &document); err != nil || document.Protocol <= 0 || len(document.Schemas.Request.OneOf) == 0 {
		return identity, errors.New("Herdr CLI schema 缺少有效协议或方法声明，请检查安装文件")
	}
	identity.Protocol = document.Protocol
	for _, entry := range document.Schemas.Request.OneOf {
		if method := entry.Properties.Method.Const; method != "" {
			identity.Methods = append(identity.Methods, method)
		}
	}
	slices.Sort(identity.Methods)
	identity.Methods = slices.Compact(identity.Methods)
	return identity, nil
}

func feature(state CapabilityState, reason string, evidence ...string) FeatureCapability {
	return FeatureCapability{State: state, Reason: reason, Evidence: evidence}
}

func reviewedProtocol(protocol int) bool { return protocol == 20 || protocol == 22 }

func testedIdentity(version string, protocol int) bool {
	return (version == "0.8.2" && protocol == 20) || ((version == "0.9.0" || version == "0.9.1") && protocol == 22)
}

// ProbeCapabilities makes only read-only requests. It never launches an observer,
// controller, pane or daemon, and never sends input or resize as a capability test.
func ProbeCapabilities(ctx context.Context, endpoint Endpoint) (CapabilityReport, error) {
	snapshot, err := endpoint.Snapshot(ctx)
	if err != nil {
		return CapabilityReport{}, err
	}
	return probeCapabilities(ctx, endpoint, snapshot), nil
}

func probeCapabilities(ctx context.Context, endpoint Endpoint, snapshot Snapshot) CapabilityReport {
	report := CapabilityReport{
		CLI:       CLIIdentity{Methods: []string{}},
		Daemon:    DaemonIdentity{Version: snapshot.Version, Protocol: snapshot.Protocol},
		CheckedAt: time.Now().UTC(), Coverage: "untested", Features: map[string]FeatureCapability{},
	}
	for _, name := range []string{"snapshot", "observe", "input", "resize", "preserve_scroll", "history"} {
		report.Features[name] = feature(CapabilityUnknown, "尚未取得足够的能力证据，请重新检查连接", "未探测")
	}
	report.Features["snapshot"] = feature(CapabilityAvailable, "当前 Herdr 会话快照读取成功", "daemon:session.snapshot")

	// Ping's optional fields come from the live named session. They are never
	// synthesized from an executable's schema or a different session's status.
	identityChanged := false
	if data, err := endpoint.Call(ctx, "ping", map[string]any{}); err == nil {
		var pong struct {
			Version      string                     `json:"version"`
			Protocol     int                        `json:"protocol"`
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		}
		if json.Unmarshal(data, &pong) == nil && pong.Version == snapshot.Version && pong.Protocol == snapshot.Protocol {
			report.Daemon.Capabilities = pong.Capabilities
		} else if pong.Version != "" && pong.Protocol > 0 {
			identityChanged = true
		}
	}
	var cliErr error
	if source, ok := endpoint.(CLIIdentityProvider); ok {
		report.CLI, cliErr = source.HerdrCLIIdentity(ctx)
	} else {
		cliErr = errors.New("该接入方式未提供 CLI 只读诊断")
	}
	if report.CLI.Methods == nil {
		report.CLI.Methods = []string{}
	}
	if testedIdentity(snapshot.Version, snapshot.Protocol) && testedIdentity(report.CLI.Version, report.CLI.Protocol) {
		report.Coverage = "tested"
	}

	// JSON input is independent of the CLI's binary handshake. Protocols 20/22
	// have reviewed pane.send_text/send_input contracts; a future protocol is not guessed.
	if reviewedProtocol(snapshot.Protocol) {
		report.Features["input"] = feature(CapabilityAvailable, "当前服务的文本输入接口契约已适配", fmt.Sprintf("daemon:protocol=%d; reviewed pane.send_text/pane.send_input", snapshot.Protocol))
		report.Features["history"] = feature(CapabilityAvailable, "当前服务的终端历史读取接口契约已适配", fmt.Sprintf("daemon:protocol=%d; reviewed pane.read", snapshot.Protocol))
	}
	terminal := feature(CapabilityAvailable, "CLI 与当前服务的终端协议一致", "cli:api schema --json", "daemon:session.snapshot")
	switch {
	case !reviewedProtocol(snapshot.Protocol):
		terminal = feature(CapabilityUnavailable, "工作台尚未适配此终端协议，请检查工作台支持矩阵；JSON 快照仍可读取", fmt.Sprintf("daemon:protocol=%d", snapshot.Protocol))
	case cliErr != nil:
		terminal = feature(CapabilityUnknown, "无法确认已安装 CLI 的协议，请重新检查；Tailcat 接入请确认受控端 CLI 支持只读诊断", "cli:probe incomplete")
	case report.CLI.Protocol != snapshot.Protocol:
		terminal = feature(CapabilityUnavailable, "已安装 CLI 与正在运行的 Herdr 协议不一致，请由管理员核对运行版本；工作台不会重启远程任务", fmt.Sprintf("cli:protocol=%d", report.CLI.Protocol), fmt.Sprintf("daemon:protocol=%d", snapshot.Protocol))
	case !slices.Contains(report.CLI.Methods, "session.snapshot"):
		terminal = feature(CapabilityUnavailable, "已安装 CLI 缺少所需接口声明，请检查 Herdr 安装", "cli:session.snapshot missing")
	}
	report.Features["observe"], report.Features["resize"] = terminal, terminal

	native, hasNative := endpoint.(NativeScrollEndpoint)
	if !hasNative {
		report.Features["preserve_scroll"] = feature(CapabilityUnavailable, "该接入方式未提供保尺寸滚轮能力", "transport:native scroll unavailable")
		report.Features["resize"] = feature(CapabilityUnavailable, "该接入方式无法动态同步观察画布，请检查受控端能力", "transport:native observer unavailable")
	} else if !reviewedProtocol(snapshot.Protocol) {
		report.Features["preserve_scroll"] = feature(CapabilityUnavailable, "工作台尚未适配此保尺寸滚轮协议，请检查支持矩阵", fmt.Sprintf("daemon:protocol=%d", snapshot.Protocol))
	} else {
		report.Features["preserve_scroll"] = feature(CapabilityAvailable, "保尺寸滚轮协议已适配，使用时继续校验目标终端的实际几何", fmt.Sprintf("daemon:protocol=%d", snapshot.Protocol), "transport:native scroll")
		report.Features["observe"] = feature(CapabilityAvailable, "原生只读观察协议已适配，打开时继续校验目标终端实际尺寸", fmt.Sprintf("daemon:protocol=%d", snapshot.Protocol), "transport:native observer")
	}
	// A bounded read of one existing pane verifies optional JSON methods without
	// sending any user input. An empty session is not evidence of missing support.
	if len(snapshot.Panes) > 0 && publicID.MatchString(snapshot.Panes[0].ID) {
		pane := snapshot.Panes[0].ID
		_, err := endpoint.Call(ctx, "pane.read", map[string]any{"pane_id": pane, "source": "recent", "format": "text", "lines": 1})
		switch {
		case err == nil:
			report.Features["history"] = feature(CapabilityAvailable, "当前服务的终端历史读取成功", "daemon:pane.read (1 line)")
		case IsUnsupportedError(err):
			report.Features["history"] = feature(CapabilityUnavailable, "当前 Herdr 服务未提供终端历史读取接口", "daemon:pane.read unsupported")
		default:
			report.Features["history"] = feature(CapabilityUnknown, "终端历史探测暂未完成，请重新检查", "daemon:pane.read incomplete")
		}
		if hasNative && reviewedProtocol(snapshot.Protocol) {
			if _, err := native.TerminalGeometry(ctx, pane); err != nil {
				state := CapabilityUnknown
				if IsUnsupportedError(err) {
					state = CapabilityUnavailable
				}
				geometry := feature(state, "无法确认目标终端的实际尺寸，请检查 Herdr 进程与受控端尺寸读取能力", "daemon:pane.process_info/transport:geometry incomplete")
				report.Features["preserve_scroll"] = geometry
				if terminal.State != CapabilityUnavailable {
					report.Features["resize"] = geometry
				}
				// CLI observation remains a safe fixed-canvas fallback, but cannot
				// qualify for explicit sizing control without a dynamic observer.
				report.Features["observe"] = terminal
				if terminal.State != CapabilityAvailable && state == CapabilityUnknown {
					report.Features["observe"] = geometry
				}
			} else {
				report.Features["observe"] = feature(CapabilityAvailable, "原生只读观察协议已适配，目标终端实际尺寸读取成功", fmt.Sprintf("daemon:protocol=%d", snapshot.Protocol), "transport:TIOCGWINSZ; native observer")
			}
		}
	}
	if identityChanged {
		for _, name := range []string{"observe", "resize", "input", "preserve_scroll", "history"} {
			report.Features[name] = feature(CapabilityUnknown, "Herdr 服务在探测期间发生变化，请重新检查", "daemon:snapshot/ping identity changed")
		}
	}
	report.refreshStatus()
	return report
}

func (r *CapabilityReport) refreshStatus() {
	r.Status = "available"
	for _, f := range r.Features {
		if f.State != CapabilityAvailable {
			r.Status = "limited"
		}
	}
	if r.Features["snapshot"].State != CapabilityAvailable {
		r.Status = "unavailable"
	}
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// IsUnsupportedError excludes timeouts, transient transport failures, missing
// panes and ordinary invalid parameters. Only explicit method rejection sticks.
func IsUnsupportedError(err error) bool {
	var api *APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.Code {
	case "unknown_method", "method_not_found", "unsupported_method":
		return true
	case "unsupported":
		return strings.Contains(strings.ToLower(api.Message), "method")
	case "invalid_request":
		message := strings.ToLower(api.Message)
		if strings.Contains(message, "unknown method") {
			return true
		}
		// A serde enum parameter can also be an unknown variant. Only a method
		// discriminant is evidence that the corresponding interface is absent.
		for _, method := range []string{"session.snapshot", "pane.send_input", "pane.send_text", "pane.send_keys", "pane.read", "pane.process_info"} {
			if strings.Contains(message, "unknown variant `"+method+"`") || strings.Contains(message, "unknown variant \""+method+"\"") {
				return true
			}
		}
	}
	return false
}
