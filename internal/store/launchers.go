package store

import (
	"context"
	"database/sql"
	"errors"
)

// UserLaunchers returns the canonical launcher JSON for the account.
// An empty string means the user has not saved any custom launchers.
func (s *Store) UserLaunchers(ctx context.Context, userID string) (string, error) {
	var document string
	err := s.db.QueryRowContext(ctx, `SELECT document FROM user_launchers WHERE user_id=?`, userID).Scan(&document)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return document, err
}

// SetUserLaunchers replaces the account's launcher document.
// The caller must already have validated and canonicalized it.
func (s *Store) SetUserLaunchers(ctx context.Context, userID, document string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_launchers(user_id,document,updated_at) VALUES(?,?,?)
ON CONFLICT(user_id) DO UPDATE SET document=excluded.document, updated_at=excluded.updated_at`, userID, document, now())
	return err
}
