package store

import (
	"context"
	"database/sql"
	"errors"
)

type PushSubscription struct {
	ID       string `json:"id"`
	UserID   string `json:"-"`
	Endpoint string `json:"endpoint"`
	P256DH   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

func (s *Store) UpsertPushSubscription(ctx context.Context, subscription PushSubscription) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO push_subscriptions(id,user_id,endpoint,p256dh,auth,created_at) SELECT ?,id,?,?,?,? FROM users WHERE id=? AND disabled=0
ON CONFLICT(endpoint) DO UPDATE SET id=excluded.id,user_id=excluded.user_id,p256dh=excluded.p256dh,auth=excluded.auth`,
		subscription.ID, subscription.Endpoint, subscription.P256DH, subscription.Auth, now(), subscription.UserID)
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
	return nil
}

func (s *Store) PushSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.user_id,p.endpoint,p.p256dh,p.auth FROM push_subscriptions p JOIN users u ON u.id=p.user_id WHERE u.disabled=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	subscriptions := make([]PushSubscription, 0)
	for rows.Next() {
		var subscription PushSubscription
		if err := rows.Scan(&subscription.ID, &subscription.UserID, &subscription.Endpoint, &subscription.P256DH, &subscription.Auth); err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, subscription)
	}
	return subscriptions, rows.Err()
}

func (s *Store) DeletePushSubscription(ctx context.Context, endpoint string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint=?`, endpoint)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) PushSubscriptionByEndpoint(ctx context.Context, userID, endpoint string) (PushSubscription, error) {
	var subscription PushSubscription
	err := s.db.QueryRowContext(ctx, `SELECT id,user_id,endpoint,p256dh,auth FROM push_subscriptions WHERE user_id=? AND endpoint=?`, userID, endpoint).Scan(
		&subscription.ID, &subscription.UserID, &subscription.Endpoint, &subscription.P256DH, &subscription.Auth)
	if errors.Is(err, sql.ErrNoRows) {
		return PushSubscription{}, ErrNotFound
	}
	return subscription, err
}

// A late provider response must not delete a renewed or reassigned subscription.
func (s *Store) DeletePushSubscriptionIfCurrent(ctx context.Context, subscription PushSubscription) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id=? AND user_id=? AND endpoint=?`, subscription.ID, subscription.UserID, subscription.Endpoint)
	return err
}
