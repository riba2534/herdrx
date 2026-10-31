package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func passwordSessions(t *testing.T, s *Store, user string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := s.CreateSession(context.Background(), Session{ID: id, UserID: user, CSRFToken: "test-csrf", ExpiresAt: time.Now().Add(time.Hour)}, []byte(id), "Test", "192.0.2.5"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChangePasswordKeepsOnlyCurrentLoginAndIsConditional(t *testing.T) {
	s := accessStore(t)
	ctx := context.Background()
	passwordSessions(t, s, "member", "current", "other-a", "other-b")
	passwordSessions(t, s, "admin", "admin-login")
	for _, sub := range []PushSubscription{
		{ID: "lost-device", UserID: "member", Endpoint: "https://push.example.test/lost", P256DH: "test", Auth: "test"},
		{ID: "admin-device", UserID: "admin", Endpoint: "https://push.example.test/admin", P256DH: "test", Auth: "test"},
	} {
		if err := s.UpsertPushSubscription(ctx, sub); err != nil {
			t.Fatal(err)
		}
	}
	revoked, err := s.ChangePassword(ctx, "member", "current", "test-only", "test-only:new", AuditEvent{UserID: "member", Action: "password.changed"})
	if err != nil || revoked != 2 {
		t.Fatalf("change: revoked=%d err=%v", revoked, err)
	}
	sessions, err := s.ListSessions(ctx, "member", Page{})
	if err != nil || len(sessions) != 1 || sessions[0].ID != "current" {
		t.Fatalf("remaining sessions %+v %v", sessions, err)
	}
	if admin, _ := s.ListSessions(ctx, "admin", Page{}); len(admin) != 1 {
		t.Fatal("another user's login was revoked")
	}
	var memberSubs, adminSubs int
	s.db.QueryRow(`SELECT COUNT(*) FROM push_subscriptions WHERE user_id='member'`).Scan(&memberSubs)
	s.db.QueryRow(`SELECT COUNT(*) FROM push_subscriptions WHERE user_id='admin'`).Scan(&adminSubs)
	if memberSubs != 0 || adminSubs != 1 {
		t.Fatalf("push subscriptions after change: member=%d admin=%d", memberSubs, adminSubs)
	}
	// A second change based on the stale verified hash must lose.
	if _, err := s.ChangePassword(ctx, "member", "current", "test-only", "test-only:other", AuditEvent{Action: "password.changed"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale change err=%v", err)
	}
	user, _ := s.UserByID(ctx, "member")
	if user.PasswordHash != "test-only:new" {
		t.Fatal("stale change overwrote the password")
	}
	if _, err := s.ChangePassword(ctx, "member", "other-a", "test-only:new", "test-only:third", AuditEvent{Action: "password.changed"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked current session err=%v", err)
	}
}

func TestResetPasswordFlagsUserAndEndsAllAccess(t *testing.T) {
	s := accessStore(t)
	ctx := context.Background()
	passwordSessions(t, s, "member", "one", "two")
	if err := s.UpsertPushSubscription(ctx, PushSubscription{ID: "sub", UserID: "member", Endpoint: "https://push.example.test/sub", P256DH: "test", Auth: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE users SET disabled=1 WHERE id='member'`); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx, "admin", "admin", "x", AuditEvent{}); !errors.Is(err, ErrSelfReset) {
		t.Fatalf("self reset err=%v", err)
	}
	if err := s.ResetPassword(ctx, "member", "admin", "x", AuditEvent{}); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("member reset err=%v", err)
	}
	if err := s.ResetPassword(ctx, "admin", "missing", "x", AuditEvent{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing reset err=%v", err)
	}
	if err := s.ResetPassword(ctx, "admin", "member", "test-only:temporary", AuditEvent{UserID: "admin", Action: "password.reset"}); err != nil {
		t.Fatal(err)
	}
	user, err := s.UserByID(ctx, "member")
	if err != nil || !user.MustChangePassword || !user.Disabled || user.PasswordHash != "test-only:temporary" {
		t.Fatalf("reset user %+v %v", user, err)
	}
	var sessions, subs int
	s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id='member'`).Scan(&sessions)
	s.db.QueryRow(`SELECT COUNT(*) FROM push_subscriptions WHERE user_id='member'`).Scan(&subs)
	if sessions != 0 || subs != 0 {
		t.Fatalf("sessions=%d subscriptions=%d after reset", sessions, subs)
	}
	listed, err := s.ListUsers(ctx, Page{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range listed {
		if item.ID == "member" && !item.MustChangePassword {
			t.Fatal("admin list hides the forced change")
		}
	}
}
