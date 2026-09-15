package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/riba2534/herdrx/internal/voicegateway"
)

// This file registers the three voice endpoints frozen in
// docs/design/chat-media-contract.md §3. It is deliberately separate from the
// terminal workbench socket: that connection carries a pane observation stream
// with entirely different semantics and lifetime.
//
// The server credential lives only in the gateway (internal/voicegateway). The
// browser never learns the upstream address, the key or the upstream session
// id, and the relay exposes no pane.*, agent.* or terminal.* method at all.

// maxVoiceTicketLength bounds the query parameter before it reaches the
// gateway, so a hostile client cannot make the gateway hash an unbounded value.
const maxVoiceTicketLength = 512

// voiceSessionRequest is the only accepted body of POST /api/voice/sessions.
// decodeJSON uses DisallowUnknownFields, so model/url/api_key/target and any
// other field are refused rather than ignored.
type voiceSessionRequest struct {
	Output string `json:"output"`
}

type voiceSessionResponse struct {
	Ticket       string                  `json:"ticket"`
	ExpiresAt    time.Time               `json:"expiresAt"`
	Capabilities voicegateway.Capabilities `json:"capabilities"`
}

// RegisterVoiceRoutes installs the voice endpoints on a chi router.
//
// It is safe to mount inside or outside the authenticated /api group: the
// guard below reuses the context's existing login when one is present and
// authenticates otherwise. Typical wiring, matching the contract:
//
//	router.Route("/voice", func(router chi.Router) {
//		api.RegisterVoiceRoutes(router, api.voice)
//	})
func (a *API) RegisterVoiceRoutes(router chi.Router, gateway *voicegateway.Gateway) {
	router.Use(a.guardWrites)
	router.Use(a.voiceAuthenticate)
	router.Get("/capabilities", a.voiceCapabilities(gateway))
	router.With(a.requireCSRF, requireJSON).Post("/sessions", a.voiceSession(gateway))
	router.With(a.requireOrigin).Get("/ws", a.voiceSocket(gateway))
}

// RegisterVoiceRoutes is the package-level form of the same registration, for
// callers that prefer not to keep the *API receiver in scope.
func RegisterVoiceRoutes(router chi.Router, api *API, gateway *voicegateway.Gateway) {
	api.RegisterVoiceRoutes(router, gateway)
}

// NewVoiceGateway builds a gateway from the HERDRX_VOICE_* environment and
// wires the instance's own request limiter into it, so session creation shares
// one accounting table with the rest of the API instead of double counting.
//
// A nil gateway is returned when the feature is switched off; the routes then
// answer 404 for every /api/voice/* path.
func NewVoiceGateway(cfg voicegateway.Config, api *API, logger *slog.Logger) (*voicegateway.Gateway, error) {
	options := voicegateway.Options{Config: cfg, Logger: logger}
	if api != nil {
		options.Limiter = requestLimiterAdapter{limiter: &api.limiter}
	}
	return voicegateway.NewWithOptions(options)
}

// requestLimiterAdapter exposes the instance's requestLimiter (guards.go) under
// the gateway's RateLimiter interface, so session creation shares one
// accounting table with the rest of the API.
type requestLimiterAdapter struct{ limiter *requestLimiter }

func (a requestLimiterAdapter) Allow(key string, limit int) bool {
	return a.limiter.allow(key, limit)
}

// voiceAuthenticate applies the instance's cookie-session boundary unless the
// route is already mounted inside the authenticated group.
func (a *API) voiceAuthenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if sessionFromContext(request.Context()).ID != "" {
			next.ServeHTTP(writer, request)
			return
		}
		a.authenticate(next).ServeHTTP(writer, request)
	})
}

// voiceCapabilities reports whether the microphone entry point should exist.
// It never discloses the upstream address, the credential or the account.
func (a *API) voiceCapabilities(gateway *voicegateway.Gateway) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if gateway == nil || !gateway.Available() {
			writeError(writer, http.StatusNotFound, "voice_disabled", "本实例未启用语音输入")
			return
		}
		writeJSON(writer, http.StatusOK, gateway.Capabilities())
	}
}

func (a *API) voiceSession(gateway *voicegateway.Gateway) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if gateway == nil || !gateway.Available() {
			writeError(writer, http.StatusNotFound, "voice_disabled", "本实例未启用语音输入")
			return
		}
		var body voiceSessionRequest
		if err := decodeJSON(request, &body); err != nil {
			decodeError(writer, err)
			return
		}
		switch body.Output {
		case "", "text":
			body.Output = "text"
		case "audio":
			if gateway.Capabilities().AudioReply != voicegateway.AudioReplyAllowed {
				writeError(writer, http.StatusForbidden, "voice_audio_disabled", "本实例未开启语音回答，请改用文字回复模式")
				return
			}
		default:
			writeError(writer, http.StatusBadRequest, "invalid_output", "output 只能是 text 或 audio")
			return
		}

		user := userFromContext(request.Context())
		session := sessionFromContext(request.Context())
		ticket, err := gateway.IssueTicket(user.ID, session.ID)
		if err != nil {
			writeVoiceFailure(writer, err)
			return
		}
		a.audit(request, "voice.session_created", "user", user.ID, map[string]any{"output": body.Output})
		writeJSON(writer, http.StatusOK, voiceSessionResponse{
			Ticket:       ticket.Value,
			ExpiresAt:    ticket.ExpiresAt,
			Capabilities: gateway.Capabilities(),
		})
	}
}

