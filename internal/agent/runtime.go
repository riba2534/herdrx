package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"tailscale.com/wgengine/filter"
)

func Run(ctx context.Context, configPath string, logger *slog.Logger) error {
	return RunWithUpdater(ctx, configPath, logger, nil)
}

func RunWithUpdater(ctx context.Context, configPath string, logger *slog.Logger, updater ConfigUpdater) error {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	agentConfig, err := Load(configPath)
	if err != nil {
		return err
	}
	var allowed key.NodePublic
	if err := allowed.UnmarshalText([]byte(agentConfig.AllowedNodeKey)); err != nil {
		return fmt.Errorf("parse allowed tailcat node key: %w", err)
	}
	sshServer, err := NewSSHServer(configPath, agentConfig)
	if err != nil {
		return err
	}
	if updater != nil {
		sshServer.SetUpdater(updater)
	}
	server := &tailcat.Server{
		Key:            agentConfig.Node.Private,
		AllowedClients: []key.NodePublic{allowed},
		ServedTCPPorts: []filter.PortRange{{First: 22, Last: 22}},
		Logf:           func(format string, args ...any) { logger.Debug(fmt.Sprintf(format, args...)) },
	}
	// 针对已有旧无 PSK 绑定显式开启 DisablePresharedKey 兼容模式，保持旧客户端可连接；新协议配置非零 PSK 则不降级
	if agentConfig.PresharedKey.IsZero() || agentConfig.LegacyNoPSK {
		server.DisablePresharedKey = true
	} else {
		server.PresharedKey = agentConfig.PresharedKey
	}

	if len(agentConfig.Node.Public.Region) > 0 {
		server.Region = agentConfig.Node.Public.Region[0]
	} else {
		server.RegionID = agentConfig.Node.Public.RegionID
	}
	server.OnTCP = func(port uint16) func(net.Conn) {
		if port == 22 {
			return sshServer.Handle
		}
		return nil
	}
	if err := server.Start(); err != nil {
		return fmt.Errorf("start tailcat server: %w", err)
	}
	defer server.Close()
	logger.Info("herdrx-agent ready", "tailcat_addr", MaskAddress(string(server.TailcatAddr())))
	<-ctx.Done()
	return nil
}

func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}
