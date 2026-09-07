package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/tunnel"
)

func (a *API) refreshTailcatEndpoint(w http.ResponseWriter, r *http.Request) {
	host, err := a.ownedHost(r)
	if err != nil {
		writeError(w, 404, "host_not_found", "主机不存在或无权访问")
		return
	}
	if host.Transport != "tailcat" {
		writeError(w, 400, "invalid_transport", "仅 Tailcat 主机支持导入端点更新包")
		return
	}
	var input struct {
		Update string `json:"update"`
	}
	if decodeJSON(r, &input) != nil || len(input.Update) > tunnel.MaxConnectionStringLen {
		writeError(w, 400, "invalid_endpoint_update", "请粘贴完整的端点更新包，最多 16 KiB")
		return
	}
	credential, err := a.store.CredentialByID(r.Context(), host.OwnerID, host.CredentialID)
	if err != nil {
		writeError(w, 500, "credential_unavailable", "无法读取主机凭据")
		return
	}
	plaintext, err := hostruntime.OpenCredential(a.vault, host.OwnerID, credential.ID, credential.Kind, credential.Ciphertext)
	if err != nil {
		writeError(w, 500, "credential_unavailable", "无法解密主机凭据")
		return
	}
	var secret hostruntime.TailcatCredential
	if json.Unmarshal(plaintext, &secret) != nil || secret.AgentID == "" || secret.ControllerID == "" || credential.Kind != "tailcat" {
		writeError(w, 409, "legacy_binding", "此主机使用旧版绑定，请先按安装教程升级接入")
		return
	}
	if secret.BindingID == "" {
		secret.BindingID, err = a.store.ActiveBindingForHost(r.Context(), host)
		if err != nil || secret.BindingID == "" {
			writeError(w, 409, "binding_unavailable", "无法确认已有绑定，更新包未导入")
			return
		}
	}
	raw := secret.RawFormalAddr
	if raw == "" {
		raw = secret.FormalTailcatAddr
	}
	// A retry after a lost response may re-submit the exact current revision.
	minimum := secret.EndpointVersion
	if minimum > 0 {
		minimum--
	}
	payload, err := tunnel.VerifyEndpointUpdate(input.Update, tunnel.EndpointIdentity{AgentID: secret.AgentID, ControllerID: secret.ControllerID, BindingID: secret.BindingID, ClientNode: secret.NodePrivate.Public().String(), Address: raw, SSHHostKey: host.HostKey, MinimumVersion: minimum})
	if err != nil {
		writeError(w, 409, "invalid_endpoint_update", "无法导入端点更新包："+err.Error())
		return
	}
	if payload.Revision == secret.EndpointVersion {
		if payload.Address != raw || payload.RelayProbeNode != secret.RelayProbeNode {
			writeError(w, 409, "endpoint_conflict", "此版本已导入其他地址，请重新生成更新包")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "revision": secret.EndpointVersion})
		return
	}
	dial, err := tunnel.DefaultSSRFValidator.ValidateDialAddr(payload.Address)
	if err != nil {
		writeError(w, 400, "invalid_endpoint_address", "中继地址未通过校验："+err.Error())
		return
	}
	secret.RawFormalAddr = payload.Address
	secret.RelayProbeNode = payload.RelayProbeNode
	secret.FormalTailcatAddr = payload.Address
	secret.DialFormalAddr = dial
	secret.EndpointVersion = payload.Revision
	encoded, err := json.Marshal(secret)
	if err != nil {
		writeError(w, 500, "credential_unavailable", "无法编码主机凭据")
		return
	}
	cipher, err := hostruntime.SealCredential(a.vault, host.OwnerID, credential.ID, credential.Kind, encoded)
	if err != nil {
		writeError(w, 500, "credential_unavailable", "无法保存主机凭据")
		return
	}
	if err := a.store.UpdateTailcatEndpoint(r.Context(), host, credential.Ciphertext, cipher); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, 409, "host_changed", "主机已被修改，请刷新后重新导入")
		} else {
			writeError(w, 500, "database_error", "保存失败，请重试导入")
		}
		return
	}
	a.hosts.CloseHost(host.ID)
	a.audit(r, "host.endpoint_updated", "host", host.ID, map[string]any{"revision": payload.Revision})
	writeJSON(w, 200, map[string]any{"ok": true, "revision": payload.Revision})
}
