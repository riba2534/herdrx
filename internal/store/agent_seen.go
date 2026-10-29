package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AgentSeen records the newest Herdr state_change_seq a user has looked at in
// the Web client for one pane. It is a per-user read marker only: it never
// changes the Herdr session, and a missing row simply means "not seen here".
type AgentSeen struct {
	HostID         string    `json:"host_id"`
	PaneID         string    `json:"pane_id"`
	StateChangeSeq int64     `json:"state_change_seq"`
	SeenAt         time.Time `json:"seen_at"`
}

// MarkAgentSeen records the state_change_seq the user has looked at for a pane.
// It overwrites in both directions: Herdr restarts its counter at 0 and reuses
// pane IDs, so a lower value is the current run, not a stale one. A delayed
// request from an older page can at worst show a round as unseen again.
// The host must belong to the user.
func (s *Store) MarkAgentSeen(ctx context.Context, userID, hostID, paneID string, stateChangeSeq int64) error {
	if stateChangeSeq < 0 {
		return errors.New("negative state_change_seq")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM hosts WHERE id=? AND owner_id=? AND `+allowedHost, hostID, userID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_seen(user_id,host_id,pane_id,state_change_seq,seen_at) VALUES(?,?,?,?,?)
ON CONFLICT(user_id,host_id,pane_id) DO UPDATE SET state_change_seq=excluded.state_change_seq,seen_at=excluded.seen_at`, userID, hostID, paneID, stateChangeSeq, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// AgentSeenForUser returns every read marker of the user keyed by
// host_id + "\x00" + pane_id (see AgentSeenKey).
func (s *Store) AgentSeenForUser(ctx context.Context, userID string) (map[string]AgentSeen, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.host_id,a.pane_id,a.state_change_seq,a.seen_at FROM agent_seen a
JOIN hosts h ON h.id=a.host_id AND h.owner_id=a.user_id WHERE a.user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]AgentSeen)
	for rows.Next() {
		var item AgentSeen
		var at string
		if err := rows.Scan(&item.HostID, &item.PaneID, &item.StateChangeSeq, &at); err != nil {
			return nil, err
		}
		item.SeenAt = parseTime(at)
		seen[AgentSeenKey(item.HostID, item.PaneID)] = item
	}
	return seen, rows.Err()
}

// PruneAgentSeen drops markers of panes that no longer exist on a host. live
// holds the current pane IDs; markers for other panes of the host are removed.
func (s *Store) PruneAgentSeen(ctx context.Context, userID, hostID string, live map[string]bool) error {
	rows, err := s.db.QueryContext(ctx, `SELECT pane_id FROM agent_seen WHERE user_id=? AND host_id=?`, userID, hostID)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var paneID string
		if err := rows.Scan(&paneID); err != nil {
			rows.Close()
			return err
		}
		if !live[paneID] {
			stale = append(stale, paneID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, paneID := range stale {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM agent_seen WHERE user_id=? AND host_id=? AND pane_id=?`, userID, hostID, paneID); err != nil {
			return err
		}
	}
	return nil
}

func AgentSeenKey(hostID, paneID string) string { return hostID + "\x00" + paneID }
