package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func accessStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, id := range []string{"admin", "member"} {
		role := "user"
		if id == "admin" {
			role = "admin"
		}
		if err = s.CreateUser(context.Background(), User{ID: id, Email: id + "@example.test", DisplayName: id, Role: role, PasswordHash: "test-only"}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestDisableAtomicityAndFreshLoginRequired(t *testing.T) {
	s := accessStore(t)
	ctx := context.Background()
	session := Session{ID: "login", UserID: "member", CSRFToken: "test-csrf", ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.CreateSession(ctx, session, []byte("test-hash"), "Test", "192.0.2.5"); err != nil {
		t.Fatal(err)
	}
	sub := PushSubscription{ID: "sub", UserID: "member", Endpoint: "https://push.example.test/sub", P256DH: "test", Auth: "test"}
	if err := s.UpsertPushSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	// A required audit write fails: all preceding changes must roll back.
	if _, err := s.db.Exec(`CREATE TRIGGER reject_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT,'test audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	event := AuditEvent{UserID: "admin", Action: "user.disabled", TargetID: "member"}
	if err := s.SetUserDisabled(ctx, "admin", "member", true, event); err == nil {
		t.Fatal("mutation ignored audit failure")
	}
	if _, err := s.SessionAccess(ctx, "login", "member"); err != nil {
		t.Fatal("failed mutation deleted a session")
	}
	if subs, _ := s.PushSubscriptions(ctx); len(subs) != 1 {
		t.Fatal("failed mutation deleted push subscription")
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_audit"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDisabled(ctx, "admin", "member", true, event); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionAccess(ctx, "login", "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled login: %v", err)
	}
	if err := s.CreateSession(ctx, session, []byte("test-hash"), "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled user got a new session: %v", err)
	}
	if err := s.UpsertPushSubscription(ctx, sub); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled user subscribed: %v", err)
	}
	if err := s.SetUserDisabled(ctx, "admin", "member", false, AuditEvent{UserID: "admin", Action: "user.enabled"}); err != nil {
		t.Fatal(err)
	}
	if sessions, _ := s.ListSessions(ctx, "member", Page{}); len(sessions) != 0 {
		t.Fatal("reenabling revived login")
	}
	if subs, _ := s.PushSubscriptions(ctx); len(subs) != 0 {
		t.Fatal("reenabling revived notifications")
	}
}

func TestAdminProtectionAndConcurrentDisable(t *testing.T) {
	s := accessStore(t)
	ctx := context.Background()
	event := AuditEvent{UserID: "admin", Action: "user.disabled"}
	if err := s.SetUserDisabled(ctx, "admin", "admin", true, event); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last admin: %v", err)
	}
	if err := s.SetUserDisabled(ctx, "member", "admin", true, event); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("member elevated privilege: %v", err)
	}
	if err := s.CreateUser(ctx, User{ID: "admin2", Email: "admin2@example.test", DisplayName: "Admin 2", Role: "admin", PasswordHash: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDisabled(ctx, "admin", "admin", true, event); !errors.Is(err, ErrSelfDisable) {
		t.Fatalf("self disable: %v", err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, pair := range [][2]string{{"admin", "admin2"}, {"admin2", "admin"}} {
		go func(actor, target string) {
			<-start
			results <- s.SetUserDisabled(ctx, actor, target, true, AuditEvent{UserID: actor, Action: "user.disabled"})
		}(pair[0], pair[1])
	}
	close(start)
	success := 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if !errors.Is(err, ErrAdminRequired) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent disabling had %d successes", success)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0").Scan(&count); err != nil || count != 1 {
		t.Fatalf("active administrators=%d %v", count, err)
	}
}

func TestInviteTransactionsAndRaces(t *testing.T) {
	s := accessStore(t)
	if _, err := s.SetRegistration(context.Background(), "admin", "invite", 0, AuditEvent{UserID: "admin", Action: "registration.changed"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	invite := func(id string) {
		t.Helper()
		if err := s.CreateInvite(ctx, Invite{ID: id, CreatedBy: "admin", ExpiresAt: time.Now().Add(time.Hour)}, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	invite("duplicate")
	user := User{ID: "new", Email: "MEMBER@example.test", DisplayName: "New", Role: "admin", PasswordHash: "test"}
	if err := s.ConsumeInviteAndCreateUser(ctx, []byte("duplicate"), user); !errors.Is(err, ErrEmailExists) {
		t.Fatalf("duplicate email: %v", err)
	}
	if err := s.CheckInvite(ctx, []byte("duplicate")); err != nil {
		t.Fatal("duplicate email consumed invitation")
	}
	user.Email = "new@example.test"
	if err := s.ConsumeInviteAndCreateUser(ctx, []byte("duplicate"), user); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.UserByID(ctx, "new")
	if saved.Role != "user" {
		t.Fatal("invitation elevated role")
	}
	if err := s.RevokeInvite(ctx, "admin", "duplicate", AuditEvent{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("used invitation revoked: %v", err)
	}
	for i := range 10 {
		id := fmt.Sprintf("race-%d", i)
		invite(id)
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			results <- s.ConsumeInviteAndCreateUser(ctx, []byte(id), User{ID: id, Email: id + "@example.test", DisplayName: id, Role: "user", PasswordHash: "test"})
		}()
		go func() {
			<-start
			results <- s.RevokeInvite(ctx, "admin", id, AuditEvent{UserID: "admin", Action: "invite.revoked"})
		}()
		close(start)
		success := 0
		for range 2 {
			err := <-results
			if err == nil {
				success++
			} else if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
		}
		if success != 1 {
			t.Fatalf("consume/revoke both succeeded: %d", success)
		}
		if err := s.CheckInvite(ctx, []byte(id)); !errors.Is(err, ErrNotFound) {
			t.Fatal("raced invitation remains active")
		}
	}
	invite("single-use")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			results <- s.ConsumeInviteAndCreateUser(ctx, []byte("single-use"), User{ID: fmt.Sprintf("once-%d", i), Email: fmt.Sprintf("once-%d@example.test", i), DisplayName: "Once", PasswordHash: "test"})
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("invitation reused")
	}
}

func TestSessionOwnerScopeAndAuditFiltering(t *testing.T) {
	s := accessStore(t)
	ctx := context.Background()
	if err := s.CreateSession(ctx, Session{ID: "admin-login", UserID: "admin", ExpiresAt: time.Now().Add(time.Hour)}, []byte("hash"), "", ""); err != nil {
		t.Fatal(err)
	}
	event := AuditEvent{UserID: "admin", Action: "session.revoked", TargetID: "member"}
	if err := s.RevokeUserSessions(ctx, "admin", "member", "admin-login", event); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user login revoke: %v", err)
	}
	if _, err := s.SessionAccess(ctx, "admin-login", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeUserSessions(ctx, "admin", "admin", "admin-login", event); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeUserSessions(ctx, "admin", "admin", "admin-login", event); err != nil {
		t.Fatal("repeated revocation is not idempotent")
	}
	events, err := s.ListAudit(ctx, AuditFilter{UserID: "admin", Action: "session.revoked", Since: time.Now().Add(-time.Minute), Until: time.Now().Add(time.Minute), Page: Page{Limit: 1}})
	if err != nil || len(events) != 2 {
		t.Fatalf("pagination lookahead/filter: %v %v", events, err)
	}
	empty, err := s.ListAudit(ctx, AuditFilter{UserID: "' OR 1=1 --"})
	if err != nil || len(empty) != 0 {
		t.Fatal("audit filter was not literal")
	}
}
