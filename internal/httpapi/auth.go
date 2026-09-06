package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
)

type credentialsRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Token       string `json:"token,omitempty"`
	InviteCode  string `json:"invite_code,omitempty"`
}

func (a *API) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	count, err := a.store.UserCount(r.Context())
	if err != nil {
		writeError(w, 500, "database_error", "无法读取初始化状态")
		return
	}
	settings, err := a.store.Settings(r.Context())
	if err != nil {
		writeError(w, 503, "database_error", "无法读取注册设置，请稍后重试")
		return
	}
	mode := settings.Registration
	if count == 0 {
		mode = "closed"
	}
	writeJSON(w, 200, map[string]any{"required": count == 0, "registration": mode})
}

func (a *API) bootstrap(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.allow("bootstrap:"+a.clientIP(r), 5) {
		rateLimited(w)
		return
	}
	count, err := a.store.UserCount(r.Context())
	if err != nil {
		writeError(w, 503, "database_error", "暂时无法初始化，请稍后重试")
		return
	}
	a.bootstrapMu.Lock()
	token := a.bootstrapToken
	a.bootstrapMu.Unlock()
	if count != 0 || token == "" {
		writeError(w, 409, "already_bootstrapped", "管理员已经创建，请登录")
		return
	}
	var input credentialsRequest
	if err := decodeJSON(r, &input); err != nil {
		decodeError(w, err)
		return
	}
	if len(input.Token) != len(token) || subtle.ConstantTimeCompare([]byte(input.Token), []byte(token)) != 1 {
		writeError(w, 401, "invalid_bootstrap_token", "初始化令牌不正确")
		return
	}
	if err := validateNewUser(&input); err != nil {
		userError(w, err)
		return
	}
	release, ok := a.hashSlot(r.Context(), true)
	if !ok {
		hashBusy(w)
		return
	}
	user, err := a.createUserRecord(input, "admin")
	release()
	if err != nil {
		userError(w, err)
		return
	}
	event := a.auditEvent(r, "bootstrap.completed", "user", user.ID, nil)
	event.UserID = user.ID
	if err := a.store.BootstrapUser(r.Context(), user, event); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, 409, "already_bootstrapped", "管理员已经创建，请登录")
		} else {
			writeError(w, 500, "database_error", "创建管理员失败，请重试")
		}
		return
	}
	a.bootstrapMu.Lock()
	a.bootstrapToken = ""
	a.bootstrapMu.Unlock()
	if err := os.Remove(filepath.Join(a.config.DataDir, "bootstrap-token")); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.logger.Warn("could not remove consumed bootstrap token file", "error", err)
	}
	a.issueSession(w, r, user)
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	settings, err := a.store.Settings(r.Context())
	if err != nil {
		writeError(w, 503, "database_error", "无法读取注册设置，请稍后重试")
		return
	}
	if settings.Registration != "invite" {
		writeError(w, 403, "registration_closed", "当前实例已关闭注册")
		return
	}
	if !a.limiter.allow("register:"+a.clientIP(r), 5) {
		rateLimited(w)
		return
	}
	var input credentialsRequest
	if err := decodeJSON(r, &input); err != nil {
		decodeError(w, err)
		return
	}
	input.InviteCode = strings.TrimSpace(input.InviteCode)
	if input.InviteCode == "" || len(input.InviteCode) > 256 {
		writeError(w, 400, "invalid_invite", "请输入有效的一次性邀请码")
		return
	}
	if err := validateNewUser(&input); err != nil {
		userError(w, err)
		return
	}
	codeHash := secure.TokenHash(input.InviteCode)
	if err := a.store.CheckInvite(r.Context(), codeHash); err != nil {
		registrationError(w, err)
		return
	}
	release, ok := a.hashSlot(r.Context(), true)
	if !ok {
		hashBusy(w)
		return
	}
	user, err := a.createUserRecord(input, "user")
	release()
	if err != nil {
		userError(w, err)
		return
	}
	if err := a.store.ConsumeInviteAndCreateUser(r.Context(), codeHash, user, settings.Revision); err != nil {
		registrationError(w, err)
		return
	}
	a.issueSession(w, r, user)
}

func registrationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRegistrationClosed):
		writeError(w, 403, "registration_closed", "管理员已关闭注册，请联系管理员")
	case errors.Is(err, store.ErrConflict):
		writeError(w, 409, "registration_changed", "注册设置已变更，请刷新页面后重试；邀请码尚未使用")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 400, "invalid_invite", "邀请码无效、已使用、已撤销或已过期")
	case errors.Is(err, store.ErrEmailExists):
		writeError(w, 409, "email_exists", "此邮箱已注册，请返回登录；邀请码尚未使用")
	default:
		writeError(w, 503, "database_error", "暂时无法完成注册，请稍后重试")
	}
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	if !a.limiter.allow("login-ip:"+ip, 60) {
		rateLimited(w)
		return
	}
	var input credentialsRequest
	if err := decodeJSON(r, &input); err != nil {
		decodeError(w, err)
		return
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if len(input.Email) > 254 {
		writeError(w, 400, "invalid_request", "邮箱过长")
		return
	}
	pairKey := "login-account:" + ip + ":" + input.Email
	if !a.limiter.allow(pairKey, 10) {
		rateLimited(w)
		return
	}
	user, err := a.store.UserByEmail(r.Context(), input.Email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 503, "database_error", "暂时无法登录，请稍后重试")
		return
	}
	valid := false
	if err == nil && !user.Disabled {
		release, ok := a.hashSlot(r.Context(), false)
		if !ok {
			hashBusy(w)
			return
		}
		valid = a.verifyPassword(user.PasswordHash, input.Password)
		release()
	}
	if !valid {
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
		a.store.Audit(r.Context(), "", "login.failed", "", "", ip, "{}")
		writeError(w, 401, "invalid_credentials", "邮箱或密码不正确")
		return
	}
	a.limiter.clear(pairKey)
	a.issueSession(w, r, user)
}

func (a *API) issueSession(w http.ResponseWriter, r *http.Request, user store.User) {
	token, err := secure.Token(32)
	if err != nil {
		writeError(w, 500, "session_failed", "无法创建登录会话")
		return
	}
	csrf, err := secure.Token(24)
	if err != nil {
		writeError(w, 500, "session_failed", "无法创建登录会话")
		return
	}
	id, err := secure.Token(16)
	if err != nil {
		writeError(w, 500, "session_failed", "无法创建登录会话")
		return
	}
	session := store.Session{ID: "ses_" + id, UserID: user.ID, CSRFToken: csrf, ExpiresAt: time.Now().Add(a.config.SessionTTL)}
	event := a.auditEvent(r, "login.succeeded", "session", session.ID, nil)
	event.UserID = user.ID
	agent := r.UserAgent()
	if len(agent) > 1024 {
		agent = agent[:1024]
	}
	gate := a.access.gate(user.ID)
	gate.Lock()
	err = a.store.CreateSession(r.Context(), session, secure.TokenHash(token), agent, a.clientIP(r), event)
	gate.Unlock()
	if err != nil {
		a.authenticationError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: a.config.CookieSecure, SameSite: http.SameSiteLaxMode, Expires: session.ExpiresAt})
	writeJSON(w, 200, map[string]any{"user": user, "csrf_token": csrf, "session_id": session.ID})
}

func validateNewUser(input *credentialsRequest) error {
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || len(input.Email) > 254 {
		return &fieldError{"请输入有效的邮箱地址"}
	}
	if input.DisplayName == "" || utf8.RuneCountInString(input.DisplayName) > 80 {
		return &fieldError{"显示名称需要 1–80 个字符"}
	}
	if len(input.Password) < 12 {
		return &fieldError{"密码至少需要 12 个字符"}
	}
	return nil
}

func (a *API) createUserRecord(input credentialsRequest, role string) (store.User, error) {
	if err := validateNewUser(&input); err != nil {
		return store.User{}, err
	}
	hash, err := a.hashPassword(input.Password)
	if err != nil {
		return store.User{}, err
	}
	id, err := secure.Token(16)
	if err != nil {
		return store.User{}, err
	}
	return store.User{ID: "usr_" + id, Email: input.Email, DisplayName: input.DisplayName, Role: role, PasswordHash: hash, CreatedAt: time.Now().UTC()}, nil
}

type fieldError struct{ message string }

func (e *fieldError) Error() string { return e.message }
func userError(w http.ResponseWriter, err error) {
	var field *fieldError
	if errors.As(err, &field) {
		writeError(w, 400, "invalid_user", field.Error())
	} else {
		writeError(w, 500, "user_creation_failed", "创建账号失败，请稍后重试")
	}
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	writeJSON(w, 200, map[string]any{"user": userFromContext(r.Context()), "csrf_token": session.CSRFToken, "session_id": session.ID})
}

func (a *API) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: a.config.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	session, user, err := a.lookupSession(r)
	if errors.Is(err, store.ErrNotFound) {
		a.clearCookie(w)
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if err != nil {
		a.authenticationError(w, err)
		return
	}
	want, got := session.CSRFToken, r.Header.Get("X-CSRF-Token")
	if want == "" || len(want) != len(got) || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		writeError(w, 403, "csrf_failed", "登录状态已变化，请刷新后重试")
		return
	}
	event := a.auditEvent(r, "logout.completed", "session", session.ID, nil)
	event.UserID = user.ID
	err = a.access.change(user.ID, session.ID, false, func() error { return a.store.DeleteSession(r.Context(), session.ID, user.ID, event) })
	if err != nil {
		writeError(w, 503, "logout_failed", "退出未完成，请重试")
		return
	}
	a.clearCookie(w)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
