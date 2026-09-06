package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
)

func (a *API) adminRoutes(r chi.Router) {
	r.Use(a.adminOnly)
	r.Get("/settings", a.getInstanceSettings)
	r.With(a.requireCSRF, requireJSON).Patch("/settings", a.updateInstanceSettings)
	r.Get("/users", a.listUsers)
	r.With(a.requireCSRF, requireJSON).Patch("/users/{userID}", a.setUserDisabled)
	r.Get("/users/{userID}/sessions", a.listUserSessions)
	r.With(a.requireCSRF).Delete("/users/{userID}/sessions", a.revokeUserSessions)
	r.With(a.requireCSRF).Delete("/users/{userID}/sessions/{sessionID}", a.revokeUserSessions)
	r.Get("/invites", a.listInvites)
	r.With(a.requireCSRF).Post("/invites", a.createInvite)
	r.With(a.requireCSRF).Delete("/invites/{inviteID}", a.revokeInvite)
	r.Get("/audit", a.listAudit)
}
func (a *API) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userFromContext(r.Context()).Role != "admin" {
			writeError(w, 403, "admin_required", "此操作需要管理员权限")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func readPage(w http.ResponseWriter, r *http.Request) (store.Page, bool) {
	page := store.Page{Limit: 50}
	for key, target := range map[string]*int{"limit": &page.Limit, "offset": &page.Offset} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 {
				writeError(w, 400, "invalid_page", "分页参数不正确")
				return page, false
			}
			*target = value
		}
	}
	if page.Limit < 1 || page.Limit > 100 || page.Offset > 1000000 {
		writeError(w, 400, "invalid_page", "每页数量为 1–100，偏移最多为 1000000")
		return page, false
	}
	return page, true
}
func writePage[T any](w http.ResponseWriter, key string, items []T, page store.Page) {
	more := len(items) > page.Limit
	if more {
		items = items[:page.Limit]
	}
	if items == nil {
		items = make([]T, 0)
	}
	writeJSON(w, 200, map[string]any{key: items, "has_more": more, "offset": page.Offset, "limit": page.Limit, "next_offset": page.Offset + len(items)})
}
func (a *API) adminError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRegistrationClosed):
		writeError(w, 403, "registration_closed", "当前已关闭注册，请先在管理页面开启注册")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "not_found", "目标不存在或不属于该用户")
	case errors.Is(err, store.ErrAdminRequired):
		writeError(w, 403, "admin_required", "管理员权限已失效，请重新登录")
	case errors.Is(err, store.ErrSelfDisable):
		writeError(w, 409, "self_disable", "不能禁用当前登录的管理员")
	case errors.Is(err, store.ErrLastAdmin):
		writeError(w, 409, "last_admin", "必须保留至少一位可用管理员")
	case errors.Is(err, store.ErrConflict):
		writeError(w, 409, "conflict", "当前状态不允许此操作，请刷新后重试")
	default:
		writeError(w, 503, "database_error", "操作未完成，请稍后重试")
	}
}
func (a *API) auditEvent(r *http.Request, action, kind, id string, details any) store.AuditEvent {
	encoded, _ := json.Marshal(map[string]any{"request_id": middleware.GetReqID(r.Context()), "changes": details})
	return store.AuditEvent{UserID: userFromContext(r.Context()).ID, Action: action, TargetType: kind, TargetID: id, RemoteIP: a.clientIP(r), Details: string(encoded)}
}
func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	page, ok := readPage(w, r)
	if !ok {
		return
	}
	filter := store.UserFilter{Query: strings.TrimSpace(r.URL.Query().Get("q")), Role: r.URL.Query().Get("role"), Status: r.URL.Query().Get("status")}
	if len(filter.Query) > 254 || (filter.Role != "" && filter.Role != "admin" && filter.Role != "user") || (filter.Status != "" && filter.Status != "enabled" && filter.Status != "disabled") {
		writeError(w, 400, "invalid_filter", "请输入有效的用户筛选条件")
		return
	}
	items, err := a.store.ListUsers(r.Context(), page, filter)
	if err != nil {
		a.adminError(w, err)
		return
	}
	writePage(w, "users", items, page)
}
func (a *API) setUserDisabled(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Disabled *bool `json:"disabled"`
	}
	if err := decodeJSON(r, &input); err != nil {
		decodeError(w, err)
		return
	}
	if input.Disabled == nil {
		writeError(w, 400, "invalid_request", "需要明确指定账号是否禁用")
		return
	}
	id := chi.URLParam(r, "userID")
	actor := userFromContext(r.Context()).ID
	event := a.auditEvent(r, "user.enabled", "user", id, nil)
	if *input.Disabled {
		event.Action = "user.disabled"
	}
	mutate := func() error { return a.store.SetUserDisabled(r.Context(), actor, id, *input.Disabled, event) }
	var err error
	if *input.Disabled {
		err = a.access.change(id, "", true, mutate)
	} else {
		err = mutate()
	}
	if err != nil {
		a.adminError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *API) listUserSessions(w http.ResponseWriter, r *http.Request) {
	page, ok := readPage(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "userID")
	if _, err := a.store.UserByID(r.Context(), id); err != nil {
		a.adminError(w, err)
		return
	}
	items, err := a.store.ListSessions(r.Context(), id, page)
	if err != nil {
		a.adminError(w, err)
		return
	}
	writePage(w, "sessions", items, page)
}
func (a *API) revokeUserSessions(w http.ResponseWriter, r *http.Request) {
	id, sid := chi.URLParam(r, "userID"), chi.URLParam(r, "sessionID")
	action := "sessions.revoked"
	if sid != "" {
		action = "session.revoked"
	}
	event := a.auditEvent(r, action, "user", id, map[string]any{"session_id": sid})
	err := a.access.change(id, sid, false, func() error {
		return a.store.RevokeUserSessions(r.Context(), userFromContext(r.Context()).ID, id, sid, event)
	})
	if err != nil {
		a.adminError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *API) listInvites(w http.ResponseWriter, r *http.Request) {
	page, ok := readPage(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListInvites(r.Context(), page)
	if err != nil {
		a.adminError(w, err)
		return
	}
	writePage(w, "invites", items, page)
}
func (a *API) createInvite(w http.ResponseWriter, r *http.Request) {
	code, err := secure.Token(24)
	if err != nil {
		writeError(w, 500, "invite_failed", "无法生成邀请")
		return
	}
	id, err := secure.Token(12)
	if err != nil {
		writeError(w, 500, "invite_failed", "无法生成邀请")
		return
	}
	invite := store.Invite{ID: "inv_" + id, CreatedBy: userFromContext(r.Context()).ID, ExpiresAt: time.Now().Add(7 * 24 * time.Hour), CreatedAt: time.Now(), Status: "active"}
	event := a.auditEvent(r, "invite.created", "invite", invite.ID, map[string]any{"expires_at": invite.ExpiresAt})
	if err := a.store.CreateInvite(r.Context(), invite, secure.TokenHash(code), event); err != nil {
		a.adminError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"invite": invite, "code": code})
}

func (a *API) getInstanceSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := a.store.Settings(r.Context())
	if err != nil {
		a.adminError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"settings": settings})
}

