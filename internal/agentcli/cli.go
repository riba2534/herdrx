package agentcli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

var Version = "v0.1.0"

// Run 作为用户 CLI 的可测试总分发入口
func Run(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	defaultConfigPath := filepath.Join(env.ConfigDir, "config.json")

	switch args[0] {
	case "help", "--help", "-h":
		printUsage(stdout)
		return 0
	case "setup":
		return runSetup(args[1:], stdout, stderr, env, defaultConfigPath)
	case "connect":
		return runConnect(args[1:], stdout, stderr, env, defaultConfigPath)
	case "status":
		return runStatus(args[1:], stdout, stderr, env, defaultConfigPath)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr, env, defaultConfigPath)
	case "serve":
		return runServe(args[1:], stdout, stderr, env, defaultConfigPath)
	case "service":
		return runService(args[1:], stdout, stderr, env, defaultConfigPath)
	case "logs":
		return runLogs(args[1:], stdout, stderr)
	case "update":
		return runUpdate(args[1:], stdout, stderr, env)
	case "self-test":
		return runSelfTest(args[1:], stdout, stderr)
	case "rollback":
		return runRollback(args[1:], stdout, stderr, env)

	// 兼容旧版 herdrx-agent 命令路径
	case "init":
		return runLegacyInit(args[1:], stdout, stderr, env, defaultConfigPath)
	case "run":
		// run 作为 serve 的完全兼容入口，统一生命周期与单写者管理器
		return runServe(args[1:], stdout, stderr, env, defaultConfigPath)
	case "pair":
		return runLegacyPair(args[1:], stdout, stderr, env, defaultConfigPath)
	case "install":
		return runLegacyInstall(args[1:], stdout, stderr, env, defaultConfigPath)
	case "uninstall":
		return runLegacyUninstall(args[1:], stdout, stderr, env)
	case "unpair":
		return runLegacyUnpair(args[1:], stdout, stderr, env, defaultConfigPath)
	case "version", "--version", "-V":
		fmt.Fprintf(stdout, "herdrx %s\n", Version)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

// GenerateSetupID 根据受控端根节点的公钥生成安全且全局唯一的 SetupID
// 采用稳定根公钥文本的 SHA-256 哈希（截取 128 位 = 16 字节，编码为 32 位十六进制字符），彻底杜绝地址前缀碰撞
func GenerateSetupID(node tailcat.PrivateKey) string {
	h := sha256.Sum256([]byte(node.Private.Public().String()))
	return "setup-" + hex.EncodeToString(h[:16])
}

func runSetup(args []string, stdout, stderr io.Writer, env Environment, defaultConfigPath string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	herdrBin := fs.String("herdr-bin", "", "path to herdr binary")
	configPath := fs.String("config", defaultConfigPath, "config file path")
	runtimeDir := fs.String("runtime-dir", "", "path to runtime dir")
	derpConfig := fs.String("derp-config", "", "JSON file describing a self-hosted DERP region")
	skipService := fs.Bool("skip-service", false, "skip background service registration")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *runtimeDir != "" {
		env.RuntimeDir = *runtimeDir
	}
	if *herdrBin != "" {
		env.HerdrBin = *herdrBin
	}

	var selectedRegion *tailcfg.DERPRegion
	if *derpConfig != "" {
		var err error
		selectedRegion, err = readDERPConfig(*derpConfig)
		if err != nil {
			fmt.Fprintf(stderr, "invalid DERP configuration: %v\n", err)
			return 1
		}
	}

	// 1. 运行严格 Preflight 检查
	preflight := RunPreflight(env)
	if preflight.Status != PreflightOK {
		fmt.Fprintf(stderr, "setup failed: %s\n", preflight.Details)
		if preflight.Suggestion != "" {
			fmt.Fprintf(stderr, "%s\n", preflight.Suggestion)
		}
		// 无论何种非 OK 状态，均明确失败，绝不输出虚假成功
		return 1
	}

	// 2. 初始化或迁移配置（通过 StateStore 保证同一事务）
	targetCfgPath, err := filepath.Abs(filepath.Clean(*configPath))
	if err != nil {
		fmt.Fprintf(stderr, "invalid config path: %v\n", err)
		return 1
	}

	store, err := NewStateStore(targetCfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "init state store: %v\n", err)
		return 1
	}

	exists, existsErr := agent.Exists(targetCfgPath)
	if existsErr != nil {
		fmt.Fprintf(stderr, "inspect existing identity: %v\n", existsErr)
		return 1
	}
	if !exists {
		// 尝试从 legacy 迁移
		migratedCfg, migrated, mErr := MigrateFromLegacy(env, targetCfgPath)
		if mErr != nil && !errors.Is(mErr, errNoLegacyConfiguration) {
			fmt.Fprintf(stderr, "configuration migration failed; existing identity was not replaced: %v\n", mErr)
			return 1
		}
		if mErr == nil && migrated {
			fmt.Fprintf(stdout, "successfully migrated configuration from legacy path to %s\n", targetCfgPath)
			// 持久化保存 herdr 路径，且严格传播错误
			if _, err := store.Update(func(c *Config) error {
				c.HerdrBin = preflight.HerdrPath
				if selectedRegion != nil && !sameDERPRegion(*c, selectedRegion) && (c.Paired || c.Binding.Status == "active" || c.Binding.Status == "prepared") {
					return errors.New("bound DERP region changes require herdrx connect --refresh-endpoint --derp-config <file>")
				}
				applyDERPRegion(c, selectedRegion)
				return nil
			}); err != nil {
				fmt.Fprintf(stderr, "persist herdr path failed: %v\n", err)
				return 1
			}
			_ = migratedCfg
		} else {
			// 初始化新受控端根身份 (identity-only)
			node := tailcat.NewPrivateKey()
			psk := node.Public.PresharedKey
			if psk.IsZero() {
				psk = tailcat.NewPresharedKey()
				node.Public.PresharedKey = psk
			}
			hostPrivate, _, keyErr := secure.GenerateSSHKey("herdrx-agent")
			if keyErr != nil {
				fmt.Fprintf(stderr, "generate host key: %v\n", keyErr)
				return 1
			}

			_, updateErr := store.Update(func(c *Config) error {
				if c.Version != 0 || !c.Node.Private.IsZero() {
					return errors.New("another setup created this identity; run setup again to reuse it")
				}
				c.Version = 1
				c.Node = *node
				applyDERPRegion(c, selectedRegion)
				c.PresharedKey = psk
				c.SSHHostPrivate = string(hostPrivate)
				c.SetupID = GenerateSetupID(*node)
				c.HerdrBin = preflight.HerdrPath
				c.Paired = false
				c.Revoked = false
				c.Binding = agent.BindingConfig{Status: "none"}
				return nil
			})
			if updateErr != nil {
				fmt.Fprintf(stderr, "save initial config: %v\n", updateErr)
				return 1
			}
			fmt.Fprintf(stdout, "initialized new agent configuration at %s\n", targetCfgPath)
		}
	} else {
		existing, err := SafeLoadConfig(targetCfgPath)
		if err != nil {
			fmt.Fprintf(stderr, "read existing identity: %v\n", err)
			return 1
		}
		regionChanged := !sameDERPRegion(existing, selectedRegion)
		if regionChanged && (existing.Paired || existing.Binding.Status == "active" || existing.Binding.Status == "prepared") {
			fmt.Fprintln(stderr, "bound DERP region changes require herdrx connect --refresh-endpoint --derp-config <file>")
			return 1
		}
		needsSetupID := (existing.Binding.Status == "" || existing.Binding.Status == "none") && !existing.Paired && (existing.SetupID == "" || strings.HasPrefix(existing.SetupID, "setup-tco2FwWC"))
		// Repeated setup must not contend with the daemon's lifetime config lock
		// when the requested settings and stable identity are already present.
		if existing.HerdrBin != preflight.HerdrPath || needsSetupID || regionChanged {
			if _, err := store.Update(func(c *Config) error {
				if !sameDERPRegion(*c, selectedRegion) && (c.Paired || c.Binding.Status == "active" || c.Binding.Status == "prepared") {
					return errors.New("binding changed; use herdrx connect --refresh-endpoint --derp-config <file>")
				}
				c.HerdrBin = preflight.HerdrPath
				c.SyncPSK()
				applyDERPRegion(c, selectedRegion)
				// 仅当未绑定（无 prepared 或 active binding）且属于已知的固定 CBOR 前缀碰撞格式或空时才安全迁移
				// 已有 prepared/active 的 AgentID 属于签名/绑定上下文，严禁静默重算！
				if (c.Binding.Status == "" || c.Binding.Status == "none") && !c.Paired {
					if c.SetupID == "" || strings.HasPrefix(c.SetupID, "setup-tco2FwWC") {
						c.SetupID = GenerateSetupID(c.Node)
					}
				}
				return nil
			}); err != nil {
				fmt.Fprintf(stderr, "update existing config with herdr path failed: %v\n", err)
				return 1
			}
		}
		fmt.Fprintf(stdout, "using existing agent configuration at %s (identity preserved)\n", targetCfgPath)
	}

	// 3. 安装后台系统服务
	if *skipService {
		fmt.Fprintln(stdout, "skipping background service registration as requested (--skip-service)")
	} else {
		binPath, err := os.Executable()
		if err != nil {
			fmt.Fprintf(stderr, "resolve service binary: %v\n", err)
			return 1
		}
		binPath = resolveServiceExecutable(env, binPath)
		instRes, err := InstallService(env, binPath, targetCfgPath)
		if err != nil {
			fmt.Fprintf(stderr, "install service failed: %v\n", err)
			return 1
		}

		fmt.Fprintf(stdout, "herdrx background service installed at %s\n", instRes.UnitPath)
		if instRes.LingerWarning != "" {
			fmt.Fprintf(stdout, "注意: %s\n", instRes.LingerWarning)
		} else {
			fmt.Fprintln(stdout, "开机与登出保活: 已启用 (loginctl linger ok)")
		}
	}

	fmt.Fprintln(stdout, "setup completed. run herdrx status, then herdrx connect --plain to bind this host in the website.")
	return 0
}

