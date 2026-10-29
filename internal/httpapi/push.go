package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/riba2534/herdrx/internal/push"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
)

type pushSubscriptionRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (a *API) pushRoutes(router chi.Router) {
	router.Get("/config", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"public_key": a.push.PublicKey()})
	})
	router.With(a.requireCSRF).Post("/subscriptions", a.subscribePush)
	// The endpoint is a capability URL, so it travels in the body, never the query string.
	router.With(a.requireCSRF, requireJSON).Post("/subscriptions/status", a.pushSubscriptionStatus)
}

// pushSubscriptionStatus reports whether this browser's subscription is still
// registered for the signed-in user. It only reads: a page uses it to leave
// background notifications to Web Push, and anything else (another user's or a
// deleted subscription) answers false so the page keeps notifying itself.
func (a *API) pushSubscriptionStatus(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Endpoint string `json:"endpoint"`
	}
	if decodeJSON(request, &input) != nil || input.Endpoint == "" || len(input.Endpoint) > 4096 {
		writeError(writer, http.StatusBadRequest, "invalid_subscription", "通知订阅无效，请重新启用浏览器通知")
		return
	}
	_, err := a.store.PushSubscriptionByEndpoint(request.Context(), userFromContext(request.Context()).ID, input.Endpoint)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(writer, http.StatusInternalServerError, "database_error", "暂时无法读取通知订阅，请稍后重试")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"subscribed": err == nil})
}

func (a *API) subscribePush(writer http.ResponseWriter, request *http.Request) {
	var input pushSubscriptionRequest
	if decodeJSON(request, &input) != nil || input.Endpoint == "" || input.Keys.P256DH == "" || input.Keys.Auth == "" || len(input.Endpoint) > 4096 {
		writeError(writer, http.StatusBadRequest, "invalid_subscription", "push subscription is invalid")
		return
	}
	if err := push.ValidateSubscription(input.Endpoint, input.Keys.P256DH, input.Keys.Auth); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_subscription", "通知订阅无效，请重新启用浏览器通知")
		return
	}
	id, _ := secure.Token(12)
	subscription := store.PushSubscription{
		ID: "push_" + id, UserID: userFromContext(request.Context()).ID, Endpoint: input.Endpoint,
		P256DH: input.Keys.P256DH, Auth: input.Keys.Auth,
	}
	if err := a.store.UpsertPushSubscription(request.Context(), subscription); err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "could not save push subscription")
		return
	}
	a.audit(request, "push_subscription.saved", "push_subscription", subscription.ID, nil)
	writeJSON(writer, http.StatusCreated, map[string]bool{"ok": true})
}
