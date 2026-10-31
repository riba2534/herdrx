package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
)

const minPasswordLength = 12

func validatePassword(password string) error {
	if len(password) < minPasswordLength {
		return &fieldError{fmt.Sprintf("密码至少需要 %d 个字符", minPasswordLength)}
	}
	return nil
}

// passwordChangeExempt lists the only authenticated requests a user whose
// password was reset by an administrator may make. Logout is outside the
// authenticated group and stays available as well.
func passwordChangeExempt(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/me":
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	case "/api/me/password":
		return r.Method == http.MethodPost
	}
	return false
}

func passwordChangeRequired(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "password_change_required", "管理员已重置你的密码，请先修改密码后再继续使用")
}

// changeExcept ends every Web access lease of the user except those of
// keepSessionID. Background leases (push monitors) are not affected; the
// account stays enabled. Like change, it never touches Herdr itself.
func (m *accessManager) changeExcept(userID, keepSessionID string, mutate func() error) error {
	return m.changeMatching(userID, mutate, func(lease *accessLease) bool {
		return lease.sessionID != "" && lease.sessionID != keepSessionID
	})
}

func (a *API) registerPasswordRoutes(router chi.Router) {
	router.With(a.requireCSRF, requireJSON).Post("/me/password", a.changePassword)
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	user, session := userFromContext(r.Context()), sessionFromContext(r.Context())
	ip := a.clientIP(r)
	if !a.limiter.allow("password-change-ip:"+ip, 60) {
		rateLimited(w)
		return
	}
	userKey := "password-change:" + user.ID
	if !a.limiter.allow(userKey, 10) {
		rateLimited(w)
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		decodeError(w, err)
		return
	}
	if input.CurrentPassword == "" {
		writeError(w, 400, "invalid_current_password", "请输入当前密码")
		return
	}
	if err := validatePassword(input.NewPassword); err != nil {
		writeError(w, 400, "invalid_password", "新"+err.Error())
		return
	}
	if input.NewPassword == input.CurrentPassword {
		writeError(w, 400, "password_unchanged", "新密码不能与当前密码相同，请换一个新密码")
		return
	}
	release, ok := a.hashSlot(r.Context(), false)
	if !ok {
		hashBusy(w)
		return
	}
	if !a.verifyPassword(user.PasswordHash, input.CurrentPassword) {
		release()
		if !failedCredentialDelay(r.Context()) {
			return
		}
		a.store.Audit(r.Context(), user.ID, "password.change_failed", "user", user.ID, ip, "{}")
		writeError(w, 400, "invalid_current_password", "当前密码不正确，请重新输入")
		return
	}
	hash, err := a.hashPassword(input.NewPassword)
	release()
	if err != nil {
		writeError(w, 500, "password_change_failed", "无法保存新密码，请稍后重试")
		return
	}
	event := a.auditEvent(r, "password.changed", "user", user.ID, nil)
	var revoked int64
	err = a.access.changeExcept(user.ID, session.ID, func() error {
		var mutateErr error
		revoked, mutateErr = a.store.ChangePassword(r.Context(), user.ID, session.ID, user.PasswordHash, hash, event)
		return mutateErr
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		a.authenticationError(w, err)
		return
	case errors.Is(err, store.ErrConflict):
		writeError(w, 409, "password_changed", "密码已在其他地方修改，请刷新页面后用最新密码重试")
		return
	case err != nil:
		writeError(w, 503, "database_error", "密码未修改，请稍后重试")
		return
	}
	a.limiter.clear(userKey)
	user.PasswordHash = hash
	user.MustChangePassword = false
	writeJSON(w, 200, map[string]any{"ok": true, "user": user, "revoked_sessions": revoked})
}

func (a *API) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "userID")
	actor := userFromContext(r.Context()).ID
	if id == actor {
		a.adminError(w, store.ErrSelfReset)
		return
	}
	temporary, err := secure.Token(18)
	if err != nil {
		writeError(w, 500, "password_reset_failed", "无法生成临时密码，请重试")
		return
	}
	release, ok := a.hashSlot(r.Context(), false)
	if !ok {
		hashBusy(w)
		return
	}
	hash, err := a.hashPassword(temporary)
	release()
	if err != nil {
		writeError(w, 500, "password_reset_failed", "无法生成临时密码，请重试")
		return
	}
	// The temporary password never enters the audit record, logs or database.
	event := a.auditEvent(r, "password.reset", "user", id, nil)
	// Push subscriptions end with the logins, so background leases end too.
	err = a.access.change(id, "", true, func() error { return a.store.ResetPassword(r.Context(), actor, id, hash, event) })
	if err != nil {
		a.adminError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"user_id": id, "temporary_password": temporary})
}

// failedCredentialDelay slows every rejected password the same way, so
// response timing does not tell a wrong password from an unknown account.
// It reports false when the caller went away during the delay.
func failedCredentialDelay(ctx context.Context) bool {
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
