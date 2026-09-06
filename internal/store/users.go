package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) UserCount(ctx context.Context) (int, error) {
	var count int
	return count, s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count)
}

func (s *Store) CreateUser(ctx context.Context, user User) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,display_name,role,created_at) VALUES(?,?,?,?,?,?)`,
		user.ID, strings.ToLower(strings.TrimSpace(user.Email)), user.PasswordHash, user.DisplayName, user.Role, now())
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (s *Store) BootstrapUser(ctx context.Context, user User, events ...AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("%w: administrator already exists", ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,display_name,role,created_at) VALUES(?,?,?,?,?,?)`,
		user.ID, strings.ToLower(strings.TrimSpace(user.Email)), user.PasswordHash, user.DisplayName, user.Role, now()); err != nil {
		return err
	}
	if err := auditTx(ctx, tx, events...); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id,email,password_hash,display_name,role,disabled,created_at FROM users WHERE email=?`, strings.ToLower(strings.TrimSpace(email))))
}

func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id,email,password_hash,display_name,role,disabled,created_at FROM users WHERE id=?`, id))
}

func (s *Store) CreateSession(ctx context.Context, session Session, tokenHash []byte, userAgent, remoteIP string, events ...AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,user_id,token_hash,csrf_token,user_agent,remote_ip,expires_at,created_at)
SELECT ?,id,?,?,?,?,?,? FROM users WHERE id=? AND disabled=0`,
		session.ID, tokenHash, session.CSRFToken, userAgent, remoteIP, session.ExpiresAt.UTC().Format(timeFormat), now(), session.UserID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	if err := auditTx(ctx, tx, events...); err != nil {
		return err
	}
	return tx.Commit()
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func (s *Store) SessionByTokenHash(ctx context.Context, tokenHash []byte) (Session, User, error) {
	var session Session
	var user User
	var disabled int
	var expires, created string
	err := s.db.QueryRowContext(ctx, `
SELECT s.id,s.user_id,s.csrf_token,s.expires_at,u.id,u.email,u.password_hash,u.display_name,u.role,u.disabled,u.created_at
FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=?`, tokenHash).Scan(
		&session.ID, &session.UserID, &session.CSRFToken, &expires,
		&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName, &user.Role, &disabled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrNotFound
	}
	if err != nil {
		return Session{}, User{}, err
	}
	session.ExpiresAt = parseTime(expires)
	user.Disabled = disabled != 0
	user.CreatedAt = parseTime(created)
	return session, user, nil
}

func (s *Store) DeleteSession(ctx context.Context, id, userID string, events ...AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE id=? AND user_id=?", id, userID); err != nil {
		return err
	}
	if err := auditTx(ctx, tx, events...); err != nil {
		return err
	}
	return tx.Commit()
}
