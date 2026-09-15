package voicegateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIssueTicketIsSingleUseAndBound(t *testing.T) {
	gateway := New(testConfig(t))
	defer gateway.Close()

	ticket, err := gateway.IssueTicket("user-1", "login-1")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if len(ticket.Value) < 32 {
		t.Fatalf("a ticket must be a long opaque token, got %q", ticket.Value)
	}
	if !ticket.ExpiresAt.After(time.Now()) {
		t.Fatal("a fresh ticket must not already be expired")
	}

	if err := gateway.consume("user-1", "login-1", ticket.Value); err != nil {
		t.Fatalf("first use must succeed: %v", err)
	}
	if err := gateway.consume("user-1", "login-1", ticket.Value); !errors.Is(err, ErrTicket) {
		t.Fatalf("a ticket must not be replayable, got %v", err)
	}
}

func TestTicketIsBoundToUserAndLoginSession(t *testing.T) {
	gateway := New(testConfig(t))
	defer gateway.Close()

	for _, tc := range []struct{ user, session string }{
		{"user-2", "login-1"},
		{"user-1", "login-2"},
		{"user-1", ""},
	} {
		ticket, err := gateway.IssueTicket("user-1", "login-1")
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		if err := gateway.consume(tc.user, tc.session, ticket.Value); !errors.Is(err, ErrTicket) {
			t.Fatalf("ticket issued for user-1/login-1 must not be spendable by %s/%s", tc.user, tc.session)
		}
	}

	if err := gateway.consume("user-1", "login-1", "forged-ticket-value"); !errors.Is(err, ErrTicket) {
		t.Fatalf("an unknown ticket must be refused, got %v", err)
	}
}

