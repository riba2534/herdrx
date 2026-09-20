package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
)

func (a *API) hostCapabilities(writer http.ResponseWriter, request *http.Request) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "主机不存在或无权访问")
		return
	}
	connectCtx, cancelConnect := context.WithTimeout(request.Context(), a.config.HostDialTimeout)
	endpoint, err := a.hosts.Open(connectCtx, host)
	cancelConnect()
	if err != nil {
		writeHostConnectionError(writer, err)
		return
	}
	defer endpoint.Close()
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	var report herdr.CapabilityReport
	if provider, ok := endpoint.(herdr.CapabilityProvider); ok {
		report, err = provider.HerdrCapabilities(ctx)
	} else {
		report, err = herdr.ProbeCapabilities(ctx, endpoint)
	}
	if err != nil {
		writeError(writer, http.StatusBadGateway, "herdr_unavailable", "无法读取 Herdr 能力，请检查主机连接后重试")
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]any{"capabilities": report})
}
