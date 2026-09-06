package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SSHKey contains display metadata only. Secret material stays in credentials.
type SSHKey struct {
	ID          string    `json:"id"`
	OwnerID     string    `json:"-"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	Algorithm   string    `json:"algorithm"`
	Certificate string    `json:"certificate,omitempty"`
	Encrypted   bool      `json:"encrypted"`
	Revision    int       `json:"revision"`
	HostCount   int       `json:"host_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const keyColumns = `id,owner_id,name,public_key,fingerprint,algorithm,certificate,encrypted,revision,created_at,updated_at,(SELECT count(*) FROM hosts WHERE credential_id=ssh_keys.id AND owner_id=ssh_keys.owner_id)`

func scanSSHKey(row interface{ Scan(...any) error }) (SSHKey, error) {
	var k SSHKey
	var created, updated string
	err := row.Scan(&k.ID, &k.OwnerID, &k.Name, &k.PublicKey, &k.Fingerprint, &k.Algorithm, &k.Certificate, &k.Encrypted, &k.Revision, &created, &updated, &k.HostCount)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	k.CreatedAt, k.UpdatedAt = parseTime(created), parseTime(updated)
	return k, err
}

func (s *Store) ListSSHKeys(ctx context.Context, owner string) ([]SSHKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+keyColumns+` FROM ssh_keys WHERE owner_id=? ORDER BY name COLLATE NOCASE,id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]SSHKey, 0)
	for rows.Next() {
		k, err := scanSSHKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) SSHKeyByID(ctx context.Context, owner, id string) (SSHKey, error) {
	return scanSSHKey(s.db.QueryRowContext(ctx, `SELECT `+keyColumns+` FROM ssh_keys WHERE owner_id=? AND id=?`, owner, id))
}

func (s *Store) CreateSSHKey(ctx context.Context, k SSHKey, c Credential) error {
	if k.ID != c.ID || k.OwnerID != c.OwnerID || c.Kind != "saved_key" {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := now()
	if _, err = tx.ExecContext(ctx, `INSERT INTO credentials(id,owner_id,kind,ciphertext,created_at) VALUES(?,?,?,?,?)`, c.ID, c.OwnerID, c.Kind, c.Ciphertext, stamp); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ssh_keys(id,owner_id,name,public_key,fingerprint,algorithm,certificate,encrypted,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, k.ID, k.OwnerID, k.Name, k.PublicKey, k.Fingerprint, k.Algorithm, k.Certificate, k.Encrypted, stamp, stamp); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateSSHKey(ctx context.Context, k SSHKey, ciphertext []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE ssh_keys SET name=?,public_key=?,fingerprint=?,algorithm=?,certificate=?,encrypted=?,revision=revision+1,updated_at=? WHERE id=? AND owner_id=? AND revision=?`, k.Name, k.PublicKey, k.Fingerprint, k.Algorithm, k.Certificate, k.Encrypted, now(), k.ID, k.OwnerID, k.Revision)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrConflict
	}
	if ciphertext != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE credentials SET ciphertext=? WHERE id=? AND owner_id=? AND kind='saved_key'`, ciphertext, k.ID, k.OwnerID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteSSHKey(ctx context.Context, owner, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var used int
	err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM hosts WHERE credential_id=ssh_keys.id) FROM ssh_keys WHERE id=? AND owner_id=?`, id, owner).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if used > 0 {
		return ErrKeyInUse
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM credentials WHERE id=? AND owner_id=? AND kind='saved_key'`, id, owner); err != nil {
		return err
	}
	return tx.Commit()
}
