package store

import (
	"context"
	"testing"
)

func TestUserLaunchersStayWithTheAccount(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	for _, id := range []string{"user-a", "user-b"} {
		if err := s.CreateUser(ctx, User{ID: id, Email: id + "@example.test", DisplayName: id, Role: "user", PasswordHash: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.UserLaunchers(ctx, "user-a"); err != nil || got != "" {
		t.Fatalf("empty = %q %v", got, err)
	}
	const document = `{"launchers":[{"id":"review","name":"审查","command":"codex review"}]}`
	if err := s.SetUserLaunchers(ctx, "user-a", document); err != nil {
		t.Fatal(err)
	}
	if got, err := s.UserLaunchers(ctx, "user-a"); err != nil || got != document {
		t.Fatalf("saved = %q %v", got, err)
	}
	if got, err := s.UserLaunchers(ctx, "user-b"); err != nil || got != "" {
		t.Fatalf("other account = %q %v", got, err)
	}
	if err := s.SetUserLaunchers(ctx, "user-a", `{"launchers":[]}`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.UserLaunchers(ctx, "user-a"); err != nil || got != `{"launchers":[]}` {
		t.Fatalf("cleared = %q %v", got, err)
	}
}

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
