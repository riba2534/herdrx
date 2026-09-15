package store

import (
	"context"
	"errors"
	"testing"
	"time"
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

func TestWorkbenchSessionWriterOrderSurvivesRestartAndOtherWriters(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.CreateUser(ctx, User{ID: "usr", Email: "writer@example.test", PasswordHash: "hash", DisplayName: "Writer", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateHost(ctx, Host{ID: "host", OwnerID: "usr", Name: "Host", Transport: "local", Port: 22}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"login-a", "login-b"} {
		if err := db.CreateSession(ctx, Session{ID: id, UserID: "usr", CSRFToken: id, ExpiresAt: time.Now().Add(time.Hour)}, []byte(id), "", ""); err != nil {
			t.Fatal(err)
		}
	}
	write := func(login, writer, workspace string, sequence int64) error {
		_, err := db.PutWorkbenchSession(ctx, WorkbenchSession{UserID: "usr", HostID: "host", WorkspaceID: workspace, LoginSessionID: login, WriterID: writer, Sequence: sequence})
		return err
	}
	if err := write("login-a", "page-a", "latest-a", 2); err != nil {
		t.Fatal(err)
	}
	if err := write("login-b", "page-b", "latest-b", 1); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, sequence := range []int64{1, 2} {
		if err := write("login-a", "page-a", "late-a", sequence); !errors.Is(err, ErrStaleWorkbenchSession) {
			t.Fatalf("late sequence %d accepted: %v", sequence, err)
		}
	}
	got, err := db.WorkbenchSession(ctx, "usr")
	if err != nil || got.WorkspaceID != "latest-b" {
		t.Fatalf("late writer replaced newer device: %+v %v", got, err)
	}
	if err := write("login-a", "page-a", "newest-a", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE sessions SET expires_at='2000-01-01T00:00:00Z' WHERE id='login-b'`); err != nil {
		t.Fatal(err)
	}
	if err := write("login-a", "page-a", "newest-a", 4); err != nil {
		t.Fatal(err)
	}
	var expiredRows int
	if err := db.db.QueryRowContext(ctx, `SELECT count(*) FROM user_workbench_writers WHERE session_id='login-b'`).Scan(&expiredRows); err != nil || expiredRows != 0 {
		t.Fatalf("expired login retained writers: %d %v", expiredRows, err)
	}
	if err := write("login-b", "page-b", "expired", 2); !errors.Is(err, ErrStaleWorkbenchSession) {
		t.Fatalf("expired login accepted: %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `DELETE FROM sessions WHERE id='login-a'`); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.db.QueryRowContext(ctx, `SELECT count(*) FROM user_workbench_writers WHERE session_id='login-a'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("revoked login retained writers: %d %v", rows, err)
	}
	if err := write("login-a", "page-a", "revoked", 4); !errors.Is(err, ErrStaleWorkbenchSession) {
		t.Fatalf("revoked login accepted: %v", err)
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
