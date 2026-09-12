package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func (s *Store) CreateCredential(ctx context.Context, credential Credential) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO credentials(id,owner_id,kind,ciphertext,created_at) VALUES(?,?,?,?,?)`,
		credential.ID, credential.OwnerID, credential.Kind, credential.Ciphertext, now())
	return err
}

func (s *Store) UpdateCredential(ctx context.Context, ownerID, id, kind string, ciphertext []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE credentials SET kind=?, ciphertext=? WHERE id=? AND owner_id=?`,
		kind, ciphertext, id, ownerID)
	return err
}

func (s *Store) CredentialByID(ctx context.Context, ownerID, id string) (Credential, error) {
	var credential Credential
	err := s.db.QueryRowContext(ctx, `SELECT id,owner_id,kind,ciphertext FROM credentials WHERE id=? AND owner_id=?`, id, ownerID).Scan(
		&credential.ID, &credential.OwnerID, &credential.Kind, &credential.Ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	return credential, err
}

func (s *Store) CreateHost(ctx context.Context, host Host) error {
	return s.SaveHost(ctx, host, nil, nil)
}

// SaveHost commits credential and host changes together. A shared SSH key is
// referenced directly, and deleting a host must never delete that shared key.
func (s *Store) SaveHost(ctx context.Context, host Host, credential *Credential, previous *Host) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if host.Transport == "local" || host.AuthMethod == "system_ssh" {
		var allowed bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND role='admin' AND disabled=0)`, host.OwnerID).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return ErrAdminRequired
		}
	}
	if host.FolderID != "" {
		if _, err = folderParent(ctx, tx, host.OwnerID, host.FolderID); err != nil {
			return err
		}
	}
	if credential != nil {
		if credential.ID != host.CredentialID || credential.OwnerID != host.OwnerID {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO credentials(id,owner_id,kind,ciphertext,created_at) VALUES(?,?,?,?,?)`, credential.ID, credential.OwnerID, credential.Kind, credential.Ciphertext, now()); err != nil {
			return err
		}
	}
	if host.CredentialID != "" {
		var valid bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM credentials WHERE id=? AND owner_id=?)`, host.CredentialID, host.OwnerID).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return ErrNotFound
		}
		if host.AuthMethod == "saved_key" {
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ssh_keys WHERE id=? AND owner_id=?)`, host.CredentialID, host.OwnerID).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return ErrNotFound
			}
		}
	}
	stamp := now()
	if previous == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO hosts(id,owner_id,name,transport,hostname,port,username,session_name,proxy_jump,auth_method,credential_id,host_key,pending_host_key,tailcat_addr,root_ssh_fp,folder_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			host.ID, host.OwnerID, host.Name, host.Transport, host.Hostname, host.Port, host.Username, host.SessionName, host.ProxyJump, host.AuthMethod, nullable(host.CredentialID), host.HostKey, host.PendingHostKey, host.TailcatAddr, host.RootSSHFingerprint, nullable(host.FolderID), stamp, stamp)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE hosts SET name=?,hostname=?,port=?,username=?,session_name=?,proxy_jump=?,auth_method=?,credential_id=?,host_key=?,pending_host_key=?,folder_id=?,updated_at=? WHERE id=? AND owner_id=? AND transport='ssh' AND updated_at=?`,
			host.Name, host.Hostname, host.Port, host.Username, host.SessionName, host.ProxyJump, host.AuthMethod, nullable(host.CredentialID), host.HostKey, host.PendingHostKey, nullable(host.FolderID), stamp, host.ID, host.OwnerID, previous.UpdatedAt.UTC().Format(time.RFC3339Nano))
		if err == nil {
			count, _ := result.RowsAffected()
			if count != 1 {
				return ErrConflict
			}
		}
	}
	if err != nil {
		return err
	}
	if previous != nil && previous.CredentialID != "" && previous.CredentialID != host.CredentialID {
		if err = deleteUnusedCredential(ctx, tx, host.OwnerID, previous.CredentialID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func deleteUnusedCredential(ctx context.Context, tx *sql.Tx, owner, id string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE id=? AND owner_id=? AND NOT EXISTS(SELECT 1 FROM hosts WHERE credential_id=credentials.id) AND NOT EXISTS(SELECT 1 FROM ssh_keys WHERE id=credentials.id)`, id, owner)
	return err
}