func runConnect(args []string, stdout, stderr io.Writer, env Environment, defaultPath string) int {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", defaultPath, "config file path")
	runtimeDir := fs.String("runtime-dir", "", "path to runtime dir")
	plain := fs.Bool("plain", false, "output connection string only on stdout")
	refresh := fs.Bool("refresh-endpoint", false, "export a signed endpoint update for the current binding")
	derpConfig := fs.String("derp-config", "", "JSON DERP region for an explicit endpoint migration")
	workbench := fs.String("workbench", "", "prefer this public HTTPS workbench's built-in relay")
	relayToken := fs.String("relay-token", "", "short-lived relay permission from the workbench")
	relayAddress := fs.String("relay-address", "", "verified public workbench IP from the connection command")
	renew := fs.Bool("renew", false, "revoke previous pending authorization and generate new string")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runtimeDir != "" {
		env.RuntimeDir = *runtimeDir
	}

	if (*refresh && *renew) || (*derpConfig != "" && !*refresh) {
		fmt.Fprintln(stderr, "--derp-config requires --refresh-endpoint, which cannot be combined with --renew")
		return 2
	}
	if (*derpConfig != "" && *workbench != "") || ((*relayToken == "") != (*workbench == "")) || (*relayAddress != "" && *workbench == "") {
		fmt.Fprintln(stderr, "--workbench and --relay-token must be supplied together and cannot be combined with --derp-config")
		return 2
	}
	cleanCfgPath, _ := filepath.Abs(filepath.Clean(*configPath))
	sockPath := filepath.Join(env.RuntimeDir, "control.sock")
	var relay *tunnel.RelayBootstrap
	if *workbench != "" {
		relay = &tunnel.RelayBootstrap{Workbench: strings.TrimRight(*workbench, "/"), Token: *relayToken, Address: *relayAddress}
	}

	if *refresh {
		input := refreshEndpointRequest{Relay: relay}
		if *derpConfig != "" {
			region, err := readDERPConfig(*derpConfig)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			input.Region = region
		}
		var result refreshEndpointResult
		if err := ClientCallIPCWithConfig(sockPath, "POST", "/endpoint", cleanCfgPath, input, &result); err != nil {
			fmt.Fprintf(stderr, "endpoint refresh failed: %v\n", err)
			return 1
		}
		if !*plain {
			fmt.Fprintln(stdout, "在原网站打开该主机的“更新连接端点”，粘贴下面的更新包（10 分钟有效）。远程任务继续运行。")
		}
		if result.Relay != "" {
			fmt.Fprintln(stderr, "中继选择："+result.Relay)
		}
		fmt.Fprintln(stdout, result.Update)
		return 0
	}
	endpoint := "/connect"
	if *renew {
		endpoint += "?renew=true"
	}

	var connRes ConnectResult
	err := ClientCallIPCWithConfig(sockPath, "POST", endpoint, cleanCfgPath, connectRequest{Relay: relay}, &connRes)
	if err != nil {
		if errors.Is(err, ErrDaemonOffline) {
			fmt.Fprintln(stderr, "error: herdrx daemon is not running; please run 'herdrx setup' or start 'herdrx serve' first")
		} else {
			fmt.Fprintf(stderr, "connect failed: %v\n", err)
		}
		return 1
	}
	if connRes.Relay != "" {
		fmt.Fprintln(stderr, "中继选择："+connRes.Relay)
	}

	if *plain {
		fmt.Fprintln(stdout, connRes.ConnectionString)
		return 0
	}

	hName, _ := os.Hostname()
	if hName == "" {
		hName = "herdr-host"
	}

	fmt.Fprintf(stdout, "主机：%s\n", hName)
	fmt.Fprintln(stdout, "Herdr：可用")
	fmt.Fprintln(stdout, "herdrx 进程：运行中；开机与登出保活请按安装引导检查 loginctl linger")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "请在网站中选择：添加主机 → Tailcat 内网穿透 → 绑定主机")
	fmt.Fprintln(stdout, "粘贴以下连接字符串（10 分钟内有效，只能绑定一次）：")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, connRes.ConnectionString)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "配对成功后长期有效，重启或更新无需重新配对。")
	fmt.Fprintln(stdout, "请勿分享这段连接字符串。")
	return 0
}

