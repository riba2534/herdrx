package store

import (
	"context"
	"database/sql"
	"time"
)

type InstanceSettings struct {
	Registration string    `json:"registration"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updated_at"`
	UpdatedBy    string    `json:"updated_by,omitempty"`
}

func (s *Store) Settings(ctx context.Context) (InstanceSettings, error) {
	var settings InstanceSettings
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT registration,revision,updated_at,COALESCE(updated_by,'') FROM instance_settings WHERE id=1`).Scan(&settings.Registration, &settings.Revision, &updated, &settings.UpdatedBy)
	settings.UpdatedAt = parseTime(updated)
	return settings, err
}

// Settings and their audit event commit together. Optimistic revisions prevent
// an old admin page from silently overwriting a more recent registration choice.
func (s *Store) SetRegistration(ctx context.Context, actor, mode string, revision int64, event AuditEvent) (InstanceSettings, error) {
	if mode != "closed" && mode != "invite" {
		return InstanceSettings{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InstanceSettings{}, err
	}
	defer tx.Rollback()
	if err := requireAdminTx(ctx, tx, actor); err != nil {
		return InstanceSettings{}, err
	}
	stamp := now()
	result, err := tx.ExecContext(ctx, `UPDATE instance_settings SET registration=?,revision=revision+1,updated_at=?,updated_by=? WHERE id=1 AND revision=?`, mode, stamp, actor, revision)
	if err != nil {
		return InstanceSettings{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return InstanceSettings{}, err
	}
	if count != 1 {
		return InstanceSettings{}, ErrConflict
	}
	if err := auditTx(ctx, tx, event); err != nil {
		return InstanceSettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return InstanceSettings{}, err
	}
	return InstanceSettings{Registration: mode, Revision: revision + 1, UpdatedAt: parseTime(stamp), UpdatedBy: actor}, nil
}

func requireRegistrationTx(ctx context.Context, tx *sql.Tx, revisions ...int64) error {
	var mode string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT registration,revision FROM instance_settings WHERE id=1`).Scan(&mode, &revision); err != nil {
		return err
	}
	if mode != "invite" {
		return ErrRegistrationClosed
	}
	if len(revisions) > 0 && revisions[0] != revision {
		return ErrConflict
	}
	return nil
}
