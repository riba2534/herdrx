package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"golang.org/x/crypto/ssh"
)

type hostRequest struct {
	Name        string `json:"name"`
	Transport   string `json:"transport"`
	Hostname    string `json:"hostname"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	SessionName string `json:"session_name"`
	AuthMethod  string `json:"auth_method"`
	Secret      string `json:"secret"`
	Passphrase  string `json:"passphrase"`
	SSHKeyID    string `json:"ssh_key_id"`
	FolderID    string `json:"folder_id"`
	KeepSecret  bool   `json:"keep_secret"`
}

func (a *API) hostRoutes(router chi.Router) {
	router.Get("/", a.listHosts)
	router.With(a.requireCSRF).Post("/", a.createHost)
	router.Route("/{hostID}", func(router chi.Router) {
		router.Get("/", a.getHost)
		router.With(a.requireCSRF).Patch("/", a.renameHost)
		router.With(a.requireCSRF).Put("/ssh", a.updateSSHHost)
		router.With(a.requireCSRF).Post("/endpoint", a.refreshTailcatEndpoint)
		router.With(a.requireCSRF).Patch("/folder", a.moveHost)
		router.Get("/snapshot", a.hostSnapshot)
		router.With(a.requireOrigin).Get("/ws", a.workbench)
		router.With(a.requireCSRF).Post("/trust-host-key", a.trustHostKey)
		router.With(a.requireCSRF).Delete("/", a.deleteHost)
		router.With(a.requireCSRF).Post("/panes/{paneID}/paste-image", a.pasteImage)
	})
}

func (a *API) listHosts(writer http.ResponseWriter, request *http.Request) {
	hosts, err := a.store.ListHosts(request.Context(), userFromContext(request.Context()).ID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "could not list hosts")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"hosts": hosts})
}

func (a *API) getHost(writer http.ResponseWriter, request *http.Request) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "host not found")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"host": host})
}

func (a *API) renameHost(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "请填写有效的主机名称")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 || strings.ContainsFunc(name, unicode.IsControl) {
		writeError(writer, http.StatusBadRequest, "invalid_name", "主机名称须为 1–80 个字符，不能包含换行或控制字符")
		return
	}
	host, err := a.store.RenameHost(request.Context(), userFromContext(request.Context()).ID, chi.URLParam(request, "hostID"), name)
	if errors.Is(err, store.ErrNotFound) {
		writeError(writer, http.StatusNotFound, "host_not_found", "主机不存在或无权修改")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "无法保存主机名称，请重试")
		return
	}
	a.audit(request, "host.renamed", "host", host.ID, map[string]string{"name": host.Name})
	writeJSON(writer, http.StatusOK, map[string]any{"host": host})
}

func (a *API) createHost(writer http.ResponseWriter, request *http.Request) {
	var input hostRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "invalid host settings")
		return
	}
	user := userFromContext(request.Context())
	if input.Transport == "local" && user.Role != "admin" {
		writeError(writer, 403, "admin_required", "本机 Herdr 仅管理员可用，请添加自己的 SSH 或 Tailcat 主机")
		return
	}
	host, credential, publicKey, err := a.buildHost(request.Context(), user, input, nil)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_host", err.Error())
		return
	}
	if err := a.store.SaveHost(request.Context(), host, credential, nil); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(writer, 404, "reference_not_found", "选择的密钥或文件夹不存在，请刷新后重试")
			return
		}
		if errors.Is(err, store.ErrAdminRequired) {
			writeError(writer, 403, "admin_required", "本机 Herdr 仅管理员可用，请刷新页面后重试")
			return
		}
		writeError(writer, http.StatusInternalServerError, "database_error", "could not create host")
		return
	}
	host, err = a.store.HostByID(request.Context(), user.ID, host.ID)
	if err != nil {
		writeError(writer, 500, "database_error", "无法读取已保存的主机")
		return
	}
	a.audit(request, "host.created", "host", host.ID, map[string]string{"transport": host.Transport})
	writeJSON(writer, http.StatusCreated, map[string]any{"host": host, "public_key": publicKey})
}

func (a *API) buildHost(ctx context.Context, user store.User, input hostRequest, previous *store.Host) (store.Host, *store.Credential, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	fail := func(message string) (store.Host, *store.Credential, string, error) {
		return store.Host{}, nil, "", fmt.Errorf("%s", message)
	}
	if !validResourceName(input.Name) {
		return fail("主机名称须为 1–80 个字符，不能包含换行或控制字符")
	}
	id, err := secure.Token(12)
	if err != nil {
		return fail("无法创建主机，请重试")
	}
	host := store.Host{ID: "hst_" + id, OwnerID: user.ID, Name: input.Name, Transport: input.Transport, SessionName: strings.TrimSpace(input.SessionName), Port: input.Port, FolderID: input.FolderID}
	if host.Port == 0 && input.AuthMethod != "system_ssh" {
		host.Port = 22
	}
	if host.Transport == "local" {
		if user.Role != "admin" {
			return fail("本机 Herdr 仅管理员可用")
		}
		return host, nil, "", nil
	}
	if host.Transport != "ssh" {
		return fail("请选择 SSH 或本机连接")
	}
	host.Hostname, host.Username, host.AuthMethod = strings.TrimSpace(input.Hostname), strings.TrimSpace(input.Username), input.AuthMethod
	systemSSH := host.AuthMethod == "system_ssh"
	if systemSSH && (user.Role != "admin" || user.Disabled || a.config.SSHBinary == "") {
		return fail("system OpenSSH 仅管理员可用，请先配置 HERDRX_SSH_BIN")
	}
	if host.Hostname == "" || (!systemSSH && (host.Username == "" || host.Port == 0)) || host.Port < 0 || host.Port > 65535 || strings.ContainsAny(host.Hostname, " /\\\t\r\n") || strings.ContainsFunc(host.Username, unicode.IsControl) {
		return fail("请填写有效的主机地址、SSH 用户和 1–65535 之间的端口")
	}
	if ip := net.ParseIP(host.Hostname); ip != nil && !a.config.AllowPrivateHosts && isPrivateAddress(ip) {
		return fail("管理员已禁用内网主机连接")
	}
	if len(host.Hostname) > 253 || len(host.Username) > 128 || len(host.SessionName) > 128 || strings.ContainsFunc(host.SessionName, unicode.IsControl) {
		return fail("主机地址、用户或会话名称无效")
	}
	if previous != nil {
		host.ID = previous.ID
		if host.Hostname == previous.Hostname && host.Port == previous.Port {
			host.HostKey, host.PendingHostKey = previous.HostKey, previous.PendingHostKey
		}
	}
	if systemSSH {
		if input.Secret != "" || input.Passphrase != "" || input.SSHKeyID != "" || input.KeepSecret {
			return fail("system OpenSSH 使用工作台服务账号的配置和 ticket，请勿填写网站凭据")
		}
		host.HostKey, host.PendingHostKey = "", ""
		return host, nil, "", nil
	}
	if input.AuthMethod == "saved_key" {
		key, err := a.store.SSHKeyByID(ctx, user.ID, input.SSHKeyID)
		if err != nil {
			return fail("选择的认证密钥不存在或无权使用")
		}
		host.CredentialID, host.SSHKeyID = key.ID, key.ID
		return host, nil, "", nil
	}
	if input.KeepSecret {
		if previous == nil || previous.AuthMethod != input.AuthMethod || previous.CredentialID == "" || input.Secret != "" || input.Passphrase != "" {
			return fail("请重新填写认证凭据")
		}
		host.CredentialID = previous.CredentialID
		return host, nil, "", nil
	}
	credentialID, err := secure.Token(12)
	if err != nil {
		return fail("无法保存凭据，请重试")
	}
	credentialID = "cred_" + credentialID
	var secret []byte
	var publicKey string
	switch input.AuthMethod {
	case "generated":
		private, public, err := secure.GenerateSSHKey("herdrx:" + host.ID)
		if err != nil {
			return fail("无法生成 SSH 密钥")
		}
		secret, publicKey = private, strings.TrimSpace(string(public))
	case "private_key", "private_key_bundle":
		material := secure.SSHKeyMaterial{PrivateKey: input.Secret, Passphrase: input.Passphrase}
		if _, _, err := material.Parse(); err != nil {
			return fail(err.Error())
		}
		secret, _ = json.Marshal(material)
		host.AuthMethod = "private_key_bundle"
	case "password":
		if input.Secret == "" || len(input.Secret) > 4096 {
			return fail("请填写有效的 SSH 密码")
		}
		secret = []byte(input.Secret)
	default:
		return fail("请选择密码、已保存密钥或私钥认证")
	}
	ciphertext, err := hostruntime.SealCredential(a.vault, user.ID, credentialID, host.AuthMethod, secret)
	if err != nil {
		return fail("无法加密 SSH 凭据")
	}
	credential := &store.Credential{ID: credentialID, OwnerID: user.ID, Kind: host.AuthMethod, Ciphertext: ciphertext}
	host.CredentialID = credentialID
	return host, credential, publicKey, nil
}

func (a *API) updateSSHHost(w http.ResponseWriter, r *http.Request) {
	previous, err := a.ownedHost(r)
	if err != nil {
		writeError(w, 404, "host_not_found", "主机不存在或无权修改")
		return
	}
	if previous.Transport != "ssh" {
		writeError(w, 400, "invalid_transport", "只有 SSH 主机可以修改连接认证")
		return
	}
	var input hostRequest
	if decodeJSON(r, &input) != nil || input.Transport != "ssh" {
		writeError(w, 400, "invalid_request", "请填写有效的 SSH 连接配置")
		return
	}
	host, credential, publicKey, err := a.buildHost(r.Context(), userFromContext(r.Context()), input, &previous)
	if err != nil {
		writeError(w, 400, "invalid_host", err.Error())
		return
	}
	if err = a.store.SaveHost(r.Context(), host, credential, &previous); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, 409, "host_changed", "主机已被修改，请刷新后重试")
		} else if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, "reference_not_found", "密钥或文件夹不存在，请刷新后重试")
		} else {
			writeError(w, 500, "database_error", "无法保存 SSH 配置，请重试")
		}
		return
	}
	a.hosts.CloseHost(host.ID)
	host, err = a.store.HostByID(r.Context(), host.OwnerID, host.ID)
	if err != nil {
		writeError(w, 500, "database_error", "无法读取已保存的主机")
		return
	}
	a.audit(r, "host.ssh_updated", "host", host.ID, map[string]string{"auth_method": host.AuthMethod})
	writeJSON(w, 200, map[string]any{"host": host, "public_key": publicKey})
}

func (a *API) hostSnapshot(writer http.ResponseWriter, request *http.Request) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "host not found")
		return
	}
	connectCtx, cancelConnect := context.WithTimeout(request.Context(), a.config.HostDialTimeout)
	endpoint, err := a.hosts.Open(connectCtx, host)
	cancelConnect()
	if err != nil {
		var unknown *herdr.UnknownHostKeyError
		if errors.As(err, &unknown) {
			writeJSON(writer, http.StatusConflict, map[string]any{"error": "SSH host key confirmation required", "code": "host_key_unknown", "fingerprint": unknown.Fingerprint})
			return
		}
		writeHostConnectionError(writer, err)
		return
	}
	defer endpoint.Close()
	// A cold Tailcat/SSH connection must not consume the RPC's entire budget.
	// Match the workbench WebSocket path: begin the snapshot timeout only once
	// the shared transport is ready, while still honoring request cancellation.
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	snapshot, err := endpoint.Snapshot(ctx)
	if err != nil {
		if request.Context().Err() == nil {
			a.hosts.CloseHost(host.ID)
		}
		writeError(writer, http.StatusBadGateway, "herdr_unavailable", err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"snapshot": snapshot})
}

func (a *API) trustHostKey(writer http.ResponseWriter, request *http.Request) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "主机不存在或无权访问")
		return
	}
	if host.PendingHostKey == "" {
		writeError(writer, http.StatusConflict, "no_pending_host_key", "connect once before trusting a host key")
		return
	}
	if err := a.store.TrustHostKey(request.Context(), host.OwnerID, host.ID, host.PendingHostKey); err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "could not trust host key")
		return
	}
	a.hosts.CloseHost(host.ID)
	a.audit(request, "host_key.trusted", "host", host.ID, map[string]string{"fingerprint": fingerprint(host.PendingHostKey)})
	writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) deleteHost(writer http.ResponseWriter, request *http.Request) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "host not found")
		return
	}
	a.hosts.CloseHost(host.ID)
	if err := a.store.DeleteHost(request.Context(), host.OwnerID, host.ID); err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "could not delete host")
		return
	}
	a.audit(request, "host.deleted", "host", host.ID, nil)
	writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) ownedHost(request *http.Request) (store.Host, error) {
	host, err := a.store.HostByID(request.Context(), userFromContext(request.Context()).ID, chi.URLParam(request, "hostID"))
	if err == nil {
		if lease, _ := request.Context().Value(accessContextKey).(*accessLease); lease != nil {
			lease.hostID.Store(&host.ID)
		}
	}
	return host, err
}

func isPrivateAddress(address net.IP) bool {
	return address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified()
}

func fingerprint(encoded string) string {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(encoded))
	if err != nil {
		return "invalid"
	}
	return ssh.FingerprintSHA256(key)
}
