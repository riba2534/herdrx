package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/types/key"
	tslogger "tailscale.com/types/logger"
)

type createEnrollmentRequest struct {
	ConnectionString string `json:"connection_string"`
	Name             string `json:"name,omitempty"`
	SessionName      string `json:"session_name,omitempty"`
}

type EnrollmentTaskStatus string

const (
	EnrollmentStatusVerifying  EnrollmentTaskStatus = "verifying"
	EnrollmentStatusConnecting EnrollmentTaskStatus = "connecting"
	EnrollmentStatusPreparing  EnrollmentTaskStatus = "preparing"
	EnrollmentStatusCommitting EnrollmentTaskStatus = "committing"
	EnrollmentStatusActive     EnrollmentTaskStatus = "active"
	EnrollmentStatusFailed     EnrollmentTaskStatus = "failed"
)

type enrollmentPublicView struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	AgentID   string `json:"agent_id"`
	Status    string `json:"status"`
	HostID    string `json:"host_id,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func publicEnrollment(task store.EnrollmentTask) enrollmentPublicView {
	phase := task.Phase
	switch phase {
	case "pending":
		phase = string(EnrollmentStatusVerifying)
	case "prepared":
		phase = string(EnrollmentStatusCommitting)
	}
	return enrollmentPublicView{
		ID:        task.ID,
		TaskID:    task.ID,
		AgentID:   task.AgentID,
		Status:    phase,
		HostID:    task.HostID,
		Error:     task.Error,
		CreatedAt: task.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: task.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (a *API) createTailcatEnrollment(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, 16*1024)
	var input createEnrollmentRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_input", "invalid json body or payload exceeds 16KiB")
		return
	}
	user := userFromContext(request.Context())
	parsed, err := tunnel.ParseConnectionString(input.ConnectionString)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_connection_string", err.Error())
		return
	}
	dialAddr, err := tunnel.DefaultSSRFValidator.ValidateDialAddr(parsed.Payload.TailcatAddr)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "ssrf_violation", err.Error())
		return
	}
	rootFP := ssh.FingerprintSHA256(parsed.SSHHostKey)

	if existing, err := a.store.HostByRootFingerprint(request.Context(), user.ID, rootFP); err == nil {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "active", "host_id": existing.ID, "agent_id": parsed.Payload.AgentID})
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(writer, http.StatusInternalServerError, "database_error", "could not read hosts")
		return
	}

	if inflight, err := a.store.InFlightEnrollment(request.Context(), user.ID, parsed.Payload.AgentID); err == nil {
		if inflight.RootSSHFingerprint != rootFP {
			writeError(writer, http.StatusConflict, "enrollment_conflict", "a different host identity is already being enrolled for this agent")
			return
		}
		a.ensureEnrollmentWorker(user.ID, inflight.ID)
		writeJSON(writer, http.StatusAccepted, publicEnrollment(inflight))
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(writer, http.StatusInternalServerError, "database_error", "could not read enrollment tasks")
		return
	}

	formalNodeKey := key.NewNode()
	formalSSHPrivPEM, _, err := secure.GenerateSSHKey("herdrx-controller")
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "key_generation_failed", err.Error())
		return
	}
	taskID := "task_" + mustToken(12)
	credID := "cred_" + mustToken(12)
	reqID := "req_" + mustToken(12)
	ctrlID := "ctrl_" + mustToken(12)
	claim := mustToken(18)
	secret, err := json.Marshal(hostruntime.TailcatCredential{
		RelayProbeNode:    parsed.Payload.RelayProbeNode,
		NodePrivate:       formalNodeKey,
		SSHPrivate:        string(formalSSHPrivPEM),
		BootstrapAddr:     parsed.Payload.TailcatAddr,
		BootstrapDialAddr: dialAddr,
		BootstrapClient:   parsed.ClientNode,
		PairSecret:        parsed.Payload.PairSecret,
		EnrollmentID:      parsed.Payload.EnrollmentID,
		SSHHostKey:        strings.TrimSpace(string(ssh.MarshalAuthorizedKey(parsed.SSHHostKey))),
		RequestID:         reqID,
		ControllerID:      ctrlID,
		AgentID:           parsed.Payload.AgentID,
	})
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "secret_failed", err.Error())
		return
	}
	ciphertext, err := hostruntime.SealCredential(a.vault, user.ID, credID, "tailcat", secret)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "secret_failed", err.Error())
		return
	}
	task := store.EnrollmentTask{
		ID:                 taskID,
		OwnerID:            user.ID,
		AgentID:            parsed.Payload.AgentID,
		RootSSHFingerprint: rootFP,
		RequestID:          reqID,
		ControllerID:       ctrlID,
		CredentialID:       credID,
		Phase:              "pending",
		EnrollmentID:       parsed.Payload.EnrollmentID,
		ClaimToken:         claim,
		Name:               strings.TrimSpace(input.Name),
		SessionName:        strings.TrimSpace(input.SessionName),
	}
	if err := a.store.CreateEnrollmentStart(request.Context(), task, store.Credential{ID: credID, OwnerID: user.ID, Kind: "tailcat", Ciphertext: ciphertext}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			if inflight, lerr := a.store.InFlightEnrollment(request.Context(), user.ID, parsed.Payload.AgentID); lerr == nil {
				a.ensureEnrollmentWorker(user.ID, inflight.ID)
				writeJSON(writer, http.StatusAccepted, publicEnrollment(inflight))
				return
			}
		}
		writeError(writer, http.StatusInternalServerError, "database_error", "could not persist enrollment")
		return
	}
	a.ensureEnrollmentWorker(user.ID, taskID)
	writeJSON(writer, http.StatusAccepted, publicEnrollment(task))
}

func (a *API) listTailcatEnrollments(writer http.ResponseWriter, request *http.Request) {
	user := userFromContext(request.Context())
	tasks, err := a.store.ListInFlightEnrollments(request.Context(), user.ID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "could not list enrollment tasks")
		return
	}
	views := make([]enrollmentPublicView, 0, len(tasks))
	for _, task := range tasks {
		a.ensureEnrollmentWorker(user.ID, task.ID)
		views = append(views, publicEnrollment(task))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"tasks": views})
}

func (a *API) getTailcatEnrollment(writer http.ResponseWriter, request *http.Request) {
	user := userFromContext(request.Context())
	taskID := chi.URLParam(request, "id")
	task, err := a.store.EnrollmentByID(request.Context(), user.ID, taskID)
	if err != nil {
		writeError(writer, http.StatusNotFound, "task_not_found", "enrollment task not found")
		return
	}
	if task.Phase != "active" && task.Phase != "failed" {
		a.ensureEnrollmentWorker(user.ID, task.ID)
	}
	writeJSON(writer, http.StatusOK, publicEnrollment(task))
}

func (a *API) ensureEnrollmentWorker(ownerID, taskID string) {
	key := ownerID + "/" + taskID
	a.enrollmentMu.Lock()
	if a.enrollmentRunning[key] {
		a.enrollmentMu.Unlock()
		return
	}
	a.enrollmentRunning[key] = true
	a.enrollmentMu.Unlock()
	go func() {
		defer func() {
			a.enrollmentMu.Lock()
			delete(a.enrollmentRunning, key)
			a.enrollmentMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		a.executeEnrollment(ctx, ownerID, taskID)
	}()
}

func (a *API) executeEnrollment(ctx context.Context, ownerID, taskID string) {
	task, err := a.store.EnrollmentByID(ctx, ownerID, taskID)
	if err != nil || task.Phase == "active" || task.Phase == "failed" {
		return
	}
	cred, err := a.store.CredentialByID(ctx, ownerID, task.CredentialID)
	if err != nil {
		_ = a.store.FailEnrollment(ctx, ownerID, taskID, task.ClaimToken, "credential missing")
		return
	}
	plain, err := hostruntime.OpenCredential(a.vault, ownerID, cred.ID, cred.Kind, cred.Ciphertext)
	if err != nil {
		_ = a.store.FailEnrollment(ctx, ownerID, taskID, task.ClaimToken, "could not decrypt enrollment secret")
		return
	}
	var secret hostruntime.TailcatCredential
	if err := json.Unmarshal(plain, &secret); err != nil {
		_ = a.store.FailEnrollment(ctx, ownerID, taskID, task.ClaimToken, "invalid enrollment secret")
		return
	}

	fail := func(msg string) {
		_ = a.store.FailEnrollment(ctx, ownerID, taskID, task.ClaimToken, msg)
	}

	if task.Phase == "pending" || task.Phase == "connecting" || task.Phase == "preparing" || task.BindingID == "" {
		if err := a.prepareEnrollment(ctx, task, &secret); err != nil {
			fail(err.Error())
			return
		}
		task, err = a.store.EnrollmentByID(ctx, ownerID, taskID)
		if err != nil {
			return
		}
	}
	if err := a.commitEnrollment(ctx, task, secret); err != nil {
		// Commit uncertainty: keep prepared/committing materials for retry.
		return
	}
}

func (a *API) prepareEnrollment(ctx context.Context, task store.EnrollmentTask, secret *hostruntime.TailcatCredential) error {
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(secret.SSHHostKey))
	if err != nil {
		return fmt.Errorf("parse ssh host key: %w", err)
	}
	ctrlSigner, err := ssh.ParsePrivateKey([]byte(secret.SSHPrivate))
	if err != nil {
		return fmt.Errorf("parse controller key: %w", err)
	}
	dialAddr := secret.BootstrapDialAddr
	if dialAddr == "" {
		dialAddr, err = tunnel.DefaultSSRFValidator.ValidateDialAddr(secret.BootstrapAddr)
		if err != nil {
			return err
		}
	}
	tempClient := tailcat.NewClient(tailcat.Addr(dialAddr))
	tempClient.Key = secret.BootstrapClient
	tempClient.Logf = tslogger.Discard
	defer tempClient.Close()

	netConn, err := tempClient.DialTCPPort(ctx, 22)
	if err != nil {
		return fmt.Errorf("dial temporary endpoint: %w", err)
	}
	defer netConn.Close()
	tempSSHConn, chans, reqs, err := ssh.NewClientConn(netConn, "", &ssh.ClientConfig{
		User:            "pairing",
		Auth:            []ssh.AuthMethod{ssh.Password(secret.PairSecret)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("ssh handshake on temp endpoint: %w", err)
	}
	tempSSH := ssh.NewClient(tempSSHConn, chans, reqs)
	defer tempSSH.Close()

	sess, err := tempSSH.NewSession()
	if err != nil {
		return fmt.Errorf("open ssh session: %w", err)
	}
	hexSSHPub := hex.EncodeToString(ssh.MarshalAuthorizedKey(ctrlSigner.PublicKey()))
	var prepOut, prepErr bytes.Buffer
	sess.Stdout, sess.Stderr = &prepOut, &prepErr
	prepareCmd := fmt.Sprintf("pairing-prepare %s %s %s %s", task.RequestID, task.ControllerID, secret.NodePrivate.Public().String(), hexSSHPub)
	if err := sess.Run(prepareCmd); err != nil {
		_ = sess.Close()
		return fmt.Errorf("execute prepare: %v (stderr: %s)", err, prepErr.String())
	}
	_ = sess.Close()

	var bindingID, challenge, formalAddr string
	var preparedExp int64
	for _, field := range strings.Fields(prepOut.String()) {
		switch {
		case strings.HasPrefix(field, "binding_id="):
			bindingID = strings.TrimPrefix(field, "binding_id=")
		case strings.HasPrefix(field, "challenge="):
			challenge = strings.TrimPrefix(field, "challenge=")
		case strings.HasPrefix(field, "formal_addr="):
			formalAddr = strings.TrimPrefix(field, "formal_addr=")
		case strings.HasPrefix(field, "prepared_expires_at="):
			_, _ = fmt.Sscanf(strings.TrimPrefix(field, "prepared_expires_at="), "%d", &preparedExp)
		}
	}
	if bindingID == "" || challenge == "" || formalAddr == "" {
		return fmt.Errorf("incomplete prepare response")
	}
	dialFormal, err := tunnel.DefaultSSRFValidator.ValidateDialAddr(formalAddr)
	if err != nil {
		return fmt.Errorf("formal address ssrf check: %w", err)
	}
	secret.BindingID = bindingID
	secret.FormalTailcatAddr = formalAddr
	secret.RawFormalAddr = formalAddr
	secret.DialFormalAddr = dialFormal
	if preparedExp > 0 {
		secret.PreparedExpiresAt = preparedExp
	}
	encoded, err := json.Marshal(secret)
	if err != nil {
		return err
	}
	cipher, err := hostruntime.SealCredential(a.vault, task.OwnerID, task.CredentialID, "tailcat", encoded)
	if err != nil {
		return err
	}
	exp := time.Unix(preparedExp, 0)
	if preparedExp == 0 {
		exp = time.Now().Add(10 * time.Minute)
	}
	return a.store.SavePrepared(ctx, task.OwnerID, task.ID, task.ClaimToken, bindingID, challenge, nil, exp, cipher)
}

func (a *API) commitEnrollment(ctx context.Context, task store.EnrollmentTask, secret hostruntime.TailcatCredential) error {
	if err := a.store.MarkCommitting(ctx, task.OwnerID, task.ID, task.ClaimToken); err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(secret.SSHHostKey))
	if err != nil {
		return err
	}
	ctrlSigner, err := ssh.ParsePrivateKey([]byte(secret.SSHPrivate))
	if err != nil {
		return err
	}
	rawAddr := secret.RawFormalAddr
	if rawAddr == "" {
		rawAddr = secret.FormalTailcatAddr
	}
	dialAddr := secret.DialFormalAddr
	if dialAddr == "" {
		dialAddr, err = tunnel.DefaultSSRFValidator.ValidateDialAddr(rawAddr)
		if err != nil {
			return err
		}
	}
	formalClient := tailcat.NewClient(tailcat.Addr(dialAddr))
	formalClient.Key = secret.NodePrivate
	formalClient.Logf = tslogger.Discard
	defer formalClient.Close()
	fNetConn, err := formalClient.DialTCPPort(ctx, 22)
	if err != nil {
		return fmt.Errorf("dial formal endpoint: %w", err)
	}
	defer fNetConn.Close()
	fSSHConn, fChans, fReqs, err := ssh.NewClientConn(fNetConn, "", &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(ctrlSigner)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("ssh handshake on formal endpoint: %w", err)
	}
	formalSSH := ssh.NewClient(fSSHConn, fChans, fReqs)
	defer formalSSH.Close()

	statusSess, err := formalSSH.NewSession()
	if err != nil {
		return err
	}
	var statusOut bytes.Buffer
	statusSess.Stdout = &statusOut
	_ = statusSess.Run("pairing-status")
	_ = statusSess.Close()
	statusLine := statusOut.String()
	alreadyActive := strings.Contains(statusLine, "status=active") && (task.BindingID == "" || strings.Contains(statusLine, task.BindingID))
	if !alreadyActive {
		ctrlSSHPub := ctrlSigner.PublicKey()
		sshFP := ssh.FingerprintSHA256(ctrlSSHPub)
		msg := tunnel.CommitContextMessage(task.Challenge, task.BindingID, task.ControllerID, secret.AgentID, secret.EnrollmentID, secret.NodePrivate.Public().String(), sshFP, rawAddr)
		digest := sha256.Sum256(msg)
		block, _ := pem.Decode([]byte(secret.SSHPrivate))
		rawPriv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return err
		}
		sigProof := ed25519.Sign(rawPriv.(ed25519.PrivateKey), digest[:])
		commitSess, err := formalSSH.NewSession()
		if err != nil {
			return err
		}
		var commitOut, commitErr bytes.Buffer
		commitSess.Stdout, commitSess.Stderr = &commitOut, &commitErr
		if err := commitSess.Run(fmt.Sprintf("pairing-commit %s %s", task.BindingID, hex.EncodeToString(sigProof))); err != nil {
			_ = commitSess.Close()
			return fmt.Errorf("execute commit: %v (stderr: %s)", err, commitErr.String())
		}
		_ = commitSess.Close()
		if !strings.Contains(commitOut.String(), "status=active") {
			return fmt.Errorf("commit rejected: %s", commitOut.String())
		}
	}

	encoded, err := json.Marshal(secret)
	if err != nil {
		return err
	}
	cipher, err := hostruntime.SealCredential(a.vault, task.OwnerID, task.CredentialID, "tailcat", encoded)
	if err != nil {
		return err
	}
	host := store.Host{
		ID:                 "hst_" + mustToken(12),
		OwnerID:            task.OwnerID,
		Name:               firstNonEmpty(task.Name, "Tailcat "+task.AgentID),
		Transport:          "tailcat",
		Hostname:           task.AgentID,
		Username:           "herdrx",
		Port:               22,
		SessionName:        task.SessionName,
		AuthMethod:         "tailcat",
		CredentialID:       task.CredentialID,
		HostKey:            secret.SSHHostKey,
		RootSSHFingerprint: task.RootSSHFingerprint,
	}
	if err := a.store.FinalizeActive(ctx, task.OwnerID, task.ID, task.ClaimToken, host, cipher); err != nil {
		return err
	}
	return nil
}

func mustToken(n int) string {
	t, err := secure.Token(n)
	if err != nil {
		return fmt.Sprintf("x%d", time.Now().UnixNano())
	}
	return t
}
