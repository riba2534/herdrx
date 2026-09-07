package tunnel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"tailscale.com/derp"
	"tailscale.com/derp/derphttp"
	"tailscale.com/net/netmon"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

type DERPProbe struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Stage     string `json:"stage"`
	Reachable bool   `json:"reachable"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ProbeDERPRegion performs an authenticated DERP protocol handshake using an
// unrelated ephemeral relay identity. It does not join a host's Tailcat tunnel.
func ProbeDERPRegion(ctx context.Context, region *tailcfg.DERPRegion) []DERPProbe {
	return ProbeDERPRegionWithKey(ctx, region, key.NewNode())
}

// ProbeDERPRegionWithKey supports relays which admit only registered nodes.
// Use a separate diagnostic identity; reusing an active node disconnects it.
func ProbeDERPRegionWithKey(ctx context.Context, region *tailcfg.DERPRegion, probeKey key.NodePrivate) []DERPProbe {
	if region == nil || len(region.Nodes) == 0 || len(region.Nodes) > 8 {
		return []DERPProbe{{Stage: "configuration", Error: "尚未保存固定中继区域，请先运行 herdrx connect 或用 setup --derp-config 配置"}}
	}
	if ProxyEnvSet() {
		return []DERPProbe{{Stage: "configuration", Error: "中继诊断不支持 HTTP/HTTPS/ALL_PROXY，请移除代理环境变量后重试"}}
	}
	results := make([]DERPProbe, len(region.Nodes))
	slots := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, node := range region.Nodes {
		wg.Go(func() {
			result := DERPProbe{Stage: "DNS/address"}
			if node == nil {
				result.Error = "中继节点为空"
				results[i] = result
				return
			}
			result.Host = node.HostName
			result.Port = node.DERPPort
			if result.Port == 0 {
				result.Port = 443
			}
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				result.Error = ctx.Err().Error()
				results[i] = result
				return
			}
			defer func() { <-slots; results[i] = result }()
			single := &tailcfg.DERPRegion{RegionID: region.RegionID, Nodes: []*tailcfg.DERPNode{node}}
			validated, err := DefaultSSRFValidator.ValidateDERPRegion(single)
			if err != nil {
				result.Error = err.Error()
				return
			}
			if node.STUNOnly {
				result.Stage = "STUN-only"
				result.Error = "此节点仅提供 STUN，不执行 DERP 握手"
				return
			}
			result.Stage = "DERP/TLS"
			monitor := netmon.NewStatic()
			defer monitor.Close()
			client := derphttp.NewRegionClient(probeKey, logger.Discard, monitor, func() *tailcfg.DERPRegion { return validated })
			defer client.Close()
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			stop := context.AfterFunc(probeCtx, func() { _ = client.Close() })
			defer stop()
			start := time.Now()
			if err := client.Connect(probeCtx); err != nil {
				result.Error = fmt.Sprintf("DERP 握手失败：%v", err)
				return
			}
			// Connect only exchanges the public key and sends ClientInfo. A
			// relay can still reject admission before sending ServerInfo.
			message, err := client.Recv()
			if _, ok := message.(derp.ServerInfoMessage); err != nil || !ok {
				result.Error = fmt.Sprintf("DERP 授权握手失败：%v", err)
				return
			}
			result.Reachable = true
			result.LatencyMS = time.Since(start).Milliseconds()
		})
	}
	wg.Wait()
	return results
}