// Revalidate local access for existing records as well as newly created hosts.
// This also filters legacy records owned by users who are no longer admins.
const hostColumns = `id,owner_id,name,transport,hostname,port,username,session_name,COALESCE(proxy_jump,''),auth_method,COALESCE(credential_id,''),host_key,pending_host_key,tailcat_addr,COALESCE(root_ssh_fp,''),created_at,updated_at,COALESCE(folder_id,''),COALESCE((SELECT id FROM ssh_keys WHERE id=hosts.credential_id AND owner_id=hosts.owner_id),'')`

const allowedHost = `((transport<>'local' AND auth_method<>'system_ssh') OR EXISTS (SELECT 1 FROM users WHERE users.id=hosts.owner_id AND users.role='admin' AND users.disabled=0))`

func (s *Store) ListHosts(ctx context.Context, ownerID string) ([]Host, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+hostColumns+` FROM hosts WHERE owner_id=? AND `+allowedHost+` ORDER BY created_at`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hosts := make([]Host, 0)
	for rows.Next() {
		host, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, rows.Err()
}

func (s *Store) HostByID(ctx context.Context, ownerID, id string) (Host, error) {
	host, err := scanHost(s.db.QueryRowContext(ctx, `SELECT `+hostColumns+` FROM hosts WHERE id=? AND owner_id=? AND `+allowedHost, id, ownerID))
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, ErrNotFound
	}
	return host, err
}

func (s *Store) RenameHost(ctx context.Context, ownerID, id, name string) (Host, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE hosts SET name=?,updated_at=? WHERE id=? AND owner_id=? AND `+allowedHost, name, now(), id, ownerID)
	if err != nil {
		return Host{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Host{}, err
	}
	if count == 0 {
		return Host{}, ErrNotFound
	}
	return s.HostByID(ctx, ownerID, id)
}

func (s *Store) TrustHostKey(ctx context.Context, ownerID, id, key string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE hosts SET host_key=?,pending_host_key='',updated_at=? WHERE id=? AND owner_id=? AND pending_host_key=? AND `+allowedHost, key, now(), id, ownerID, key)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetPendingHostKey(ctx context.Context, ownerID, id, key string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE hosts SET pending_host_key=?,updated_at=? WHERE id=? AND owner_id=?`, key, now(), id, ownerID)
	return err
}

func (s *Store) DeleteHost(ctx context.Context, ownerID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var credentialID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(credential_id,'') FROM hosts WHERE id=? AND owner_id=? AND `+allowedHost, id, ownerID).Scan(&credentialID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hosts WHERE id=? AND owner_id=?`, id, ownerID); err != nil {
		return err
	}
	if credentialID != "" {
		if err := deleteUnusedCredential(ctx, tx, ownerID, credentialID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Audit(ctx context.Context, userID, action, targetType, targetID, remoteIP, details string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO audit_log(user_id,action,target_type,target_id,remote_ip,details,created_at) VALUES(?,?,?,?,?,?,?)`,
		nullable(userID), action, targetType, targetID, remoteIP, details, now())
}

func scanHost(row interface{ Scan(...any) error }) (Host, error) {
	var host Host
	var created, updated string
	err := row.Scan(&host.ID, &host.OwnerID, &host.Name, &host.Transport, &host.Hostname, &host.Port, &host.Username,
		&host.SessionName, &host.ProxyJump, &host.AuthMethod, &host.CredentialID, &host.HostKey, &host.PendingHostKey, &host.TailcatAddr, &host.RootSSHFingerprint, &created, &updated, &host.FolderID, &host.SSHKeyID)
	if err != nil {
		return Host{}, err
	}
	host.CreatedAt = parseTime(created)
	host.UpdatedAt = parseTime(updated)
	return host, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) DatabasePath() string {
	var path string
	_ = s.db.QueryRow("PRAGMA database_list").Scan(new(int), new(string), &path)
	return path
}

func (s *Store) Backup(ctx context.Context, target string) error {
	if target == "" {
		return fmt.Errorf("backup target is required")
	}
	// Reserve an owner-only empty file before SQLite writes it. Never overwrite
	// an existing backup, and do not leave a readable partial copy on failure.
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(target)
		return err
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", target); err != nil {
		_ = os.Remove(target)
		return err
	}
	file, err = os.Open(target)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// A handshake from an obsolete configuration cannot replace the fingerprint
// presented for a newly edited host. Trust also compares the pending key.
func (s *Store) SetPendingHostKeyIfCurrent(ctx context.Context, host Host, key string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE hosts SET pending_host_key=? WHERE id=? AND owner_id=? AND hostname=? AND port=? AND username=? AND auth_method=? AND COALESCE(credential_id,'')=? AND host_key=?`, key, host.ID, host.OwnerID, host.Hostname, host.Port, host.Username, host.AuthMethod, host.CredentialID, host.HostKey)
	return err
}