// voiceSocket upgrades the browser and relays frames to the upstream session.
//
// Order matters and follows the contract: the one-time ticket is spent before
// the upgrade, so an invalid ticket never opens a socket; the upstream is
// dialed only after the upgrade, so a failed dial is reported as a normalized
// frame plus close 1011 rather than an HTTP status.
func (a *API) voiceSocket(gateway *voicegateway.Gateway) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if gateway == nil || !gateway.Available() {
			writeError(writer, http.StatusNotFound, "voice_disabled", "本实例未启用语音输入")
			return
		}
		ticket := request.URL.Query().Get("ticket")
		if ticket == "" || len(ticket) > maxVoiceTicketLength {
			writeError(writer, http.StatusForbidden, "voice_ticket_invalid", "语音会话票据无效或已过期，请重新点击麦克风")
			return
		}
		user := userFromContext(request.Context())
		session := sessionFromContext(request.Context())
		claim, err := gateway.Claim(user.ID, session.ID, ticket)
		if err != nil {
			writeVoiceFailure(writer, err)
			return
		}

		// requireOrigin already validated the exact scheme/host/port against the
		// configured origins; the library's Host-only default cannot express our
		// explicit aliases, so the check stays ours.
		connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{
			CompressionMode:    websocket.CompressionDisabled,
			InsecureSkipVerify: true,
		})
		if err != nil {
			claim.Release()
			return
		}
		defer closeAccessWebSocket(connection, request.Context())
		writer0 := &socketWriter{conn: connection}

		// The dial must not inherit a request context that has already been
		// canceled by a client that is still connected; use the lease context.
		lease := accessFromContext(request.Context())
		ctx, cancel := context.WithCancel(request.Context())
		defer cancel()
		if lease != nil && lease.ctx.Err() != nil {
			_ = writer0.JSON(ctx, map[string]any{"t": "error", "code": "voice_upstream_unavailable", "message": "登录已失效，请重新登录"})
			_ = connection.Close(websocket.StatusCode(4401), "login expired")
			claim.Release()
			return
		}

		session2, err := claim.Dial(ctx)
		if err != nil {
			// Normalized, redacted: the upstream's own error text can name the
			// account or the gateway and must not reach the browser.
			_ = writer0.JSON(ctx, map[string]any{
				"t": "error", "code": "voice_upstream_unavailable",
				"message": "语音上游暂时不可用，请稍后重试",
			})
			_ = connection.Close(websocket.StatusCode(1011), "upstream unavailable")
			return
		}
		session2.SetClassifier(voiceCloseClassifier)

		a.audit(request, "voice.session_opened", "user", user.ID, nil)
		_ = session2.Relay(ctx, &browserConn{conn: connection, writer: writer0})
	}
}

func writeVoiceFailure(writer http.ResponseWriter, err error) {
	if failure, ok := voicegateway.AsFailure(err); ok {
		if failure.RetryAfter > 0 {
			writer.Header().Set("Retry-After", strconv.Itoa(failure.RetryAfter))
		}
		if failure.Status() == http.StatusTooManyRequests {
			writer.Header().Set("Retry-After", strconv.Itoa(max(1, failure.RetryAfter)))
		}
		writeError(writer, failure.Status(), failure.Code, failure.Message)
		return
	}
	writeError(writer, http.StatusBadGateway, "voice_unavailable", "语音服务暂时不可用，请稍后重试")
}

// voiceCloseClassifier reuses the workbench's revocation vocabulary so a voice
// session and a terminal socket describe the same cause the same way.
func voiceCloseClassifier(cause error) (int, string, bool) {
	switch {
	case cause == nil:
		return 0, "", false
	case errors.Is(cause, errLoginEnded):
		return 4401, voicegateway.ReasonAuth, true
	case errors.Is(cause, errAccessUnavailable):
		return int(websocket.StatusTryAgainLater), voicegateway.ReasonAuth, true
	case errors.Is(cause, errWorkbenchStopped):
		return int(websocket.StatusServiceRestart), voicegateway.ReasonServer, true
	}
	return 0, "", false
}

// browserConn adapts the upgraded browser socket to voicegateway.Conn.
type browserConn struct {
	conn   *websocket.Conn
	writer *socketWriter
}

func (b *browserConn) Read(ctx context.Context) (voicegateway.FrameType, []byte, error) {
	typ, payload, err := b.conn.Read(ctx)
	if err != nil {
		return voicegateway.FrameText, nil, err
	}
	if typ == websocket.MessageBinary {
		return voicegateway.FrameBinary, payload, nil
	}
	return voicegateway.FrameText, payload, nil
}

func (b *browserConn) Write(ctx context.Context, typ voicegateway.FrameType, payload []byte) error {
	if typ == voicegateway.FrameBinary {
		return b.writer.Binary(ctx, payload)
	}
	return b.writer.write(ctx, websocket.MessageText, payload)
}

func (b *browserConn) Close(code int, reason string) error {
	closeWebSocket(b.conn, websocket.StatusCode(code), reason)
	return nil
}

func (b *browserConn) SetReadLimit(limit int64) {
	if limit > 0 {
		b.conn.SetReadLimit(limit)
	}
}

// VoiceConfigFromEnv reads the HERDRX_VOICE_* environment. It returns the
// resolved config together with its credential-free form, which is the only
// one safe to log.
func VoiceConfigFromEnv() (voicegateway.Config, voicegateway.Config, error) {
	cfg, err := voicegateway.LoadConfigFromEnv()
	if err != nil {
		return voicegateway.Config{}, voicegateway.Config{}, err
	}
	return cfg, cfg.Redacted(), nil
}
