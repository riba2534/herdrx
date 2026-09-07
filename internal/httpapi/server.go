package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/push"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
)

const sessionCookie = "herdrx_session"

var Version = "dev"

type hostPool interface {
	Open(context.Context, store.Host) (herdr.Endpoint, error)
	CloseHost(string)
	Close()
}

type API struct {
	config             config.Config
	store              *store.Store
	vault              *secure.Vault
	assets             fs.FS
	bootstrapToken     string
	logger             *slog.Logger
	hosts              hostPool
	push               *push.Service
	stopBackground     context.CancelFunc
	bootstrapMu        sync.Mutex
	limiter            requestLimiter
	passwordHashes     chan struct{}
	registrationHashes chan struct{}
	access             *accessManager
	hashPassword       func(string) (string, error)
	verifyPassword     func(string, string) bool
	enrollmentMu       sync.Mutex
	enrollmentRunning  map[string]bool
	cliReleases        *cliReleaseCache
}

func New(cfg config.Config, dataStore *store.Store, vault *secure.Vault, assets fs.FS, logger *slog.Logger) (*API, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	api := &API{
		config: cfg, store: dataStore, vault: vault, assets: assets, logger: logger,
		hosts:              &hostruntime.Factory{Config: cfg, Store: dataStore, Vault: vault},
		passwordHashes:     make(chan struct{}, cfg.AuthHashConcurrency),
		registrationHashes: make(chan struct{}, 1),
		access:             newAccessManager(dataStore),
		hashPassword:       secure.HashPassword, verifyPassword: secure.VerifyPassword,
		enrollmentRunning: make(map[string]bool),
		cliReleases:       newCLIReleaseCache(),
	}
	count, err := dataStore.UserCount(context.Background())
	if err != nil {
		return nil, err
	}
	if count == 0 {
		api.bootstrapToken = cfg.BootstrapToken
		if api.bootstrapToken == "" {
			path := filepath.Join(cfg.DataDir, "bootstrap-token")
			if existing, readErr := os.ReadFile(path); readErr == nil && strings.TrimSpace(string(existing)) != "" {
				api.bootstrapToken = strings.TrimSpace(string(existing))
			} else {
				api.bootstrapToken, err = secure.Token(24)
				if err != nil {
					return nil, err
				}
				file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if createErr != nil {
					return nil, fmt.Errorf("create bootstrap token: %w", createErr)
				}
				if _, writeErr := file.WriteString(api.bootstrapToken + "\n"); writeErr != nil {
					_ = file.Close()
					return nil, fmt.Errorf("write bootstrap token: %w", writeErr)
				}
				if closeErr := file.Close(); closeErr != nil {
					return nil, fmt.Errorf("close bootstrap token: %w", closeErr)
				}
			}
		}
		logger.Warn("bootstrap required", "token_file", filepath.Join(cfg.DataDir, "bootstrap-token"))
	}
	pushService, err := push.Open(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open Web Push service: %w", err)
	}
	api.push = pushService
	background, cancel := context.WithCancel(context.Background())
	api.stopBackground = cancel
	go api.monitorPush(background)
	return api, nil
}

func (a *API) Close() {
	a.access.close()
	if a.stopBackground != nil {
		a.stopBackground()
	}
	a.hosts.Close()
	if a.push != nil {
		a.push.Close()
	}
}

func (a *API) Handler() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(a.securityHeaders)
	router.Get("/healthz", a.health)
	router.Get("/install.sh", a.cliInstall)
	router.Route("/api", func(router chi.Router) {
		router.Use(a.guardWrites)
		router.Get("/bootstrap/status", a.bootstrapStatus)
		router.With(requireJSON).Post("/bootstrap", a.bootstrap)
		router.With(requireJSON).Post("/login", a.login)
		router.With(requireJSON).Post("/register", a.register)
		router.Post("/logout", a.logout)
		router.Group(func(router chi.Router) {
			router.Use(a.authenticate)
			router.Get("/me", a.me)
			router.Get("/cli-release", a.cliRelease)
			router.Route("/hosts", a.hostRoutes)
			router.Route("/ssh-keys", a.sshKeyRoutes)
			router.Route("/host-folders", a.hostFolderRoutes)
			router.Route("/tailcat", a.tailcatRoutes)
			router.Route("/push", a.pushRoutes)
			router.Route("/admin", a.adminRoutes)
		})
	})
	router.Handle("/*", a.frontend())
	return router
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/") {
			writer.Header().Set("Cache-Control", "no-store")
		}
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; connect-src 'self' ws: wss:; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(writer, request)
	})
}

func (a *API) health(writer http.ResponseWriter, request *http.Request) {
	if err := a.store.Ping(request.Context()); err != nil {
		writeError(writer, http.StatusServiceUnavailable, "database_unavailable", "database unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "version": Version})
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		session, user, err := a.lookupSession(request)
		if err != nil {
			a.authenticationError(writer, err)
			return
		}
		lease, err := a.access.attach(request.Context(), user.ID, session.ID)
		if err != nil {
			a.authenticationError(writer, err)
			return
		}
		defer lease.release()
		ctx := context.WithValue(lease.ctx, sessionContextKey, session)
		ctx = context.WithValue(ctx, userContextKey, user)
		ctx = context.WithValue(ctx, accessContextKey, lease)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func (a *API) lookupSession(request *http.Request) (store.Session, store.User, error) {
	cookie, err := request.Cookie(sessionCookie)
	if err != nil || len(cookie.Value) < 32 || len(cookie.Value) > 256 {
		return store.Session{}, store.User{}, store.ErrNotFound
	}
	session, user, err := a.store.SessionByTokenHash(request.Context(), secure.TokenHash(cookie.Value))
	if err != nil {
		return session, user, err
	}
	if !session.ExpiresAt.After(time.Now()) || user.Disabled {
		return session, user, store.ErrNotFound
	}
	return session, user, nil
}

func (a *API) authenticationError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		// A delayed 401 from an old request must not erase a newer login Cookie.
		// Only explicit logout clears the browser cookie.
		writeError(w, http.StatusUnauthorized, "unauthenticated", "登录已失效，请重新登录")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "暂时无法验证登录状态，请稍后重试")
}

func (a *API) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		want := sessionFromContext(request.Context()).CSRFToken
		got := request.Header.Get("X-CSRF-Token")
		if want == "" || len(want) != len(got) || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
			writeError(writer, http.StatusForbidden, "csrf_failed", "refresh the page and try again")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (a *API) frontend() http.Handler {
	if a.assets == nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(a.assets))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := strings.TrimPrefix(request.URL.Path, "/")
		if path != "" {
			if _, err := fs.Stat(a.assets, path); err == nil {
				if strings.HasPrefix(path, "assets/") {
					writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else if path == "sw.js" || path == "manifest.webmanifest" {
					writer.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
				}
				fileServer.ServeHTTP(writer, request)
				return
			}
			if strings.HasPrefix(path, "assets/") || filepath.Ext(path) != "" {
				http.NotFound(writer, request)
				return
			}
		}
		writer.Header().Set("Cache-Control", "no-store")
		request.URL.Path = "/"
		fileServer.ServeHTTP(writer, request)
	})
}

func (a *API) audit(request *http.Request, action, targetType, targetID string, details any) {
	encoded, _ := json.Marshal(details)
	a.store.Audit(request.Context(), userFromContext(request.Context()).ID, action, targetType, targetID, a.clientIP(request), string(encoded))
}
