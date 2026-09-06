package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type PairingSetup struct {
	ID           string
	OwnerID      string
	CredentialID string
	NodePublic   string
	SSHPublic    string
	ExpiresAt    time.Time
	UsedAt       time.Time
}

func (s *Store) CreatePairingSetup(ctx context.Context, setup PairingSetup) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO pairing_setups(id,owner_id,credential_id,node_public,ssh_public,expires_at,created_at) VALUES(?,?,?,?,?,?,?)`,
		setup.ID, setup.OwnerID, setup.CredentialID, setup.NodePublic, setup.SSHPublic, setup.ExpiresAt.UTC().Format(timeFormat), now())
	return err
}

func (s *Store) PairingSetupByID(ctx context.Context, ownerID, id string) (PairingSetup, error) {
	var setup PairingSetup
	var expires string
	var used sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,owner_id,credential_id,node_public,ssh_public,expires_at,used_at FROM pairing_setups WHERE id=? AND owner_id=?`, id, ownerID).Scan(
		&setup.ID, &setup.OwnerID, &setup.CredentialID, &setup.NodePublic, &setup.SSHPublic, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return PairingSetup{}, ErrNotFound
	}
	if err != nil {
		return PairingSetup{}, err
	}
	setup.ExpiresAt = parseTime(expires)
	if used.Valid {
		setup.UsedAt = parseTime(used.String)
	}
	return setup, nil
}

func (s *Store) MarkPairingSetupUsed(ctx context.Context, ownerID, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE pairing_setups SET used_at=? WHERE id=? AND owner_id=? AND used_at IS NULL`, now(), id, ownerID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrNotFound
	}
	return nil
}