func (a *API) updateInstanceSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Registration string `json:"registration"`
		Revision     *int64 `json:"revision"`
	}
	if err := decodeJSON(r, &input); err != nil {
		decodeError(w, err)
		return
	}
	if (input.Registration != "closed" && input.Registration != "invite") || input.Revision == nil || *input.Revision < 0 {
		writeError(w, 400, "invalid_settings", "请选择注册状态，并刷新页面获取当前设置")
		return
	}
	event := a.auditEvent(r, "registration.changed", "instance", "registration", map[string]any{"registration": input.Registration})
	settings, err := a.store.SetRegistration(r.Context(), userFromContext(r.Context()).ID, input.Registration, *input.Revision, event)
	if err != nil {
		a.adminError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"settings": settings})
}
func (a *API) revokeInvite(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "inviteID")
	event := a.auditEvent(r, "invite.revoked", "invite", id, nil)
	if err := a.store.RevokeInvite(r.Context(), userFromContext(r.Context()).ID, id, event); err != nil {
		a.adminError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	page, ok := readPage(w, r)
	if !ok {
		return
	}
	f := store.AuditFilter{Page: page, UserID: r.URL.Query().Get("user_id"), Action: r.URL.Query().Get("action")}
	if len(f.UserID) > 128 || len(f.Action) > 80 {
		writeError(w, 400, "invalid_filter", "筛选条件过长")
		return
	}
	for key, target := range map[string]*time.Time{"since": &f.Since, "until": &f.Until} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeError(w, 400, "invalid_filter", "时间需要使用 RFC3339 格式")
				return
			}
			*target = value
		}
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && f.Until.Before(f.Since) {
		writeError(w, 400, "invalid_filter", "结束时间不能早于开始时间")
		return
	}
	items, err := a.store.ListAudit(r.Context(), f)
	if err != nil {
		a.adminError(w, err)
		return
	}
	writePage(w, "events", items, page)
}
