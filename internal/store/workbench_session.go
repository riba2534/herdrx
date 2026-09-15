package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type WorkbenchSession struct {
	LoginSessionID string    `json:"-"`
	WriterID       string    `json:"-"`
	Sequence       int64     `json:"-"`
	UserID         string    `json:"-"`
	HostID         string    `json:"host_id"`
	WorkspaceID    string    `json:"workspace_id,omitempty"`
	TabID          string    `json:"tab_id,omitempty"`
	PaneID         string    `json:"pane_id,omitempty"`
	DeviceID       string    `json:"device_id,omitempty"`
	ClientClass    string    `json:"client_class,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

var ErrStaleWorkbenchSession = errors.New("stale workbench position")

func (s *Store) WorkbenchSession(ctx context.Context, userID string) (WorkbenchSession, error) {
	session, err := scanWorkbenchSession(s.db.QueryRowContext(ctx, `SELECT user_id,host_id,workspace_id,tab_id,pane_id,device_id,client_class,updated_at FROM user_workbench_sessions WHERE user_id=?`, userID))
	if err != nil {
		return WorkbenchSession{}, err
	}
	if _, err = s.HostByID(ctx, userID, session.HostID); errors.Is(err, ErrNotFound) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM user_workbench_sessions WHERE user_id=? AND host_id=? AND updated_at=?`, userID, session.HostID, session.UpdatedAt.UTC().Format(time.RFC3339Nano))
		return WorkbenchSession{}, ErrNotFound
	}
	if err != nil {
		return WorkbenchSession{}, err
	}
	return session, nil
}

func (s *Store) PutWorkbenchSession(ctx context.Context, session WorkbenchSession) (WorkbenchSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkbenchSession{}, err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM hosts WHERE id=? AND owner_id=? AND `+allowedHost, session.HostID, session.UserID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorkbenchSession{}, ErrNotFound
		}
		return WorkbenchSession{}, err
	}
	if session.WriterID != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_workbench_writers WHERE session_id IN
(SELECT id FROM sessions WHERE user_id=? AND julianday(expires_at)<=julianday('now'))`, session.UserID); err != nil {
			return WorkbenchSession{}, err
		}
		// The login session comes from authentication, not the request body. Keep
		// each page's watermark even when another page/device writes in between.
		result, err := tx.ExecContext(ctx, `INSERT INTO user_workbench_writers(session_id,writer_id,sequence)
SELECT ?,?,? WHERE EXISTS (SELECT 1 FROM sessions WHERE id=? AND user_id=? AND julianday(expires_at)>julianday('now'))
ON CONFLICT(session_id,writer_id) DO UPDATE SET sequence=excluded.sequence
WHERE user_workbench_writers.sequence < excluded.sequence`, session.LoginSessionID, session.WriterID, session.Sequence, session.LoginSessionID, session.UserID)
		if err != nil {
			return WorkbenchSession{}, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return WorkbenchSession{}, err
		}
		if count == 0 {
			return WorkbenchSession{}, ErrStaleWorkbenchSession
		}
	}
	stamp := now()
	_, err = tx.ExecContext(ctx, `INSERT INTO user_workbench_sessions(user_id,host_id,workspace_id,tab_id,pane_id,device_id,client_class,updated_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(user_id) DO UPDATE SET
  host_id=excluded.host_id,
  workspace_id=excluded.workspace_id,
  tab_id=excluded.tab_id,
  pane_id=excluded.pane_id,
  device_id=excluded.device_id,
  client_class=excluded.client_class,
  updated_at=excluded.updated_at`,
		session.UserID, session.HostID, session.WorkspaceID, session.TabID, session.PaneID, session.DeviceID, session.ClientClass, stamp)
	if err != nil {
		return WorkbenchSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkbenchSession{}, err
	}
	session.UpdatedAt = parseTime(stamp)
	return session, nil
}

func scanWorkbenchSession(row interface{ Scan(...any) error }) (WorkbenchSession, error) {
	var session WorkbenchSession
	var updated string
	err := row.Scan(&session.UserID, &session.HostID, &session.WorkspaceID, &session.TabID, &session.PaneID, &session.DeviceID, &session.ClientClass, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkbenchSession{}, ErrNotFound
	}
	if err != nil {
		return WorkbenchSession{}, err
	}
	session.UpdatedAt = parseTime(updated)
	return session, nil
}