func TestTicketExpires(t *testing.T) {
	clock := time.Now()
	gateway, err := NewWithOptions(Options{
		Config: testConfig(t),
		Now:    func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	defer gateway.Close()

	ticket, err := gateway.IssueTicket("user-1", "login-1")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	clock = clock.Add(TicketTTL*time.Second + time.Second)
	if err := gateway.consume("user-1", "login-1", ticket.Value); !errors.Is(err, ErrTicket) {
		t.Fatalf("an expired ticket must be refused, got %v", err)
	}
}

func TestIssueTicketCapsOutstandingTickets(t *testing.T) {
	gateway := New(testConfig(t))
	defer gateway.Close()

	for i := 0; i < MaxOutstandingTickets; i++ {
		if _, err := gateway.IssueTicket("user-1", "login-1"); err != nil {
			t.Fatalf("ticket %d: %v", i, err)
		}
	}
	if _, err := gateway.IssueTicket("user-1", "login-1"); !errors.Is(err, ErrBusy) {
		t.Fatalf("outstanding tickets must be capped, got %v", err)
	}
}

func TestIssueTicketIsRateLimited(t *testing.T) {
	cfg := testConfig(t)
	cfg.CreatePerMinute = 2
	gateway := New(cfg)
	defer gateway.Close()

	// Spend the budget, consuming each ticket so the outstanding cap is not
	// what rejects the third call.
	for i := 0; i < 2; i++ {
		ticket, err := gateway.IssueTicket("user-1", "login-1")
		if err != nil {
			t.Fatalf("ticket %d: %v", i, err)
		}
		if err := gateway.consume("user-1", "login-1", ticket.Value); err != nil {
			t.Fatalf("consume %d: %v", i, err)
		}
	}
	if _, err := gateway.IssueTicket("user-1", "login-1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("create rate must be bounded, got %v", err)
	}
	// The limiter is per user, not global.
	if _, err := gateway.IssueTicket("user-2", "login-2"); err != nil {
		t.Fatalf("another user must be unaffected: %v", err)
	}
}

type stubLimiter struct {
	mu      sync.Mutex
	blocked map[string]bool
}

func (l *stubLimiter) Allow(key string, _ int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.blocked[key]
}

// TestInjectedLimiterIsUsed proves the route can reuse its own requestLimiter
// instead of the gateway double-counting.
func TestInjectedLimiterIsUsed(t *testing.T) {
	limiter := &stubLimiter{blocked: map[string]bool{"voice-create:user-1": true}}
	gateway, err := NewWithOptions(Options{Config: testConfig(t), Limiter: limiter, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	defer gateway.Close()
	if _, err := gateway.IssueTicket("user-1", "login-1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("the injected limiter must decide, got %v", err)
	}
}

func TestOpenEnforcesConcurrencyCaps(t *testing.T) {
	upstream := newScriptedUpstream()
	cfg := testConfig(t)
	cfg.MaxSessionsPerUser = 1
	cfg.MaxSessions = 2
	dialer := &fakeDialer{conn: upstream.conn}
	gateway, err := NewWithOptions(Options{Config: cfg, Dialer: dialer, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	defer gateway.Close()

	ticket, _ := gateway.IssueTicket("user-1", "login-1")
	session, err := gateway.Open(context.Background(), "user-1", "login-1", ticket.Value)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	if gateway.ActiveSessions() != 1 {
		t.Fatalf("expected one active session, got %d", gateway.ActiveSessions())
	}

	// The same user is capped even though the global cap still has room.
	second, _ := gateway.IssueTicket("user-1", "login-1")
	if _, err := gateway.Open(context.Background(), "user-1", "login-1", second.Value); !errors.Is(err, ErrBusy) {
		t.Fatalf("per-user cap must apply, got %v", err)
	}
	// Another user still fits under the global cap.
	third, _ := gateway.IssueTicket("user-2", "login-2")
	other, err := gateway.Open(context.Background(), "user-2", "login-2", third.Value)
	if err != nil {
		t.Fatalf("second user: %v", err)
	}

	// Now the global cap binds.
	fourth, _ := gateway.IssueTicket("user-3", "login-3")
	if _, err := gateway.Open(context.Background(), "user-3", "login-3", fourth.Value); !errors.Is(err, ErrBusy) {
		t.Fatalf("global cap must apply, got %v", err)
	}

	other.shutdown()
	session.shutdown()
	if active := gateway.ActiveSessions(); active != 0 {
		t.Fatalf("shutdown must release the reservation, %d active", active)
	}
}

func TestOpenConsumesTheTicketEvenWhenDialFails(t *testing.T) {
	dialer := &fakeDialer{err: errors.New("dial tcp 10.0.0.1:443: connect: connection refused")}
	gateway, err := NewWithOptions(Options{Config: testConfig(t), Dialer: dialer, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	defer gateway.Close()

	ticket, _ := gateway.IssueTicket("user-1", "login-1")
	if _, err := gateway.Open(context.Background(), "user-1", "login-1", ticket.Value); !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected a normalized upstream failure, got %v", err)
	}
	if gateway.ActiveSessions() != 0 {
		t.Fatal("a failed dial must not leak a reservation")
	}
	if _, err := gateway.Open(context.Background(), "user-1", "login-1", ticket.Value); !errors.Is(err, ErrTicket) {
		t.Fatalf("a failed attempt must not be replayable, got %v", err)
	}
}

func TestOpenFailureDoesNotLeakTheUpstreamTarget(t *testing.T) {
	dialer := &fakeDialer{err: errors.New("dial tcp voice.internal.example:443: connect: connection refused")}
	gateway, err := NewWithOptions(Options{Config: testConfig(t), Dialer: dialer, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	defer gateway.Close()
	ticket, _ := gateway.IssueTicket("user-1", "login-1")
	_, openErr := gateway.Open(context.Background(), "user-1", "login-1", ticket.Value)
	if openErr == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(openErr.Error(), "voice.internal.example") || strings.Contains(openErr.Error(), "test-key") {
		t.Fatalf("the failure must not name the private upstream: %v", openErr)
	}
	if sanitizeDialError(dialer.err) != "refused" {
		t.Fatalf("unexpected classification %q", sanitizeDialError(dialer.err))
	}
}

func TestDisabledGatewayRefusesEverything(t *testing.T) {
	cfg := Config{AudioReply: AudioReplyOff}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a zero config must validate: %v", err)
	}
	gateway := New(cfg)
	defer gateway.Close()
	if gateway.Available() {
		t.Fatal("a disabled gateway must not be available")
	}
	if _, err := gateway.IssueTicket("user-1", "login-1"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("issue must refuse, got %v", err)
	}
	if _, err := gateway.Open(context.Background(), "user-1", "login-1", "anything"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("open must refuse, got %v", err)
	}
}

func TestCloseTearsDownLiveSessions(t *testing.T) {
	upstream := newScriptedUpstream()
	gateway, session, browser := openSession(t, testConfig(t), upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go upstream.serve(ctx)
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()
	browser.push(FrameText, []byte(`{"t":"start","output":"text"}`))
	_ = browser.nextJSON(t)

	gateway.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close must end a live relay")
	}
	if code, _ := browser.awaitClose(t); code != 1012 {
		t.Fatalf("a restarted gateway should use 1012, got %d", code)
	}
	if _, err := gateway.IssueTicket("user-1", "login-1"); err == nil {
		t.Fatal("a closed gateway must refuse new tickets")
	}
}

func TestInvalidateTicketsRotatesTheEpoch(t *testing.T) {
	gateway := New(testConfig(t))
	defer gateway.Close()
	ticket, err := gateway.IssueTicket("user-1", "login-1")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	gateway.InvalidateTickets()
	if err := gateway.consume("user-1", "login-1", ticket.Value); !errors.Is(err, ErrTicket) {
		t.Fatalf("rotation must invalidate outstanding tickets, got %v", err)
	}
}
