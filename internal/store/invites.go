package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Invite struct {
	ID        string     `json:"id"`
	CreatedBy string     `json:"created_by"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedBy    string     `json:"used_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	RevokedBy string     `json:"revoked_by,omitempty"`
	Status    string     `json:"status"`
}

func (s *Store) CreateInvite(ctx context.Context, invite Invite, codeHash []byte, events ...AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireAdminTx(ctx, tx, invite.CreatedBy); err != nil {
		return err
	}
	if err := requireRegistrationTx(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO invites(id,code_hash,created_by,expires_at,created_at) VALUES(?,?,?,?,?)`, invite.ID, codeHash, invite.CreatedBy, invite.ExpiresAt.UTC().Format(timeFormat), now()); err != nil {
		return err
	}
	if err := auditTx(ctx, tx, events...); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListInvites(ctx context.Context, pages ...Page) ([]Invite, error) {
	page := Page{Limit: 100}
	if len(pages) > 0 {
		page = pages[0]
	}
	page = page.Bounded()
	rows, err := s.db.QueryContext(ctx, `SELECT id,created_by,expires_at,COALESCE(used_by,''),created_at,used_at,revoked_at,COALESCE(revoked_by,'') FROM invites ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, page.Limit+1, page.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	invites := make([]Invite, 0)
	for rows.Next() {
		var invite Invite
		var expires, created string
		var used, revoked sql.NullString
		if err := rows.Scan(&invite.ID, &invite.CreatedBy, &expires, &invite.UsedBy, &created, &used, &revoked, &invite.RevokedBy); err != nil {
			return nil, err
		}
		invite.ExpiresAt = parseTime(expires)
		invite.CreatedAt = parseTime(created)
		invite.UsedAt = optionalTime(used)
		invite.RevokedAt = optionalTime(revoked)
		switch {
		case invite.UsedBy != "":
			invite.Status = "used"
		case invite.RevokedAt != nil:
			invite.Status = "revoked"
		case !invite.ExpiresAt.After(time.Now()):
			invite.Status = "expired"
		default:
			invite.Status = "active"
		}
		invites = append(invites, invite)
	}
	return invites, rows.Err()
}

func optionalTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	t := parseTime(value.String)
	return &t
}

// CheckInvite never reserves a code. The final transaction repeats this check.
func (s *Store) CheckInvite(ctx context.Context, codeHash []byte) error {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM invites WHERE code_hash=? AND used_by IS NULL AND revoked_at IS NULL AND julianday(expires_at)>julianday('now')`, codeHash).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *Store) ConsumeInviteAndCreateUser(ctx context.Context, codeHash []byte, user User, revisions ...int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireRegistrationTx(ctx, tx, revisions...); err != nil {
		return err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM invites WHERE code_hash=? AND used_by IS NULL AND revoked_at IS NULL AND julianday(expires_at)>julianday('now')`, codeHash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,display_name,role,created_at) VALUES(?,?,?,?,?,?)`, user.ID, user.Email, user.PasswordHash, user.DisplayName, "user", now()); err != nil {
		// Do not mislabel arbitrary constraints or I/O errors as a duplicate email.
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed: users.email") {
			return ErrEmailExists
		}
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE invites SET used_by=?,used_at=? WHERE id=? AND used_by IS NULL AND revoked_at IS NULL AND julianday(expires_at)>julianday('now')`, user.ID, now(), id)
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
	if err := auditTx(ctx, tx, AuditEvent{UserID: user.ID, Action: "invite.used", TargetType: "invite", TargetID: id}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeInvite(ctx context.Context, actor, id string, event AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireAdminTx(ctx, tx, actor); err != nil {
		return err
	}
	var used, revoked sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT used_by,revoked_at FROM invites WHERE id=?`, id).Scan(&used, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if used.Valid {
		return ErrConflict
	}
	if revoked.Valid {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invites SET revoked_at=?,revoked_by=? WHERE id=? AND used_by IS NULL`, now(), actor, id); err != nil {
		return err
	}
	if err := auditTx(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}