func runStatus(args []string, stdout, stderr io.Writer, env Environment, defaultConfigPath string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output in JSON format")
	runtimeDir := fs.String("runtime-dir", "", "path to runtime dir")
	configPath := fs.String("config", "", "config file path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runtimeDir != "" {
		env.RuntimeDir = *runtimeDir
	}

	var expectedConfig string
	if *configPath != "" {
		expectedConfig, _ = filepath.Abs(filepath.Clean(*configPath))
	}

	sockPath := filepath.Join(env.RuntimeDir, "control.sock")
	var daemonStat DaemonStatus
	err := ClientCallIPCWithConfig(sockPath, "GET", "/status", expectedConfig, nil, &daemonStat)

	if *asJSON {
		res := map[string]any{
			"daemon_running": err == nil,
		}
		if err == nil {
			res["daemon"] = daemonStat
			_ = json.NewEncoder(stdout).Encode(res)
			return 0
		}
		// 离线时输出错误并返回退出码 1，保持与文本输出一致
		res["error"] = err.Error()
		_ = json.NewEncoder(stdout).Encode(res)
		return 1
	}

	if err != nil {
		fmt.Fprintf(stderr, "daemon is not running (offline): %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "herdrx daemon: 运行中 (PID: %d, 运行时间: %d 秒, 版本: %s)\n",
		daemonStat.PID, daemonStat.UptimeSeconds, daemonStat.Version)
	fmt.Fprintf(stdout, "Herdr 状态: %s (%s)\n", daemonStat.HerdrStatus.Status, daemonStat.HerdrStatus.Details)
	fmt.Fprintf(stdout, "配对授权状态: paired=%v, revoked=%v\n", daemonStat.Paired, daemonStat.Revoked)
	if daemonStat.TailcatAddrMasked != "" {
		fmt.Fprintf(stdout, "受控端连接地址: %s\n", daemonStat.TailcatAddrMasked)
	}
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer, env Environment, defaultConfigPath string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output in JSON format")
	network := fs.Bool("network", false, "probe the saved DERP nodes with a protocol handshake")
	configPath := fs.String("config", defaultConfigPath, "config file path")
	runtimeDir := fs.String("runtime-dir", "", "path to runtime dir")
	herdrBin := fs.String("herdr-bin", "", "path to herdr binary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runtimeDir != "" {
		env.RuntimeDir = *runtimeDir
	}
	if *herdrBin != "" {
		env.HerdrBin = *herdrBin
	}

	var expectedConfig string
	if *configPath != "" {
		expectedConfig, _ = filepath.Abs(filepath.Clean(*configPath))
	}

	sockPath := filepath.Join(env.RuntimeDir, "control.sock")
	var doc DoctorResult
	endpoint := "/doctor"
	if *network {
		endpoint += "?network=true"
	}
	err := ClientCallIPCWithConfig(sockPath, "GET", endpoint, expectedConfig, nil, &doc)

	// 若 daemon 离线，执行本地单机离线诊断
	if err != nil {
		store, _ := NewStateStore(*configPath)
		var cfg Config
		var cfgValid bool
		if store != nil {
			c, sErr := store.Snapshot()
			if sErr == nil {
				cfg = c
				cfgValid = true
				if env.HerdrBin == "" && cfg.HerdrBin != "" {
					env.HerdrBin = cfg.HerdrBin
				}
			}
		}
		preflight := RunPreflight(env)
		doc = DoctorResult{
			DaemonRunning: false,
			HerdrCheck:    preflight,
			ConfigValid:   cfgValid,
			ConfigPath:    *configPath,
			Paired:        cfg.Paired,
			Revoked:       cfg.Revoked,
			OverallReady:  false,
		}
	}
	if *network && err != nil {
		if cfg, loadErr := SafeLoadConfig(*configPath); loadErr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			doc.DERP = probeConfiguredRelay(ctx, cfg)
			cancel()
		}
	}

	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(doc)
		if !doc.OverallReady {
			return 1
		}
		return 0
	}

	fmt.Fprintln(stdout, "=== herdrx 诊断报告 ===")
	fmt.Fprintf(stdout, "后台守护进程: running=%v\n", doc.DaemonRunning)
	fmt.Fprintf(stdout, "Herdr 检测: %s (版本: %s, 路径: %s, 运行中: %v)\n",
		doc.HerdrCheck.Status, doc.HerdrCheck.Version, doc.HerdrCheck.HerdrPath, doc.HerdrCheck.ServerRunning)
	fmt.Fprintf(stdout, "配置状态: valid=%v (路径: %s)\n", doc.ConfigValid, doc.ConfigPath)
	fmt.Fprintf(stdout, "配对授权: paired=%v, revoked=%v\n", doc.Paired, doc.Revoked)
	for _, probe := range doc.DERP {
		fmt.Fprintf(stdout, "中继 %s:%d: %s reachable=%v latency=%dms %s\n", probe.Host, probe.Port, probe.Stage, probe.Reachable, probe.LatencyMS, probe.Error)
	}
	if *network {
		fmt.Fprintln(stdout, "中继可达性独立于 Herdr 状态；此诊断不判断终端连接当前采用直连还是中继。")
	}

	if doc.OverallReady {
		fmt.Fprintln(stdout, "综合评估: 就绪")
		return 0
	}
	fmt.Fprintln(stdout, "综合评估: 未完全就绪")
	return 1
}

