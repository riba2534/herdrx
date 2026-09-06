package hostruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/store"
)

func TestLocalEndpointRequiresEnabledAdministrator(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, u := range []store.User{{ID: "admin", Email: "admin@example.test", DisplayName: "Admin", PasswordHash: "test", Role: "admin"}, {ID: "member", Email: "member@example.test", DisplayName: "Member", PasswordHash: "test", Role: "user"}, {ID: "disabled", Email: "disabled@example.test", DisplayName: "Disabled", PasswordHash: "test", Role: "admin"}} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetUserDisabled(ctx, "admin", "disabled", true, store.AuditEvent{UserID: "admin", Action: "user.disabled"}); err != nil {
		t.Fatal(err)
	}
	f := &Factory{Store: s, Config: config.Config{HerdrBinary: "unused-test-herdr"}}
	defer f.Close()
	for _, id := range []string{"member", "disabled"} {
		if endpoint, err := f.Open(ctx, store.Host{ID: "legacy", OwnerID: id, Transport: "local"}); !errors.Is(err, store.ErrAdminRequired) || endpoint != nil {
			t.Fatalf("%s opened local endpoint: %v", id, err)
		}
	}
	empty := &Factory{}
	if endpoint, err := empty.Open(ctx, store.Host{Transport: "local"}); !errors.Is(err, store.ErrAdminRequired) || endpoint != nil {
		t.Fatalf("unconfigured factory opened local: %v", err)
	}
}
