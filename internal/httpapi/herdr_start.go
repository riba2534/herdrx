package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
)

type herdrStarter interface {
	StartHerdr(context.Context) (herdr.HerdrStartResult, error)
}

// startHerdr runs only when the owner explicitly asks for it. It starts the
// Herdr server of an SSH host's session when none is running and detaches it
// from this connection; the workbench never stops, restarts or upgrades Herdr,
// and a running server is left untouched.
func (a *API) startHerdr(writer http.ResponseWriter, request *http.Request) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "主机不存在或无权访问")
		return
	}
	if host.Transport != "ssh" {
		writeError(writer, http.StatusBadRequest, "unsupported_transport", "只有 SSH 主机可以从网站启动 Herdr；其他主机请在远程主机终端运行 herdr 启动")
		return
	}
	connectCtx, cancelConnect := context.WithTimeout(request.Context(), a.config.HostDialTimeout)
	endpoint, err := a.hosts.Open(connectCtx, host)
	cancelConnect()
	if err != nil {
		var unknown *herdr.UnknownHostKeyError
		if errors.As(err, &unknown) {
			writeJSON(writer, http.StatusConflict, map[string]any{"error": "请先在主机页确认 SSH 主机指纹", "code": "host_key_unknown", "fingerprint": unknown.Fingerprint})
			return
		}
		writeHostConnectionError(writer, err)
		return
	}
	defer endpoint.Close()
	starter, ok := endpoint.(herdrStarter)
	if !ok {
		writeError(writer, http.StatusBadRequest, "unsupported_transport", "这台主机的连接方式不能从网站启动 Herdr，请在远程主机终端运行 herdr 启动")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()
	result, err := starter.StartHerdr(ctx)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "herdr_start_failed", "无法在远程主机启动 Herdr："+err.Error())
		return
	}
	if result.Status == "started" {
		// Drop cached capability failures so the next attach probes the new server.
		a.hosts.CloseHost(host.ID)
	}
	a.audit(request, "host.herdr_start", "host", host.ID, map[string]string{"status": result.Status, "method": result.Method, "linger": result.Linger})
	writeJSON(writer, http.StatusOK, map[string]any{"result": result})
}
