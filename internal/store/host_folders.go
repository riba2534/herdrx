package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type HostFolder struct {
	ID        string    `json:"id"`
	OwnerID   string    `json:"-"`
	Name      string    `json:"name"`
	ParentID  string    `json:"parent_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) ListHostFolders(ctx context.Context, owner string) ([]HostFolder, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,owner_id,name,COALESCE(parent_id,''),created_at,updated_at FROM host_folders WHERE owner_id=? ORDER BY name COLLATE NOCASE,id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	folders := make([]HostFolder, 0)
	for rows.Next() {
		var f HostFolder
		var created, updated string
		if err = rows.Scan(&f.ID, &f.OwnerID, &f.Name, &f.ParentID, &created, &updated); err != nil {
			return nil, err
		}
		f.CreatedAt, f.UpdatedAt = parseTime(created), parseTime(updated)
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

func folderParent(ctx context.Context, tx *sql.Tx, owner, id string) (string, error) {
	var parent string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(parent_id,'') FROM host_folders WHERE id=? AND owner_id=?`, id, owner).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return parent, err
}

func (s *Store) SaveHostFolder(ctx context.Context, f HostFolder, update bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if update {
		if _, err = folderParent(ctx, tx, f.OwnerID, f.ID); err != nil {
			return err
		}
	}
	seen := map[string]bool{f.ID: true}
	for parent := f.ParentID; parent != ""; {
		if seen[parent] {
			return ErrFolderCycle
		}
		seen[parent] = true
		parent, err = folderParent(ctx, tx, f.OwnerID, parent)
		if err != nil {
			return err
		}
	}
	stamp := now()
	if update {
		_, err = tx.ExecContext(ctx, `UPDATE host_folders SET name=?,parent_id=?,updated_at=? WHERE id=? AND owner_id=?`, f.Name, nullable(f.ParentID), stamp, f.ID, f.OwnerID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO host_folders(id,owner_id,name,parent_id,created_at,updated_at) VALUES(?,?,?,?,?,?)`, f.ID, f.OwnerID, f.Name, nullable(f.ParentID), stamp, stamp)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Deleting a folder moves its contents to its parent, never deletes hosts.
func (s *Store) DeleteHostFolder(ctx context.Context, owner, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	parent, err := folderParent(ctx, tx, owner, id)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hosts SET folder_id=?,updated_at=? WHERE owner_id=? AND folder_id=?`, nullable(parent), now(), owner, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE host_folders SET parent_id=?,updated_at=? WHERE owner_id=? AND parent_id=?`, nullable(parent), now(), owner, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM host_folders WHERE id=? AND owner_id=?`, id, owner); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MoveHost(ctx context.Context, owner, id, folder string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if folder != "" {
		if _, err = folderParent(ctx, tx, owner, folder); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE hosts SET folder_id=?,updated_at=? WHERE id=? AND owner_id=? AND `+allowedHost, nullable(folder), now(), id, owner)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}