func runServe(args []string, stdout, stderr io.Writer, env Environment, defaultConfigPath string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", defaultConfigPath, "config file path")
	runtimeDir := fs.String("runtime-dir", "", "path to runtime dir")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runtimeDir != "" {
		env.RuntimeDir = *runtimeDir
	}

	cleanCfgPath, err := filepath.Abs(filepath.Clean(*configPath))
	if err != nil {
		fmt.Fprintf(stderr, "invalid config path: %v\n", err)
		return 1
	}

	store, err := NewStateStore(cleanCfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "init store: %v\n", err)
		return 1
	}

	// 1. 获取基于统一配置路径的排他文件锁（固定 inode），持有进程级锁所有权，杜绝多实例双开与重入死锁
	if err := store.AcquireDaemonLock(); err != nil {
		fmt.Fprintf(stderr, "cannot start daemon: %v\n", err)
		return 1
	}
	defer store.ReleaseDaemonLock()

	// 2. 启动本地控制面 IPC 服务
	ipcServer := NewIPCServer(env, store)
	if err := ipcServer.Start(); err != nil {
		fmt.Fprintf(stderr, "start control IPC server: %v\n", err)
		return 1
	}
	defer ipcServer.Close()
	fmt.Fprintf(stdout, "herdrx control IPC server listening on %s (PID: %d)\n", filepath.Join(env.RuntimeDir, "control.sock"), os.Getpid())

	// 3. 检查远程通道监听策略
	cfg, err := store.Snapshot()
	logger := slog.New(slog.NewJSONHandler(stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err == nil && cfg.AllowedNodeKey != "" && cfg.AuthorizedSSHKey != "" && !cfg.Revoked && (cfg.Binding.Status == "" || cfg.Binding.Status == "none") {
		// 仅在纯旧 legacy 模式（未采用 P2 binding 且配置了旧 allowed_node_key）的主机启动受控端 Tailcat 服务
		// 注入当前 store 作为 ConfigUpdater，确保 confirm-pair 经同一事务落盘并实时更新缓存
		fmt.Fprintln(stdout, "starting remote tailcat listener (legacy mode)...")
		ipcServer.SetRemoteStatus(true, nil)

		go func() {
			runErr := agent.RunWithUpdater(ctx, cleanCfgPath, logger, store)
			if runErr != nil && ctx.Err() == nil {
				logger.Error("remote listener failed", "error", runErr)
				ipcServer.SetRemoteStatus(false, runErr)
			}
		}()
	} else if cfg.Binding.Status == "active" || cfg.Binding.Status == "prepared" {
		// 已由 ipcServer.Start() 内部自动从 StateStore 恢复正式端点，绝不另起 legacy listener 造成双活！
		fmt.Fprintf(stdout, "formal tailcat listener active (status: %s)\n", cfg.Binding.Status)
	} else {
		// 未配置远程白名单的全新 identity-only 主机：仅开启本地控制面，严禁开放远程端点
		fmt.Fprintln(stdout, "running in local control-only mode (remote listener disabled)")
		ipcServer.SetRemoteStatus(false, nil)
	}

	<-ctx.Done()
	fmt.Fprintln(stdout, "shutting down herdrx daemon...")
	return 0
}

func runService(args []string, stdout, stderr io.Writer, env Environment, defaultConfigPath string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: herdrx service <start|stop|restart|uninstall>")
		return 2
	}
	runner := env.ServiceRunner
	if runner == nil {
		runner = NewRealServiceRunner()
	}

	switch args[0] {
	case "start":
		if err := runner.EnableAndStart("herdrx.service"); err != nil {
			fmt.Fprintf(stderr, "service start: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "herdrx service started")
		return 0
	case "stop":
		if err := runner.Stop("herdrx.service"); err != nil {
			fmt.Fprintf(stderr, "service stop: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "herdrx service stopped")
		return 0
	case "restart":
		if err := runner.Restart("herdrx.service"); err != nil {
			fmt.Fprintf(stderr, "service restart: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "herdrx service restarted")
		return 0
	case "uninstall":
		if err := UninstallService(env); err != nil {
			fmt.Fprintf(stderr, "service uninstall: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "herdrx service uninstalled")
		return 0
	default:
		fmt.Fprintf(stderr, "unknown service action: %s\n", args[0])
		return 2
	}
}

// -------------------------------------------------------------
// 兼容旧版 herdrx-agent 命令实现
// -------------------------------------------------------------

func runLegacyInit(args []string, stdout, stderr io.Writer, env Environment, defaultPath string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", defaultPath, "config file")
	allowedNode := fs.String("allow-node", "", "tailcat node public key")
	sshKey := fs.String("ssh-key", "", "SSH public key")
	publicURL := fs.String("public-url", "", "public URL")
	setupID := fs.String("setup-id", "", "setup ID")
	regionID := fs.Int("region-id", 304, "DERP region ID")
	derpHost := fs.String("derp-host", "", "self-hosted DERP hostname")
	force := fs.Bool("force", false, "force overwrite")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *allowedNode == "" || *sshKey == "" || *publicURL == "" || *setupID == "" {
		fmt.Fprintln(stderr, "error: --allow-node, --ssh-key, --public-url and --setup-id are required")
		return 1
	}
	var parsedNode key.NodePublic
	if err := parsedNode.UnmarshalText([]byte(*allowedNode)); err != nil {
		fmt.Fprintf(stderr, "invalid --allow-node: %v\n", err)
		return 1
	}

	cleanPath, _ := filepath.Abs(filepath.Clean(*path))
	store, err := NewStateStore(cleanPath)
	if err != nil {
		fmt.Fprintf(stderr, "init store: %v\n", err)
		return 1
	}

	if exists, _ := agent.Exists(cleanPath); exists && !*force {
		fmt.Fprintf(stderr, "config already exists at %s; use --force to overwrite\n", cleanPath)
		return 1
	}

	node := tailcat.NewPrivateKey()
	if *derpHost != "" {
		node.Public.Region = []*tailcfg.DERPRegion{{
			RegionID: 1, RegionCode: "herdrx", RegionName: "herdrx",
			Nodes: []*tailcfg.DERPNode{{Name: "herdrx-derp", RegionID: 1, HostName: *derpHost}},
		}}
	} else {
		node.Public.RegionID = tailcfg.DERPRegionID(*regionID)
	}

	hostPrivate, hostPublic, err := secure.GenerateSSHKey("herdrx-agent")
	if err != nil {
		fmt.Fprintf(stderr, "generate ssh key: %v\n", err)
		return 1
	}

	_, err = store.Update(func(c *Config) error {
		c.Version = 1
		c.Node = *node
		c.AllowedNodeKey = parsedNode.String()
		c.AuthorizedSSHKey = strings.TrimSpace(*sshKey)
		c.SSHHostPrivate = string(hostPrivate)
		c.PublicURL = strings.TrimRight(*publicURL, "/")
		c.SetupID = *setupID
		c.LegacyNoPSK = true // 显式标记为 legacy 模式
		c.Paired = false
		c.Revoked = false
		return nil
	})
	if err != nil {
		fmt.Fprintf(stderr, "save config: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "configured %s\nagent SSH host key: %s\n", cleanPath, strings.TrimSpace(string(hostPublic)))
	return 0
}

func runLegacyPair(args []string, stdout, stderr io.Writer, env Environment, defaultPath string) int {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", defaultPath, "config file")
	runtimeDir := fs.String("runtime-dir", "", "path to runtime dir")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runtimeDir != "" {
		env.RuntimeDir = *runtimeDir
	}

	cleanPath, _ := filepath.Abs(filepath.Clean(*path))

	// 1. 在线优先：若 daemon 正在运行，必须通过 IPC 请求由 daemon 的 store.Update 生成与保存
	sockPath := filepath.Join(env.RuntimeDir, "control.sock")
	var pairRes LegacyPairResult
	err := ClientCallIPCWithConfig(sockPath, "POST", "/legacy/pair", cleanPath, nil, &pairRes)
	if err == nil {
		qrterminal.GenerateHalfBlock(pairRes.URL, qrterminal.L, stdout)
		fmt.Fprintln(stdout, pairRes.URL)
		fmt.Fprintf(stdout, "pairing link expires at %s\n", pairRes.ExpiresAt.Format(time.RFC3339))
		return 0
	}
	if !errors.Is(err, ErrDaemonOffline) {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	// 2. 离线模式：daemon 确实未运行，降级由 CLI 本地获取排他锁进行 Update 事务
	store, err := NewStateStore(cleanPath)
	if err != nil {
		fmt.Fprintf(stderr, "init store: %v\n", err)
		return 1
	}

	token, err := secure.Token(16)
	if err != nil {
		fmt.Fprintf(stderr, "generate token: %v\n", err)
		return 1
	}

	tokenHashStr := base64.RawStdEncoding.EncodeToString(secure.TokenHash(token))
	expiresAt := time.Now().Add(10 * time.Minute)

	updatedCfg, err := store.Update(func(c *Config) error {
		c.PairTokenHash = tokenHashStr
		c.PairExpiresAt = expiresAt
		c.Paired = false
		return nil
	})
	if err != nil {
		fmt.Fprintf(stderr, "save pair config: %v\n", err)
		return 1
	}

	hostSigner, err := ssh.ParsePrivateKey([]byte(updatedCfg.SSHHostPrivate))
	if err != nil {
		fmt.Fprintf(stderr, "parse host private key: %v\n", err)
		return 1
	}

	payload := map[string]any{
		"v": 1, "setup_id": updatedCfg.SetupID, "tc": updatedCfg.Node.Public.Addr(), "host": hostname(),
		"os": runtime.GOOS, "arch": runtime.GOARCH, "agent_ver": Version,
		"ssh_host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))),
		"token":        token, "exp": updatedCfg.PairExpiresAt.Unix(),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(stderr, "marshal pair payload: %v\n", err)
		return 1
	}
	pairURL := updatedCfg.PublicURL + "/#pair=" + base64.RawURLEncoding.EncodeToString(encoded)

	qrterminal.GenerateHalfBlock(pairURL, qrterminal.L, stdout)
	fmt.Fprintln(stdout, pairURL)
	fmt.Fprintf(stdout, "pairing link expires at %s\n", updatedCfg.PairExpiresAt.Format(time.RFC3339))
	return 0
}

func runLegacyInstall(args []string, stdout, stderr io.Writer, env Environment, defaultPath string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", defaultPath, "config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	bin, _ := os.Executable()
	res, err := InstallService(env, bin, *path)
	if err != nil {
		fmt.Fprintf(stderr, "install service failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "service installed at %s\n", res.UnitPath)
	return 0
}

func runLegacyUninstall(args []string, stdout, stderr io.Writer, env Environment) int {
	if err := UninstallService(env); err != nil {
		fmt.Fprintf(stderr, "uninstall service failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "service uninstalled")
	return 0
}

func runLegacyUnpair(args []string, stdout, stderr io.Writer, env Environment, defaultPath string) int {
	fs := flag.NewFlagSet("unpair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", defaultPath, "config file")
	runtimeDir := fs.String("runtime-dir", "", "path to runtime dir")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runtimeDir != "" {
		env.RuntimeDir = *runtimeDir
	}
	cleanPath, _ := filepath.Abs(filepath.Clean(*path))

	sockPath := filepath.Join(env.RuntimeDir, "control.sock")
	err := ClientCallIPCWithConfig(sockPath, "POST", "/unpair", cleanPath, nil, nil)
	if err == nil {
		fmt.Fprintln(stdout, "agent revoked via active daemon IPC")
		return 0
	}
	if !errors.Is(err, ErrDaemonOffline) {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	// 离线状态：走 StateStore 事务更新，保持锁与父目录 fsync
	store, err := NewStateStore(cleanPath)
	if err != nil {
		fmt.Fprintf(stderr, "init store: %v\n", err)
		return 1
	}

	_, err = store.Update(func(c *Config) error {
		c.Paired = false
		c.Revoked = true
		c.PairTokenHash = ""
		c.PairExpiresAt = time.Time{}
		c.Binding.Status = "revoked"
		c.Binding.Epoch = agent.NextEpoch(c.Binding.Epoch)
		c.Enrollment = agent.EnrollmentConfig{}
		return nil
	})
	if err != nil {
		fmt.Fprintf(stderr, "unpair failed: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "agent access revoked; run a new init command to pair again")
	return 0
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return "herdr-host"
	}
	return name
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: herdrx <command> [options]")
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  setup     Check environment and install background service")
	fmt.Fprintln(w, "  connect   Request one-time pairing endpoint")
	fmt.Fprintln(w, "  status    Query status of background daemon via IPC")
	fmt.Fprintln(w, "  doctor    Perform diagnostic checks on Herdr and service")
	fmt.Fprintln(w, "  logs      Show herdrx user-service logs (does not stop Herdr)")
	fmt.Fprintln(w, "  serve     Run daemon in foreground with IPC control")
	fmt.Fprintln(w, "  service   Manage herdrx service (start|stop|restart|uninstall)")
	fmt.Fprintln(w, "  update    Install a signed herdrx update")
	fmt.Fprintln(w, "  self-test Check binary compatibility and read an identity offline")
	fmt.Fprintln(w, "  rollback  Restore the previous compatible herdrx binary")
	fmt.Fprintln(w, "  version   Print version information")
}

func runLogs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	follow := fs.Bool("f", false, "follow log output")
	lines := fs.Int("n", 200, "number of journal lines")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cmdArgs := []string{"--user", "-u", "herdrx.service", "-n", fmt.Sprintf("%d", *lines), "--no-pager"}
	if *follow {
		cmdArgs = append(cmdArgs, "-f")
	}
	cmd := exec.Command("journalctl", cmdArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "logs failed: %v\nuse: journalctl --user -u herdrx.service\nthis command does not stop Herdr\n", err)
		return 1
	}
	return 0
}
