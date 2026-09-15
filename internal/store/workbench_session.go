package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type WorkbenchSession struct {
	UserID      string    `json:"-"`
	HostID      string    `json:"host_id"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	TabID       string    `json:"tab_id,omitempty"`
	PaneID      string    `json:"pane_id,omitempty"`
	DeviceID    string    `json:"device_id,omitempty"`
	ClientClass string    `json:"client_class,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *Store) WorkbenchSession(ctx context.Context, userID string) (WorkbenchSession, error) {
	session, err := scanWorkbenchSession(s.db.QueryRowContext(ctx, `SELECT user_id,host_id,workspace_id,tab_id,pane_id,device_id,client_class,updated_at FROM user_workbench_sessions WHERE user_id=?`, userID))
	if err != nil {
		return WorkbenchSession{}, err
	}
	if _, err = s.HostByID(ctx, userID, session.HostID); errors.Is(err, ErrNotFound) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM user_workbench_sessions WHERE user_id=?`, userID)
		return WorkbenchSession{}, ErrNotFound
	}
	if err != nil {
		return WorkbenchSession{}, err
	}
	return session, nil
}

func (s *Store) PutWorkbenchSession(ctx context.Context, session WorkbenchSession) (WorkbenchSession, error) {
	if _, err := s.HostByID(ctx, session.UserID, session.HostID); err != nil {
		return WorkbenchSession{}, err
	}
	stamp := now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_workbench_sessions(user_id,host_id,workspace_id,tab_id,pane_id,device_id,client_class,updated_at)
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
