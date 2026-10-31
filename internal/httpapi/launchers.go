package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/riba2534/herdrx/internal/launchers"
)

func (a *API) registerLauncherRoutes(router chi.Router) {
	router.Get("/me/launchers", a.getLaunchers)
	router.With(a.requireCSRF, requireJSON).Put("/me/launchers", a.putLaunchers)
}

func (a *API) getLaunchers(w http.ResponseWriter, r *http.Request) {
	document, err := a.launcherDocument(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_error", "无法读取启动项")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"launchers": document.Launchers})
}

func (a *API) putLaunchers(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		decodeError(w, err)
		return
	}
	document, canonical, err := launchers.Parse(raw)
	if err != nil {
		var invalid *launchers.ValidationError
		if errors.As(err, &invalid) {
			writeError(w, http.StatusBadRequest, "invalid_launchers", invalid.Message)
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_launchers", "启动项无法保存")
		return
	}
	if err := a.store.SetUserLaunchers(r.Context(), userFromContext(r.Context()).ID, string(canonical)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_error", "无法保存启动项")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"launchers": document.Launchers})
}

func (a *API) launcherDocument(r *http.Request) (launchers.Document, error) {
	raw, err := a.store.UserLaunchers(r.Context(), userFromContext(r.Context()).ID)
	if err != nil {
		return launchers.Document{}, err
	}
	if raw == "" {
		return launchers.Document{Launchers: []launchers.Launcher{}}, nil
	}
	document, _, err := launchers.Parse([]byte(raw))
	if err != nil {
		// A document that no longer parses should not hide the built-in menu.
		return launchers.Document{Launchers: []launchers.Launcher{}}, nil
	}
	return document, nil
}
