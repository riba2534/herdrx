-- Historical schema before access-management migration.

CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  display_name TEXT NOT NULL,
  role TEXT NOT NULL CHECK(role IN ('admin','user')),
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
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
  ON enrollment_tasks(owner_id, agent_id) WHERE phase NOT IN ('active','failed');
ALTER TABLE hosts ADD COLUMN root_ssh_fp TEXT NOT NULL DEFAULT '';
