package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("conflict")
	ErrEmailExists        = errors.New("email already exists")
	ErrAdminRequired      = errors.New("administrator required")
	ErrSelfDisable        = errors.New("cannot disable your own account")
	ErrLastAdmin          = errors.New("cannot disable the last administrator")
	ErrRegistrationClosed = errors.New("registration is closed")
	ErrKeyInUse           = errors.New("SSH key is used by hosts")
	ErrFolderCycle        = errors.New("folder cannot contain itself")
)

type Store struct {
	db *sql.DB
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"display_name"`
	Role         string    `json:"role"`
	PasswordHash string    `json:"-"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"created_at"`
}

type Session struct {
	ID        string
	UserID    string
	CSRFToken string
	ExpiresAt time.Time
}

type Host struct {
	ID                 string    `json:"id"`
	OwnerID            string    `json:"-"`
	Name               string    `json:"name"`
	Transport          string    `json:"transport"`
	Hostname           string    `json:"hostname,omitempty"`
	Port               int       `json:"port,omitempty"`
	Username           string    `json:"username,omitempty"`
	SessionName        string    `json:"session_name,omitempty"`
	AuthMethod         string    `json:"auth_method,omitempty"`
	CredentialID       string    `json:"-"`
	SSHKeyID           string    `json:"ssh_key_id,omitempty"`
	FolderID           string    `json:"folder_id,omitempty"`
	HostKey            string    `json:"host_key,omitempty"`
	PendingHostKey     string    `json:"pending_host_key,omitempty"`
	TailcatAddr        string    `json:"tailcat_addr,omitempty"`
	RootSSHFingerprint string    `json:"-"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type Credential struct {
	ID         string
	OwnerID    string
	Kind       string
	Ciphertext []byte
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dataDir, "herdrx.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		// Authentication revocations and completed pairings must survive loss of
		// the entire workbench host, not only a process restart.
		"PRAGMA synchronous=FULL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply %s: %w", pragma, err)
		}
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type schemaDB interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
}

