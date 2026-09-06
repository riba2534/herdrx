package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SessionAccess checks only Web authorization. It never touches Herdr or a PTY.
func (s *Store) SessionAccess(ctx context.Context, id, userID string) (time.Time, error) {
	var expires string
	var disabled bool
	err := s.db.QueryRowContext(ctx, `SELECT s.expires_at,u.disabled FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.id=? AND s.user_id=?`, id, userID).Scan(&expires, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	deadline := parseTime(expires)
	if disabled || !deadline.After(time.Now()) {
		return time.Time{}, ErrNotFound
	}
	return deadline, nil
}

func (s *Store) UserEnabled(ctx context.Context, id string) error {
	var disabled bool
	err := s.db.QueryRowContext(ctx, `SELECT disabled FROM users WHERE id=?`, id).Scan(&disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if disabled {
		return ErrNotFound
	}
	return nil
}

type AuditEvent struct {
	UserID     string
	Action     string
	TargetType string
	TargetID   string
	RemoteIP   string
	Details    string
}

func auditTx(ctx context.Context, tx *sql.Tx, events ...AuditEvent) error {
	for _, event := range events {
		if event.Details == "" {
			event.Details = "{}"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log(user_id,action,target_type,target_id,remote_ip,details,created_at) VALUES(?,?,?,?,?,?,?)`, nullable(event.UserID), event.Action, event.TargetType, event.TargetID, event.RemoteIP, event.Details, now()); err != nil {
			return err
		}
	}
	return nil
}

// Recheck the actor inside the same write transaction as the admin mutation.
// Two administrators concurrently disabling one another cannot act as a user
// that the preceding transaction has already disabled.
func requireAdminTx(ctx context.Context, tx *sql.Tx, actor string) error {
	var role string
	var disabled bool
	err := tx.QueryRowContext(ctx, `SELECT role,disabled FROM users WHERE id=?`, actor).Scan(&role, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAdminRequired
	}
	if err != nil {
		return err
	}
	if role != "admin" || disabled {
		return ErrAdminRequired
	}
	return nil
}

func (s *Store) SetUserDisabled(ctx context.Context, actor, id string, disabled bool, event AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireAdminTx(ctx, tx, actor); err != nil {
		return err
	}
	var role string
	var wasDisabled bool
	err = tx.QueryRowContext(ctx, `SELECT role,disabled FROM users WHERE id=?`, id).Scan(&role, &wasDisabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if disabled && role == "admin" && !wasDisabled {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0`).Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return ErrLastAdmin
		}
	}
	if disabled && actor == id {
		return ErrSelfDisable
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled=? WHERE id=?`, disabled, id); err != nil {
		return err
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE user_id=?`, id); err != nil {
			return err
		}
	}
	if err := auditTx(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeUserSessions(ctx context.Context, actor, userID, sessionID string, event AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireAdminTx(ctx, tx, actor); err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=?`, userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	if sessionID != "" {
		var owner string
		err := tx.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE id=?`, sessionID).Scan(&owner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && owner != userID {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND id=?`, userID, sessionID); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID); err != nil {
		return err
	}
	if err := auditTx(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}
