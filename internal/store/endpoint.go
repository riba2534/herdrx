package store

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) ActiveBindingForHost(ctx context.Context, host Host) (string, error) {
	var binding string
	err := s.db.QueryRowContext(ctx, `SELECT binding_id FROM enrollment_tasks WHERE owner_id=? AND host_id=? AND credential_id=? AND phase='active'`, host.OwnerID, host.ID, host.CredentialID).Scan(&binding)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return binding, err
}

// The encrypted credential contains the endpoint revision. Comparing its old
// ciphertext in the same transaction prevents replay and concurrent lost writes.
func (s *Store) UpdateTailcatEndpoint(ctx context.Context, host Host, previous, next []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE credentials SET ciphertext=? WHERE id=? AND owner_id=? AND kind='tailcat' AND ciphertext=? AND EXISTS (SELECT 1 FROM hosts WHERE id=? AND owner_id=? AND credential_id=credentials.id AND transport='tailcat')`, next, host.CredentialID, host.OwnerID, previous, host.ID, host.OwnerID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hosts SET tailcat_addr='',updated_at=? WHERE id=? AND owner_id=?`, now(), host.ID, host.OwnerID); err != nil {
		return err
	}
	return tx.Commit()
}
