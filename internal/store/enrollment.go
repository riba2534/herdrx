package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type EnrollmentTask struct {
	ID                   string
	OwnerID              string
	AgentID              string
	RootSSHFingerprint   string
	RequestID            string
	ControllerID         string
	CredentialID         string
	Phase                string
	HostID               string
	EnrollmentID         string
	BindingID            string
	Challenge            string
	FormalAddrCiphertext []byte
	PreparedExpiresAt    time.Time
	ClaimToken           string
	LeaseUntil           time.Time
	Error                string
	Name                 string
	SessionName          string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (s *Store) CreateEnrollmentStart(ctx context.Context, task EnrollmentTask, credential Credential) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO credentials(id,owner_id,kind,ciphertext,created_at) VALUES(?,?,?,?,?)`,
		credential.ID, credential.OwnerID, credential.Kind, credential.Ciphertext, now()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO enrollment_tasks(id,owner_id,agent_id,root_ssh_fp,request_id,controller_id,credential_id,phase,host_id,enrollment_id,binding_id,challenge,formal_addr_ciphertext,prepared_expires_at,claim_token,lease_until,error,name,session_name,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		task.ID, task.OwnerID, task.AgentID, task.RootSSHFingerprint, task.RequestID, task.ControllerID, task.CredentialID, task.Phase, nullable(task.HostID), task.EnrollmentID, task.BindingID, task.Challenge, task.FormalAddrCiphertext, nullableTime(task.PreparedExpiresAt), task.ClaimToken, nullableTime(task.LeaseUntil), task.Error, task.Name, task.SessionName, now(), now()); err != nil {
		if isUnique(err) {
			return ErrConflict
		}
		return err
	}
	return tx.Commit()
}

func (s *Store) EnrollmentByID(ctx context.Context, ownerID, id string) (EnrollmentTask, error) {
	task, err := scanEnrollment(s.db.QueryRowContext(ctx, enrollmentSelect+` WHERE id=? AND owner_id=?`, id, ownerID))
	if errors.Is(err, sql.ErrNoRows) {
		return EnrollmentTask{}, ErrNotFound
	}
	return task, err
}

func (s *Store) InFlightEnrollment(ctx context.Context, ownerID, agentID string) (EnrollmentTask, error) {
	task, err := scanEnrollment(s.db.QueryRowContext(ctx, enrollmentSelect+` WHERE owner_id=? AND agent_id=? AND phase NOT IN ('active','failed') ORDER BY created_at DESC LIMIT 1`, ownerID, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return EnrollmentTask{}, ErrNotFound
	}
	return task, err
}

func (s *Store) ListInFlightEnrollments(ctx context.Context, ownerID string) ([]EnrollmentTask, error) {
	rows, err := s.db.QueryContext(ctx, enrollmentSelect+` WHERE owner_id=? AND phase NOT IN ('active','failed') ORDER BY created_at`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]EnrollmentTask, 0)
	for rows.Next() {
		task, err := scanEnrollment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func (s *Store) SavePrepared(ctx context.Context, ownerID, id, claim, bindingID, challenge string, addrCipher []byte, expires time.Time, credCipher []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE enrollment_tasks SET phase='prepared', binding_id=?, challenge=?, formal_addr_ciphertext=?, prepared_expires_at=?, error='', updated_at=?
WHERE id=? AND owner_id=? AND claim_token=? AND phase IN ('pending','connecting','preparing','prepared')`,
		bindingID, challenge, addrCipher, expires.UTC().Format(timeFormat), now(), id, ownerID, claim)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE credentials SET ciphertext=? WHERE id=(SELECT credential_id FROM enrollment_tasks WHERE id=? AND owner_id=?) AND owner_id=?`,
		credCipher, id, ownerID, ownerID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkCommitting(ctx context.Context, ownerID, id, claim string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE enrollment_tasks SET phase='committing', error='', updated_at=?
WHERE id=? AND owner_id=? AND claim_token=? AND phase IN ('prepared','committing')`, now(), id, ownerID, claim)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) FinalizeActive(ctx context.Context, ownerID, id, claim string, host Host, credCipher []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE enrollment_tasks SET phase='active', host_id=?, error='', updated_at=?
WHERE id=? AND owner_id=? AND claim_token=? AND phase IN ('prepared','committing')`,
		host.ID, now(), id, ownerID, claim)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	stamp := now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO hosts(id,owner_id,name,transport,hostname,port,username,session_name,auth_method,credential_id,host_key,pending_host_key,tailcat_addr,root_ssh_fp,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		host.ID, host.OwnerID, host.Name, host.Transport, host.Hostname, host.Port, host.Username, host.SessionName, host.AuthMethod, nullable(host.CredentialID), host.HostKey, host.PendingHostKey, host.TailcatAddr, host.RootSSHFingerprint, stamp, stamp); err != nil {
		if isUnique(err) {
			return ErrConflict
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE credentials SET ciphertext=? WHERE id=? AND owner_id=?`, credCipher, host.CredentialID, ownerID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailEnrollment(ctx context.Context, ownerID, id, claim, errMsg string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE enrollment_tasks SET phase='failed', error=?, updated_at=?
WHERE id=? AND owner_id=? AND claim_token=? AND phase NOT IN ('active','committing')`,
		errMsg, now(), id, ownerID, claim)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) HostByRootFingerprint(ctx context.Context, ownerID, fp string) (Host, error) {
	host, err := scanHost(s.db.QueryRowContext(ctx, `SELECT `+hostColumns+` FROM hosts WHERE owner_id=? AND root_ssh_fp=? AND transport='tailcat'`, ownerID, fp))
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, ErrNotFound
	}
	return host, err
}

const enrollmentSelect = `SELECT id,owner_id,agent_id,root_ssh_fp,request_id,controller_id,credential_id,phase,COALESCE(host_id,''),enrollment_id,binding_id,challenge,formal_addr_ciphertext,prepared_expires_at,claim_token,lease_until,error,name,session_name,created_at,updated_at FROM enrollment_tasks`

func scanEnrollment(row interface{ Scan(...any) error }) (EnrollmentTask, error) {
	var task EnrollmentTask
	var prepared, lease, created, updated sql.NullString
	err := row.Scan(&task.ID, &task.OwnerID, &task.AgentID, &task.RootSSHFingerprint, &task.RequestID, &task.ControllerID, &task.CredentialID, &task.Phase, &task.HostID, &task.EnrollmentID, &task.BindingID, &task.Challenge, &task.FormalAddrCiphertext, &prepared, &task.ClaimToken, &lease, &task.Error, &task.Name, &task.SessionName, &created, &updated)
	if err != nil {
		return EnrollmentTask{}, err
	}
	if prepared.Valid {
		task.PreparedExpiresAt = parseTime(prepared.String)
	}
	if lease.Valid {
		task.LeaseUntil = parseTime(lease.String)
	}
	task.CreatedAt = parseTime(created.String)
	task.UpdatedAt = parseTime(updated.String)
	return task, nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(timeFormat)
}

func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "constraint")
}
