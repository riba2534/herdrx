package store

import "context"

// RelayCredentials returns only credentials still referenced by an enabled
// owner's host or in-flight enrollment. Deleted hosts and failed enrollments
// cannot keep admitting new relay connections through orphaned credentials.
func (s *Store) RelayCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.owner_id,c.kind,c.ciphertext FROM credentials c JOIN users u ON u.id=c.owner_id
WHERE u.disabled=0 AND (EXISTS (SELECT 1 FROM hosts h WHERE h.credential_id=c.id AND h.owner_id=c.owner_id AND h.transport='tailcat')
OR EXISTS (SELECT 1 FROM enrollment_tasks e WHERE e.credential_id=c.id AND e.owner_id=c.owner_id AND e.phase NOT IN ('active','failed')))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.OwnerID, &c.Kind, &c.Ciphertext); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
