package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRegistrationSettingsAtomicityAndPersistence(t *testing.T) {
	s := accessStore(t)
	ctx := context.Background()
	settings, err := s.Settings(ctx)
	if err != nil || settings.Registration != "closed" || settings.Revision != 0 {
		t.Fatalf("default: %+v %v", settings, err)
	}
	event := AuditEvent{UserID: "admin", Action: "registration.changed"}
	if _, err := s.SetRegistration(ctx, "member", "invite", 0, event); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("member opened registration: %v", err)
	}
	if err := s.CreateInvite(ctx, Invite{ID: "closed", CreatedBy: "admin", ExpiresAt: time.Now().Add(time.Hour)}, []byte("closed")); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("invite while closed: %v", err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_settings_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT,'test unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRegistration(ctx, "admin", "invite", 0, event); err == nil {
		t.Fatal("ignored failed audit")
	}
	settings, _ = s.Settings(ctx)
	if settings.Registration != "closed" || settings.Revision != 0 {
		t.Fatal("audit failure did not roll back setting")
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_settings_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRegistration(ctx, "admin", "invite", 0, event); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRegistration(ctx, "admin", "closed", 0, event); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale admin overwrote setting: %v", err)
	}
	if err := s.CreateInvite(ctx, Invite{ID: "pending", CreatedBy: "admin", ExpiresAt: time.Now().Add(time.Hour)}, []byte("pending")); err != nil {
		t.Fatal(err)
	}
	user := User{ID: "new", Email: "new@example.test", DisplayName: "New", PasswordHash: "test", Role: "admin"}
	if _, err := s.SetRegistration(ctx, "admin", "closed", 1, event); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeInviteAndCreateUser(ctx, []byte("pending"), user, 1); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("registration after closure: %v", err)
	}
	if _, err := s.SetRegistration(ctx, "admin", "invite", 2, event); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeInviteAndCreateUser(ctx, []byte("pending"), user, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale in-flight registration after reopen: %v", err)
	}
	if err := s.CheckInvite(ctx, []byte("pending")); err != nil {
		t.Fatal("rejected registration consumed invite")
	}
	if err := s.ConsumeInviteAndCreateUser(ctx, []byte("pending"), user, 3); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.UserByID(ctx, "new")
	if saved.Role != "user" {
		t.Fatal("role elevated")
	}
	// Re-running migrations models normal reopen and must preserve the admin choice.
	if err := migrate(s.db); err != nil {
		t.Fatal(err)
	}
	settings, _ = s.Settings(ctx)
	if settings.Registration != "invite" || settings.Revision != 3 {
		t.Fatalf("migration reset policy: %+v", settings)
	}
}

func TestConcurrentColdBootstrapCreatesExactlyOneAdmin(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			results <- s.BootstrapUser(ctx, User{ID: fmt.Sprintf("admin-%d", i), Email: fmt.Sprintf("admin-%d@example.test", i), DisplayName: "Admin", PasswordHash: "test", Role: "admin"})
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("%d bootstrap winners", success)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.BootstrapUser(ctx, User{ID: "restart", Email: "restart@example.test", DisplayName: "Admin", PasswordHash: "test", Role: "admin"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("restart allowed bootstrap: %v", err)
	}
	if settings, err := reopened.Settings(ctx); err != nil || settings.Registration != "closed" {
		t.Fatalf("restart default: %+v %v", settings, err)
	}
}

func TestLegacyLocalHostRoleAndTenantRestrictions(t *testing.T) {
	s := accessStore(t)
	ctx := context.Background()
	local := Host{ID: "local", OwnerID: "member", Name: "Local", Transport: "local", Port: 22}
	if err := s.CreateHost(ctx, local); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("member created local host: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE users SET role='admin' WHERE id='member'`); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateHost(ctx, local); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE users SET role='user' WHERE id='member'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HostByID(ctx, "member", "local"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy local readable: %v", err)
	}
	if _, err := s.RenameHost(ctx, "member", "local", "changed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy local renamed: %v", err)
	}
	if err := s.TrustHostKey(ctx, "member", "local", "key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy local trust: %v", err)
	}
	if err := s.DeleteHost(ctx, "member", "local"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy local delete: %v", err)
	}
	for _, kind := range []string{"ssh", "tailcat"} {
		host := Host{ID: kind, OwnerID: "member", Name: kind, Transport: kind, Port: 22}
		if err := s.CreateHost(ctx, host); err != nil {
			t.Fatal(err)
		}
		if _, err := s.HostByID(ctx, "member", kind); err != nil {
			t.Fatal(err)
		}
		if _, err := s.HostByID(ctx, "admin", kind); !errors.Is(err, ErrNotFound) {
			t.Fatalf("admin bypassed ownership: %v", err)
		}
	}
	hosts, err := s.ListHosts(ctx, "member")
	if err != nil || len(hosts) != 2 {
		t.Fatalf("member list: %+v %v", hosts, err)
	}
	for _, h := range hosts {
		if h.Transport == "local" {
			t.Fatal("legacy local listed")
		}
	}
}
