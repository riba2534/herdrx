package store

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateLegacyDataAndRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "herdrx.db"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/schema-v0.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	for _, query := range []string{
		`INSERT INTO users VALUES('admin','admin@example.test','original-password-hash','Admin','admin',0,'2026-01-01T00:00:00Z')`,
		`INSERT INTO credentials VALUES('credential','admin','tailcat',X'01020304','2026-01-01T00:00:00Z')`,
		`INSERT INTO hosts(id,owner_id,name,transport,credential_id,root_ssh_fp,tailcat_addr,created_at,updated_at) VALUES('host','admin','Remote','tailcat','credential','original-fingerprint','original-address','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO sessions(id,user_id,token_hash,csrf_token,expires_at,created_at) VALUES('login','admin',X'05','original-csrf',?,'2026-01-01T00:00:00Z')`, expires); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO invites(id,code_hash,created_by,expires_at,created_at) VALUES('invite',X'06','admin',?,'2026-01-01T00:00:00Z')`, expires); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for i := range 2 {
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		settings, err := s.Settings(ctx)
		if err != nil || (i == 0 && settings.Registration != "closed") || (i == 1 && (settings.Registration != "invite" || settings.Revision != 1)) {
			t.Fatalf("migrated/reopened registration settings: %+v %v", settings, err)
		}
		user, err := s.UserByID(ctx, "admin")
		if err != nil || user.PasswordHash != "original-password-hash" {
			t.Fatalf("user changed: %+v %v", user, err)
		}
		credential, err := s.CredentialByID(ctx, "admin", "credential")
		if err != nil || !bytes.Equal(credential.Ciphertext, []byte{1, 2, 3, 4}) {
			t.Fatal("encrypted credential changed")
		}
		host, err := s.HostByID(ctx, "admin", "host")
		if err != nil || host.RootSSHFingerprint != "original-fingerprint" || host.TailcatAddr != "original-address" {
			t.Fatal("host identity changed")
		}
		login, _, err := s.SessionByTokenHash(ctx, []byte{5})
		if err != nil || login.CSRFToken != "original-csrf" {
			t.Fatal("existing login changed")
		}
		if i == 0 {
			if _, err = s.SetRegistration(ctx, "admin", "invite", 0, AuditEvent{UserID: "admin", Action: "registration.changed"}); err != nil {
				t.Fatal(err)
			}
			if err = s.RevokeInvite(ctx, "admin", "invite", AuditEvent{UserID: "admin", Action: "invite.revoked"}); err != nil {
				t.Fatal(err)
			}
		}
		invites, err := s.ListInvites(ctx)
		if err != nil || len(invites) != 1 || invites[0].Status != "revoked" {
			t.Fatal("revocation did not survive restart")
		}
		var version int
		if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
			t.Fatal("migration version not recorded")
		}
		s.Close()
	}
}

func TestUnsupportedSchemaFailsClosed(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if reopened, err := Open(dir); err == nil {
		reopened.Close()
		t.Fatal("newer schema silently accepted")
	}
}
