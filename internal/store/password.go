package store

import "context"

// ChangePassword replaces the caller's own password hash and, in one
// transaction, ends every other Web login and every push subscription of the
// user (subscriptions are bound to the user, not to a login, so a lost device
// would otherwise keep receiving notifications). The update is conditioned on the
// hash the caller verified, so two concurrent changes cannot both succeed.
// keepSessionID must still be a live session of the user; it stays valid.
// Only Web login rows change here: no Herdr session, pane or task is touched.
func (s *Store) ChangePassword(ctx context.Context, userID, keepSessionID, verifiedHash, newHash string, event AuditEvent) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var kept bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=? AND user_id=?)`, keepSessionID, userID).Scan(&kept); err != nil {
		return 0, err
	}
	if !kept {
		return 0, ErrNotFound
	}
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?,must_change_password=0 WHERE id=? AND password_hash=? AND disabled=0`, newHash, userID, verifiedHash)
	if err != nil {
		return 0, err
	}
	if count, err := result.RowsAffected(); err != nil {
		return 0, err
	} else if count != 1 {
		return 0, ErrConflict
	}
	result, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND id<>?`, userID, keepSessionID)
	if err != nil {
		return 0, err
	}
	revoked, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE user_id=?`, userID); err != nil {
		return 0, err
	}
	if err := auditTx(ctx, tx, event); err != nil {
		return 0, err
	}
	return revoked, tx.Commit()
}

// ResetPassword lets an administrator replace another user's password with a
// temporary one. The user must change it after the next login, and all of the
// user's Web logins and push subscriptions end in the same transaction. The
// disabled state is left unchanged.
func (s *Store) ResetPassword(ctx context.Context, actor, userID, newHash string, event AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireAdminTx(ctx, tx, actor); err != nil {
		return err
	}
	if actor == userID {
		return ErrSelfReset
	}
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?,must_change_password=1 WHERE id=?`, newHash, userID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return ErrNotFound
	}
	if err := endUserAccessTx(ctx, tx, userID); err != nil {
		return err
	}
	if err := auditTx(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}
