package store

import (
	"context"
	"encoding/json"
	"time"
)

type Page struct {
	Limit  int
	Offset int
}

func (p Page) Bounded() Page {
	if p.Limit < 1 {
		p.Limit = 50
	}
	if p.Limit > 100 {
		p.Limit = 100
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	if p.Offset > 1000000 {
		p.Offset = 1000000
	}
	return p
}

type AdminUser struct {
	User
	ActiveSessions int `json:"active_sessions"`
}

type UserFilter struct{ Query, Role, Status string }

func (s *Store) ListUsers(ctx context.Context, page Page, filters ...UserFilter) ([]AdminUser, error) {
	page = page.Bounded()
	query := `SELECT u.id,u.email,u.display_name,u.role,u.disabled,u.created_at,
 (SELECT COUNT(*) FROM sessions s WHERE s.user_id=u.id AND julianday(s.expires_at)>julianday('now') AND u.disabled=0)
 FROM users u WHERE 1=1`
	args := []any{}
	if len(filters) > 0 {
		f := filters[0]
		if f.Query != "" {
			query += ` AND (instr(lower(u.email),lower(?))>0 OR instr(lower(u.display_name),lower(?))>0)`
			args = append(args, f.Query, f.Query)
		}
		if f.Role != "" {
			query += ` AND u.role=?`
			args = append(args, f.Role)
		}
		if f.Status != "" {
			query += ` AND u.disabled=?`
			args = append(args, f.Status == "disabled")
		}
	}
	query += ` ORDER BY u.created_at DESC,u.id DESC LIMIT ? OFFSET ?`
	args = append(args, page.Limit+1, page.Offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AdminUser, 0)
	for rows.Next() {
		var item AdminUser
		var created string
		if err := rows.Scan(&item.ID, &item.Email, &item.DisplayName, &item.Role, &item.Disabled, &created, &item.ActiveSessions); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

type LoginSession struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	UserAgent string    `json:"user_agent"`
	RemoteIP  string    `json:"remote_ip"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Store) ListSessions(ctx context.Context, userID string, page Page) ([]LoginSession, error) {
	page = page.Bounded()
	rows, err := s.db.QueryContext(ctx, `SELECT id,user_id,user_agent,remote_ip,created_at,expires_at FROM sessions WHERE user_id=? AND julianday(expires_at)>julianday('now') ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, userID, page.Limit+1, page.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]LoginSession, 0)
	for rows.Next() {
		var item LoginSession
		var created, expires string
		if err := rows.Scan(&item.ID, &item.UserID, &item.UserAgent, &item.RemoteIP, &created, &expires); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		item.ExpiresAt = parseTime(expires)
		items = append(items, item)
	}
	return items, rows.Err()
}

type AuditEntry struct {
	ID         int64           `json:"id"`
	UserID     string          `json:"user_id"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	RemoteIP   string          `json:"remote_ip"`
	Details    json.RawMessage `json:"details"`
	CreatedAt  time.Time       `json:"created_at"`
}
type AuditFilter struct {
	Page
	UserID, Action string
	Since, Until   time.Time
}

func (s *Store) ListAudit(ctx context.Context, filter AuditFilter) ([]AuditEntry, error) {
	p := filter.Page.Bounded()
	query := `SELECT id,COALESCE(user_id,''),action,target_type,target_id,remote_ip,details,created_at FROM audit_log WHERE 1=1`
	args := make([]any, 0)
	if filter.UserID != "" {
		query += " AND user_id=?"
		args = append(args, filter.UserID)
	}
	if filter.Action != "" {
		query += " AND action=?"
		args = append(args, filter.Action)
	}
	if !filter.Since.IsZero() {
		query += " AND julianday(created_at)>=julianday(?)"
		args = append(args, filter.Since.UTC().Format(timeFormat))
	}
	if !filter.Until.IsZero() {
		query += " AND julianday(created_at)<=julianday(?)"
		args = append(args, filter.Until.UTC().Format(timeFormat))
	}
	query += " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, p.Limit+1, p.Offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AuditEntry, 0)
	for rows.Next() {
		var item AuditEntry
		var created, details string
		if err := rows.Scan(&item.ID, &item.UserID, &item.Action, &item.TargetType, &item.TargetID, &item.RemoteIP, &details, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		if json.Valid([]byte(details)) {
			item.Details = json.RawMessage(details)
		} else {
			item.Details = json.RawMessage(`{}`)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
