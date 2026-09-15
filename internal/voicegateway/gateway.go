package voicegateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Ticket is a one-time opaque grant to open exactly one voice WebSocket. It is
// bound to {user, login session, issuance generation}: a ticket stolen from one
// user or one login cannot be spent by another.
type Ticket struct {
	Value     string    `json:"ticket"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type pendingTicket struct {
	userID    string
	sessionID string
	epoch     uint64
	expiresAt time.Time
}

// RateLimiter bounds how often one user may create a session. It matches the
// shape of internal/httpapi's requestLimiter so the route can inject its own
// instance instead of the gateway doubling the accounting.
type RateLimiter interface {
	Allow(key string, limit int) bool
}

// Options configures a Gateway. Every field is optional; zero values fall back
// to the production defaults.
type Options struct {
	Config  Config
	Dialer  Dialer
	Limiter RateLimiter
	Logger  *slog.Logger
	// Now and Entropy are test seams. Production leaves them nil.
	Now     func() time.Time
	Entropy io.Reader
}

// Gateway owns the upstream voice sessions for one herdrx instance: ticket
// issuance, concurrency caps, session creation rate and lifecycle.
//
// It is safe for concurrent use. It never logs, stores or exposes the upstream
// credential.
type Gateway struct {
	cfg     Config
	dialer  Dialer
	limiter RateLimiter
	logger  *slog.Logger
	now     func() time.Time
	entropy io.Reader

	mu       sync.Mutex
	tickets  map[string]*pendingTicket
	perUser  map[string]int
	active   int
	epoch    uint64
	ticketsN int
	closed   bool
	closers  map[*Session]struct{}
}

type fallbackLimiter struct {
	mu      sync.Mutex
	windows map[string]*window
}

type window struct {
	count int
	until time.Time
}

func (l *fallbackLimiter) Allow(key string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.windows == nil {
		l.windows = make(map[string]*window)
	}
	current, ok := l.windows[key]
	if !ok || !now.Before(current.until) {
		current = &window{until: now.Add(time.Minute)}
		l.windows[key] = current
	}
	if current.count >= limit {
		return false
	}
	current.count++
	return true
}

// New builds a Gateway from a resolved Config using the production dialer.
func New(cfg Config) *Gateway {
	gateway, err := NewWithOptions(Options{Config: cfg})
	if err != nil {
		// NewWithOptions only fails on a programming error (nil Config handling
		// is total); fall back to a disabled gateway rather than panicking.
		return &Gateway{cfg: Config{AudioReply: AudioReplyOff}, dialer: WSDialer{}, logger: slog.Default(), now: time.Now, entropy: rand.Reader, tickets: map[string]*pendingTicket{}, perUser: map[string]int{}, closers: map[*Session]struct{}{}}
	}
	return gateway
}

// NewWithOptions builds a Gateway with injectable seams.
func NewWithOptions(opts Options) (*Gateway, error) {
	cfg := opts.Config
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	dialer := opts.Dialer
	if dialer == nil {
		dialer = WSDialer{}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	entropy := opts.Entropy
	if entropy == nil {
		entropy = rand.Reader
	}
	limiter := opts.Limiter
	if limiter == nil {
		limiter = &fallbackLimiter{}
	}
	return &Gateway{
		cfg: cfg, dialer: dialer, limiter: limiter, logger: logger,
		now: now, entropy: entropy,
		tickets: map[string]*pendingTicket{}, perUser: map[string]int{},
		epoch: 1, closers: map[*Session]struct{}{},
	}, nil
}

// Config returns the resolved configuration with the credential stripped.
func (g *Gateway) Config() Config { return g.cfg.Redacted() }

// Available reports whether the capability endpoint should advertise voice.
func (g *Gateway) Available() bool { return g.cfg.Available() }

// Capabilities is the browser-safe capability payload.
func (g *Gateway) Capabilities() Capabilities { return g.cfg.Capabilities() }

// IssueTicket mints a one-time ticket. It enforces the per-minute creation
// rate and the cap on outstanding unconsumed tickets, and never queues.
func (g *Gateway) IssueTicket(userID, sessionID string) (Ticket, error) {
	if !g.cfg.Available() {
		return Ticket{}, ErrNotConfigured
	}
	if userID == "" || sessionID == "" {
		return Ticket{}, ErrTicket
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return Ticket{}, ErrNotConfigured
	}
	g.sweepLocked()
	outstanding := 0
	for _, record := range g.tickets {
		if record.userID == userID {
			outstanding++
		}
	}
	epoch := g.epoch
	closed := g.closed
	g.mu.Unlock()

	if closed {
		return Ticket{}, ErrNotConfigured
	}
	if outstanding >= MaxOutstandingTickets {
		return Ticket{}, ErrBusy
	}
	// Rate limiting is deliberately outside the lock: a limiter may be
	// injected by the caller and must not run under our mutex.
	if !g.limiter.Allow("voice-create:"+userID, g.cfg.CreatePerMinute) {
		return Ticket{}, ErrRateLimited
	}

	raw := make([]byte, 32)
	if _, err := io.ReadFull(g.entropy, raw); err != nil {
		return Ticket{}, failure(500, "voice_internal", "语音服务暂时不可用，请稍后重试")
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	expires := g.now().Add(TicketTTL * time.Second)

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return Ticket{}, ErrNotConfigured
	}
	if g.tickets == nil {
		g.tickets = map[string]*pendingTicket{}
	}
	g.ticketsN++
	g.tickets[value] = &pendingTicket{userID: userID, sessionID: sessionID, epoch: epoch, expiresAt: expires}
	return Ticket{Value: value, ExpiresAt: expires}, nil
}

// InvalidateTickets rotates the issuance epoch so every outstanding ticket is
// refused. Routes may call it when an operator revokes access wholesale.
func (g *Gateway) InvalidateTickets() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.epoch++
	g.tickets = map[string]*pendingTicket{}
}

// Claim consumes a ticket and reserves a concurrency slot. The ticket is spent
// even if the caller never dials: a failed attempt must not be replayable.
//
// Claim is separate from Dial so the HTTP layer can refuse a bad ticket before
// it upgrades the browser socket, while still dialing upstream only after the
// upgrade succeeds (the contract requires that order).
func (g *Gateway) Claim(userID, sessionID, value string) (*Claim, error) {
	if !g.cfg.Available() {
		return nil, ErrNotConfigured
	}
	if err := g.consume(userID, sessionID, value); err != nil {
		return nil, err
	}
	if err := g.reserve(userID); err != nil {
		return nil, err
	}
	return &Claim{gateway: g, userID: userID, sessionID: sessionID}, nil
}

// Claim is a spent ticket plus a held concurrency slot. It must end in either
// Dial or Release.
type Claim struct {
	gateway   *Gateway
	userID    string
	sessionID string
	mu        sync.Mutex
	settled   bool
}

// Dial opens the upstream session for a claim.
func (c *Claim) Dial(ctx context.Context) (*Session, error) {
	c.mu.Lock()
	if c.settled {
		c.mu.Unlock()
		return nil, ErrTicket
	}
	c.settled = true
	c.mu.Unlock()
	session, err := c.gateway.dial(ctx, c.userID, c.sessionID)
	if err != nil {
		c.gateway.release(c.userID)
		return nil, err
	}
	return session, nil
}

// Release abandons a claim that never became a session.
func (c *Claim) Release() {
	c.mu.Lock()
	if c.settled {
		c.mu.Unlock()
		return
	}
	c.settled = true
	c.mu.Unlock()
	c.gateway.release(c.userID)
}

// Open consumes a ticket and dials in one step. It is the convenience form for
// callers that do not need to separate the two phases.
func (g *Gateway) Open(ctx context.Context, userID, sessionID, value string) (*Session, error) {
	claim, err := g.Claim(userID, sessionID, value)
	if err != nil {
		return nil, err
	}
	return claim.Dial(ctx)
}

func (g *Gateway) dial(ctx context.Context, userID, sessionID string) (*Session, error) {
	header := http.Header{}
	// Headers only. The upstream rejects ?api_key= outright, and this header
	// never leaves this process.
	header.Set("Authorization", "Bearer "+g.cfg.APIKey)

	upstream, err := g.dialer.Dial(ctx, g.cfg.UpstreamURL(), header)
	if err != nil {
		// The caller owns the reservation release; see Claim.Dial.
		g.logger.Warn("voice upstream dial failed", "category", sanitizeDialError(err))
		return nil, ErrUpstream
	}
	upstream.SetReadLimit(upstreamReadLimit(g.cfg))

	session := &Session{
		gateway: g, userID: userID, loggedInSession: sessionID,
		output: "text", upstream: upstream,
		state: relayState{cfg: g.cfg},
		lastActivity: g.now(),
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		_ = upstream.Close(1001, "gateway closed")
		return nil, ErrNotConfigured
	}
	g.closers[session] = struct{}{}
	g.mu.Unlock()
	return session, nil
}

// upstreamReadLimit is the largest single frame either leg may deliver, plus a
// small allowance so the transport rejects oversize frames before the relay
// has to buffer them.
func upstreamReadLimit(cfg Config) int64 {
	limit := cfg.MaxControlBytes
	if cfg.MaxAudioFrameBytes > limit {
		limit = cfg.MaxAudioFrameBytes
	}
	return int64(limit) + 1024
}

func (g *Gateway) consume(userID, sessionID, value string) error {
	if value == "" || len(value) > 512 {
		return ErrTicket
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked()
	record, ok := g.tickets[value]
	if !ok {
		return ErrTicket
	}
	// Single use: remove before any other check so a rejected attempt still
	// burns the ticket.
	delete(g.tickets, value)
	if !g.now().Before(record.expiresAt) {
		return ErrTicket
	}
	if record.epoch != g.epoch || record.userID != userID || record.sessionID != sessionID {
		return ErrTicket
	}
	return nil
}

func (g *Gateway) reserve(userID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return ErrNotConfigured
	}
	if g.perUser == nil {
		g.perUser = map[string]int{}
	}
	if g.perUser[userID] >= g.cfg.MaxSessionsPerUser || g.active >= g.cfg.MaxSessions {
		return ErrBusy
	}
	g.perUser[userID]++
	g.active++
	return nil
}

func (g *Gateway) release(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.perUser[userID] > 0 {
		g.perUser[userID]--
	}
	if g.perUser[userID] == 0 {
		delete(g.perUser, userID)
	}
	if g.active > 0 {
		g.active--
	}
}

// forget removes a finished session from the instance's closer set.
func (g *Gateway) forget(session *Session) {
	g.mu.Lock()
	delete(g.closers, session)
	g.mu.Unlock()
}

func (g *Gateway) sweepLocked() {
	now := g.now()
	for key, record := range g.tickets {
		if !now.Before(record.expiresAt) {
			delete(g.tickets, key)
		}
	}
}

// ActiveSessions reports the number of live voice sessions, for diagnostics.
func (g *Gateway) ActiveSessions() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

// Close tears down every live voice session and refuses new work. It affects
// only Web access: the relay holds no pane, Herdr session or remote process.
func (g *Gateway) Close() {
	g.mu.Lock()
	g.closed = true
	g.tickets = map[string]*pendingTicket{}
	sessions := make([]*Session, 0, len(g.closers))
	for session := range g.closers {
		sessions = append(sessions, session)
	}
	g.closers = map[*Session]struct{}{}
	g.mu.Unlock()
	for _, session := range sessions {
		session.shutdown()
	}
}

// sanitizeDialError classifies a dial failure without letting the private
// gateway host, its IP or the credential reach a log line. Network errors embed
// the dial target, so only a coarse category is reported.
func sanitizeDialError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "no such host"):
		return "dns"
	case strings.Contains(text, "certificate"):
		return "tls"
	case strings.Contains(text, "connection refused"):
		return "refused"
	case strings.Contains(text, "connection reset"):
		return "reset"
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline"):
		return "timeout"
	case strings.Contains(text, "101"), strings.Contains(text, "unexpected"), strings.Contains(text, "websocket"):
		return "handshake"
	}
	return "dial"
}