func migrate(pool *sql.DB) error {
	db, err := pool.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 3 {
		return fmt.Errorf("database schema %d is newer than supported schema 3", version)
	}
	const schema = `
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  display_name TEXT NOT NULL,
  role TEXT NOT NULL CHECK(role IN ('admin','user')),
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS instance_settings (
  id INTEGER PRIMARY KEY CHECK(id=1),
  registration TEXT NOT NULL CHECK(registration IN ('closed','invite')),
  revision INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL
);
INSERT OR IGNORE INTO instance_settings(id,registration,updated_at) VALUES(1,'closed',strftime('%Y-%m-%dT%H:%M:%fZ','now'));
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash BLOB NOT NULL UNIQUE,
  csrf_token TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  remote_ip TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user_id ON sessions(user_id);
CREATE TABLE IF NOT EXISTS invites (
  id TEXT PRIMARY KEY,
  code_hash BLOB NOT NULL UNIQUE,
  created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  used_by TEXT REFERENCES users(id),
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS credentials (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  ciphertext BLOB NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS hosts (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  transport TEXT NOT NULL CHECK(transport IN ('local','ssh','tailcat')),
  hostname TEXT NOT NULL DEFAULT '',
  port INTEGER NOT NULL DEFAULT 22,
  username TEXT NOT NULL DEFAULT '',
  session_name TEXT NOT NULL DEFAULT '',
  auth_method TEXT NOT NULL DEFAULT '',
  credential_id TEXT REFERENCES credentials(id) ON DELETE SET NULL,
  host_key TEXT NOT NULL DEFAULT '',
  pending_host_key TEXT NOT NULL DEFAULT '',
  tailcat_addr TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ssh_keys (
  id TEXT PRIMARY KEY REFERENCES credentials(id) ON DELETE CASCADE,
  owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  public_key TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  algorithm TEXT NOT NULL,
  certificate TEXT NOT NULL DEFAULT '',
  encrypted INTEGER NOT NULL DEFAULT 0,
  revision INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ssh_keys_owner ON ssh_keys(owner_id);
CREATE TABLE IF NOT EXISTS host_folders (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  parent_id TEXT REFERENCES host_folders(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS host_folders_owner ON host_folders(owner_id);
CREATE INDEX IF NOT EXISTS hosts_owner_id ON hosts(owner_id);
CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  action TEXT NOT NULL,
  target_type TEXT NOT NULL DEFAULT '',
  target_id TEXT NOT NULL DEFAULT '',
  remote_ip TEXT NOT NULL DEFAULT '',
  details TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS pairing_setups (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id TEXT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
  node_public TEXT NOT NULL,
  ssh_public TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_at TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS push_subscriptions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  endpoint TEXT NOT NULL UNIQUE,
  p256dh TEXT NOT NULL,
  auth TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS enrollment_tasks (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL,
  root_ssh_fp TEXT NOT NULL,
  request_id TEXT NOT NULL,
  controller_id TEXT NOT NULL,
  credential_id TEXT NOT NULL REFERENCES credentials(id),
  phase TEXT NOT NULL,
  host_id TEXT,
  enrollment_id TEXT NOT NULL,
  binding_id TEXT NOT NULL DEFAULT '',
  challenge TEXT NOT NULL DEFAULT '',
  formal_addr_ciphertext BLOB,
  prepared_expires_at TEXT,
  claim_token TEXT NOT NULL,
  lease_until TEXT,
  error TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  session_name TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS enrollment_tasks_owner ON enrollment_tasks(owner_id);
CREATE UNIQUE INDEX IF NOT EXISTS enrollment_tasks_owner_inflight
  ON enrollment_tasks(owner_id, agent_id) WHERE phase NOT IN ('active','failed');`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate sqlite schema: %w", err)
	}
	if err := ensureHostColumns(db); err != nil {
		return err
	}
	if err := ensureColumn(db, "hosts", "folder_id", `ALTER TABLE hosts ADD COLUMN folder_id TEXT REFERENCES host_folders(id) ON DELETE SET NULL`); err != nil {
		return err
	}
	for _, column := range []struct{ name, ddl string }{
		{"revoked_at", "ALTER TABLE invites ADD COLUMN revoked_at TEXT"},
		{"revoked_by", "ALTER TABLE invites ADD COLUMN revoked_by TEXT REFERENCES users(id)"},
		{"used_at", "ALTER TABLE invites ADD COLUMN used_at TEXT"},
	} {
		if err := ensureColumn(db, "invites", column.name, column.ddl); err != nil {
			return err
		}
	}
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS invites_created ON invites(created_at, id);
CREATE INDEX IF NOT EXISTS audit_created ON audit_log(created_at, id);
CREATE INDEX IF NOT EXISTS audit_actor ON audit_log(user_id, id);
PRAGMA user_version=3;`)
	if err != nil {
		return err
	}
	return db.Commit()
}

func ensureHostColumns(db schemaDB) error {
	rows, err := db.Query("PRAGMA table_info(hosts)")
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&index, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == "tailcat_addr" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !found {
		if _, err := db.Exec(`ALTER TABLE hosts ADD COLUMN tailcat_addr TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add hosts.tailcat_addr: %w", err)
		}
	}
	if err := ensureColumn(db, "hosts", "root_ssh_fp", `ALTER TABLE hosts ADD COLUMN root_ssh_fp TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS hosts_owner_root_ssh ON hosts(owner_id, root_ssh_fp) WHERE transport='tailcat' AND root_ssh_fp != ''`)
	return err
}

func ensureColumn(db schemaDB, table, column, alter string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&index, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := db.Exec(alter); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var user User
	var disabled int
	var created string
	err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName, &user.Role, &disabled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	user.Disabled = disabled != 0
	user.CreatedAt = parseTime(created)
	return user, nil
}

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
