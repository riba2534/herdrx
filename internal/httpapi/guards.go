package httpapi

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/riba2534/herdrx/internal/config"
)

func (a *API) trustedProxy(addr netip.Addr) bool {
	for _, prefix := range a.config.TrustedProxies {
		if prefix.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}

func (a *API) clientIP(request *http.Request) string {
	direct := remoteIP(request)
	peer, err := netip.ParseAddr(direct)
	if err != nil || !a.trustedProxy(peer) {
		return direct
	}
	forwarded := strings.Join(request.Header.Values("X-Forwarded-For"), ",")
	if len(forwarded) > 4096 || forwarded == "" {
		return direct
	}
	parts := strings.Split(forwarded, ",")
	if len(parts) > 16 {
		return direct
	}
	chain := make([]netip.Addr, len(parts))
	for i, part := range parts {
		addr, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			return direct
		}
		chain[i] = addr.Unmap()
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if !a.trustedProxy(peer) {
			break
		}
		peer = chain[i]
	}
	return peer.Unmap().String()
}

func (a *API) allowedOrigin(request *http.Request) bool {
	if request.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if len(request.Header.Values("Origin")) > 1 {
		return false
	}
	raw := request.Header.Get("Origin")
	if raw == "" {
		u, err := url.Parse(request.Referer())
		if err != nil || u.User != nil || u.Host == "" {
			return false
		}
		raw = u.Scheme + "://" + u.Host
	}
	origin, err := config.NormalizeOrigin(raw)
	if err != nil {
		return false
	}
	if origin == a.config.PublicURL {
		return true
	}
	for _, allowed := range a.config.AllowedOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

func (a *API) requireOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.allowedOrigin(r) {
			writeError(w, http.StatusForbidden, "origin_forbidden", "访问地址未被实例允许，请使用配置的站点地址")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) guardWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && !a.allowedOrigin(r) {
			writeError(w, http.StatusForbidden, "origin_forbidden", "访问地址未被实例允许，请使用配置的站点地址")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "json_required", "此接口仅接受 JSON 请求")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
		next.ServeHTTP(w, r)
	})
}

func decodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容过大")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request", "请求格式不正确，请检查输入")
}

type rateWindow struct {
	count int
	until time.Time
}
type requestLimiter struct {
	mu      sync.Mutex
	windows map[string]rateWindow
	sweep   time.Time
}

func (l *requestLimiter) allow(key string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.windows == nil {
		l.windows = make(map[string]rateWindow)
	}
	if !now.Before(l.sweep) {
		for key, window := range l.windows {
			if !now.Before(window.until) {
				delete(l.windows, key)
			}
		}
		l.sweep = now.Add(time.Minute)
	}
	window, exists := l.windows[key]
	if !exists && len(l.windows) >= 10000 {
		return false
	}
	if !now.Before(window.until) {
		window = rateWindow{until: now.Add(time.Minute)}
	}
	if window.count >= limit {
		return false
	}
	window.count++
	l.windows[key] = window
	return true
}

func (l *requestLimiter) clear(key string) { l.mu.Lock(); delete(l.windows, key); l.mu.Unlock() }

func rateLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, "rate_limited", "尝试次数过多，请稍后重试")
}

// Hash slots bound the expensive operation itself, not only its HTTP caller.
// Argon2 cannot be interrupted; a canceled caller holds its slot until it ends.
func (a *API) hashSlot(ctx context.Context, registration bool) (func(), bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	if registration {
		select {
		case a.registrationHashes <- struct{}{}:
		default:
			return nil, false
		}
	}
	select {
	case a.passwordHashes <- struct{}{}:
		return func() {
			<-a.passwordHashes
			if registration {
				<-a.registrationHashes
			}
		}, true
	default:
		if registration {
			<-a.registrationHashes
		}
		return nil, false
	}
}

func hashBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	writeError(w, http.StatusServiceUnavailable, "auth_busy", "登录服务繁忙，请稍后重试")
}
