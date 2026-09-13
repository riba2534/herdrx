package store

import (
	"context"
	"errors"
	"testing"
)

func TestWorkbenchSessionIsTenantScopedAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	dataStore, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, user := range []User{
		{ID: "usr_a", Email: "a@example.test", PasswordHash: "hash", DisplayName: "A", Role: "admin"},
		{ID: "usr_b", Email: "b@example.test", PasswordHash: "hash", DisplayName: "B", Role: "user"},
	} {
		if err := dataStore.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	if err := dataStore.CreateHost(ctx, Host{ID: "hst_a", OwnerID: "usr_a", Name: "A", Transport: "local", Port: 22}); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.PutWorkbenchSession(ctx, WorkbenchSession{
		UserID: "usr_b", HostID: "hst_a", WorkspaceID: "w1", DeviceID: "phone", ClientClass: "mobile",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant wrote host session: %v", err)
	}
	saved, err := dataStore.PutWorkbenchSession(ctx, WorkbenchSession{
		UserID: "usr_a", HostID: "hst_a", WorkspaceID: "w2", TabID: "w2:t1", PaneID: "w2:p1", DeviceID: "pc-1", ClientClass: "desktop",
	})
	if err != nil || saved.WorkspaceID != "w2" || saved.UpdatedAt.IsZero() {
		t.Fatalf("put: %+v %v", saved, err)
	}
	if _, err := dataStore.WorkbenchSession(ctx, "usr_b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant read session: %v", err)
	}
	dataStore.Close()

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.WorkbenchSession(ctx, "usr_a")
	if err != nil || got.HostID != "hst_a" || got.WorkspaceID != "w2" || got.TabID != "w2:t1" || got.PaneID != "w2:p1" || got.DeviceID != "pc-1" || got.ClientClass != "desktop" {
		t.Fatalf("restored: %+v %v", got, err)
	}
}

func TestWorkbenchSessionClearsDeletedHost(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	ctx := context.Background()
	if err := dataStore.CreateUser(ctx, User{ID: "usr_a", Email: "a@example.test", PasswordHash: "hash", DisplayName: "A", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.CreateHost(ctx, Host{ID: "hst_gone", OwnerID: "usr_a", Name: "Gone", Transport: "local", Port: 22}); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.PutWorkbenchSession(ctx, WorkbenchSession{UserID: "usr_a", HostID: "hst_gone", WorkspaceID: "w1", ClientClass: "desktop"}); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.DeleteHost(ctx, "usr_a", "hst_gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.WorkbenchSession(ctx, "usr_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted host still restored: %v", err)
	}
}
