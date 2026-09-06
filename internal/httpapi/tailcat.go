package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/types/key"
	tslogger "tailscale.com/types/logger"
)

type pairAgentRequest struct {
	Version     int    `json:"v"`
	SetupID     string `json:"setup_id"`
	Address     string `json:"tc"`
	Host        string `json:"host"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	AgentVer    string `json:"agent_ver"`
	SSHHostKey  string `json:"ssh_host_key"`
	Token       string `json:"token"`
	ExpiresAt   int64  `json:"exp"`
	Name        string `json:"name,omitempty"`
	SessionName string `json:"session_name,omitempty"`
}

func (a *API) tailcatRoutes(router chi.Router) {
	router.With(a.requireCSRF).Post("/setups", a.createTailcatSetup)
	router.With(a.requireCSRF).Post("/pair", a.pairTailcatAgent)
	router.With(a.requireCSRF).Post("/enrollments", a.createTailcatEnrollment)
	router.Get("/enrollments", a.listTailcatEnrollments)
	router.Get("/enrollments/{id}", a.getTailcatEnrollment)
}

func (a *API) createTailcatSetup(writer http.ResponseWriter, request *http.Request) {
	user := userFromContext(request.Context())
	nodePrivate := key.NewNode()
	sshPrivate, sshPublic, err := secure.GenerateSSHKey("herdrx-tailcat")
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "key_generation_failed", err.Error())
		return
	}
	credentialIDToken, _ := secure.Token(12)
	setupIDToken, _ := secure.Token(12)
	credentialID := "cred_" + credentialIDToken
	setupID := "pair_" + setupIDToken
	secret, _ := json.Marshal(hostruntime.TailcatCredential{NodePrivate: nodePrivate, SSHPrivate: string(sshPrivate)})
	ciphertext, err := hostruntime.SealCredential(a.vault, user.ID, credentialID, "tailcat_setup", secret)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "secret_failed", err.Error())
		return
	}
	if err := a.store.CreateCredential(request.Context(), store.Credential{ID: credentialID, OwnerID: user.ID, Kind: "tailcat_setup", Ciphertext: ciphertext}); err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "could not save setup")
		return
	}
	setup := store.PairingSetup{
		ID: setupID, OwnerID: user.ID, CredentialID: credentialID, NodePublic: nodePrivate.Public().String(),
		SSHPublic: strings.TrimSpace(string(sshPublic)), ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := a.store.CreatePairingSetup(request.Context(), setup); err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "could not create setup")
		return
	}
	command := "herdrx-agent init --allow-node " + shellQuote(setup.NodePublic) + " --ssh-key " + shellQuote(setup.SSHPublic) +
		" --public-url " + shellQuote(a.config.PublicURL) + " --setup-id " + shellQuote(setup.ID)
	if a.config.DERPHost != "" {
		command += " --derp-host " + shellQuote(a.config.DERPHost)
	} else {
		command += " --region-id " + strconv.Itoa(a.config.DERPRegionID)
	}
	a.audit(request, "tailcat_setup.created", "pairing_setup", setup.ID, map[string]any{"expires_at": setup.ExpiresAt})
	writeJSON(writer, http.StatusCreated, map[string]any{"setup_id": setup.ID, "command": command, "expires_at": setup.ExpiresAt})
}

func (a *API) pairTailcatAgent(writer http.ResponseWriter, request *http.Request) {
	var input pairAgentRequest
	if err := decodeJSON(request, &input); err != nil || input.Version != 1 || input.SetupID == "" || input.Address == "" || input.Token == "" || input.SSHHostKey == "" {
		writeError(writer, http.StatusBadRequest, "invalid_pairing", "pairing payload is invalid")
		return
	}
	if time.Now().Unix() > input.ExpiresAt {
		writeError(writer, http.StatusBadRequest, "pairing_expired", "pairing link has expired")
		return
	}
	user := userFromContext(request.Context())
	setup, err := a.store.PairingSetupByID(request.Context(), user.ID, input.SetupID)
	if err != nil || !setup.UsedAt.IsZero() || setup.ExpiresAt.Before(time.Now()) {
		writeError(writer, http.StatusBadRequest, "setup_unavailable", "setup is invalid, expired or already used")
		return
	}
	credential, err := a.store.CredentialByID(request.Context(), user.ID, setup.CredentialID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "credential_missing", "setup credential is unavailable")
		return
	}
	plaintext, err := a.vault.Open(credential.Ciphertext, []byte(user.ID+"\x00"+credential.ID+"\x00"+credential.Kind))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "credential_failed", "setup credential cannot be decrypted")
		return
	}
	var tailcatSecret hostruntime.TailcatCredential
	if json.Unmarshal(plaintext, &tailcatSecret) != nil {
		writeError(writer, http.StatusInternalServerError, "credential_failed", "setup credential is invalid")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 25*time.Second)
	defer cancel()
	if err := confirmTailcatPair(ctx, input, tailcatSecret); err != nil {
		writeError(writer, http.StatusBadGateway, "pairing_failed", err.Error())
		return
	}
	finalCredentialToken, _ := secure.Token(12)
	finalCredentialID := "cred_" + finalCredentialToken
	finalSecret, _ := json.Marshal(tailcatSecret)
	finalCiphertext, err := hostruntime.SealCredential(a.vault, user.ID, finalCredentialID, "tailcat", finalSecret)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "secret_failed", err.Error())
		return
	}
	if err := a.store.CreateCredential(request.Context(), store.Credential{ID: finalCredentialID, OwnerID: user.ID, Kind: "tailcat", Ciphertext: finalCiphertext}); err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "could not save agent credential")
		return
	}
	hostToken, _ := secure.Token(12)
	host := store.Host{
		ID: "hst_" + hostToken, OwnerID: user.ID, Name: firstNonEmpty(input.Name, input.Host, "Tailcat host"),
		Transport: "tailcat", Hostname: input.Host, Username: "herdrx", Port: 22, SessionName: input.SessionName,
		AuthMethod: "tailcat", CredentialID: finalCredentialID, HostKey: strings.TrimSpace(input.SSHHostKey), TailcatAddr: input.Address,
	}
	if err := a.store.CreateHost(request.Context(), host); err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "could not create tailcat host")
		return
	}
	_ = a.store.MarkPairingSetupUsed(request.Context(), user.ID, setup.ID)
	a.audit(request, "tailcat_agent.paired", "host", host.ID, map[string]string{"agent_version": input.AgentVer, "os": input.OS, "arch": input.Arch})
	writeJSON(writer, http.StatusCreated, map[string]any{"host": host})
}

func confirmTailcatPair(ctx context.Context, input pairAgentRequest, secret hostruntime.TailcatCredential) error {
	client := tailcat.NewClient(tailcat.Addr(input.Address))
	client.Key = secret.NodePrivate
	client.Logf = tslogger.Discard
	defer client.Close()
	if _, err := client.Ping(ctx); err != nil {
		return fmt.Errorf("tailcat handshake: %w", err)
	}
	connection, err := client.DialTCPPort(ctx, 22)
	if err != nil {
		return fmt.Errorf("tailcat SSH dial: %w", err)
	}
	defer connection.Close()
	signer, err := ssh.ParsePrivateKey([]byte(secret.SSHPrivate))
	if err != nil {
		return fmt.Errorf("parse pairing SSH key: %w", err)
	}
	wantHostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(input.SSHHostKey))
	if err != nil {
		return fmt.Errorf("parse agent SSH host key: %w", err)
	}
	config := &ssh.ClientConfig{
		User: "herdrx", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: 15 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, got ssh.PublicKey) error {
			if bytes.Equal(wantHostKey.Marshal(), got.Marshal()) {
				return nil
			}
			return fmt.Errorf("agent SSH host key mismatch")
		},
	}
	sshConn, channels, requests, err := ssh.NewClientConn(connection, "tailcat-agent", config)
	if err != nil {
		return fmt.Errorf("agent SSH handshake: %w", err)
	}
	sshClient := ssh.NewClient(sshConn, channels, requests)
	defer sshClient.Close()
	session, err := sshClient.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	output, err := session.CombinedOutput("confirm-pair " + input.Token)
	if err != nil {
		return fmt.Errorf("agent rejected pairing: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "Tailcat host"
}
