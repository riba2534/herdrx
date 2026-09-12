package hostruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	tslogger "tailscale.com/types/logger"
)

// Factory caches access transports. It never owns a remote Herdr process.
type Factory struct {
	Config    config.Config
	Store     *store.Store
	Vault     *secure.Vault
	mu        sync.Mutex
	endpoints map[string]*sharedEndpoint
	flights   map[string]*dialFlight
	failures  map[string]dialFailure
	dialSlots chan struct{}
	closed    bool
	dial      func(context.Context, store.Host) (herdr.Endpoint, error)
}

type sharedEndpoint struct {
	endpoint  herdr.Endpoint
	refs      int
	idle      *time.Timer
	closed    bool
	idleSince time.Time
}

func (f *Factory) Open(ctx context.Context, host store.Host) (herdr.Endpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, net.ErrClosed
	}
	if host.Transport == "local" {
		f.mu.Unlock()
		if f.Store == nil {
			return nil, store.ErrAdminRequired
		}
		user, err := f.Store.UserByID(ctx, host.OwnerID)
		if err != nil {
			return nil, err
		}
		if user.Role != "admin" || user.Disabled {
			return nil, store.ErrAdminRequired
		}
		endpoint, err := herdr.NewLocalEndpoint(f.Config.HerdrBinary, host.SessionName)
		if err != nil {
			return nil, err
		}
		return endpoint, nil
	}
	return f.openSharedLocked(ctx, host)
}

func (f *Factory) openUncached(ctx context.Context, host store.Host) (herdr.Endpoint, error) {
	switch host.Transport {
	case "ssh":
		if host.AuthMethod == "system_ssh" {
			user, err := f.Store.UserByID(ctx, host.OwnerID)
			if err != nil {
				return nil, err
			}
			if user.Role != "admin" || user.Disabled {
				return nil, store.ErrAdminRequired
			}
			endpoint, err := herdr.DialOpenSSHEndpoint(ctx, f.Config.SSHBinary, herdr.SSHOptions{Host: host, Timeout: f.Config.HostDialTimeout})
			if err != nil {
				return nil, err
			}
			return endpoint, nil
		}
		credential, err := f.Store.CredentialByID(ctx, host.OwnerID, host.CredentialID)
		if err != nil {
			return nil, fmt.Errorf("load host credential: %w", err)
		}
		secret, err := f.Vault.Open(credential.Ciphertext, secretAAD(host.OwnerID, credential.ID, credential.Kind))
		if err != nil {
			return nil, err
		}
		endpoint, err := herdr.DialSSHEndpoint(ctx, herdr.SSHOptions{
			Host: host, Secret: secret, Timeout: 15 * time.Second,
			OnHostKey: func(key, _ string) { _ = f.Store.SetPendingHostKeyIfCurrent(ctx, host, key) },
		})
		if err != nil {
			// Returning a nil *SSHEndpoint directly as Endpoint produces a
			// non-nil interface that the pool would try to close on failure.
			return nil, err
		}
		return endpoint, nil
	case "tailcat":
		credential, err := f.Store.CredentialByID(ctx, host.OwnerID, host.CredentialID)
		if err != nil {
			return nil, fmt.Errorf("load tailcat credential: %w", err)
		}
		secret, err := f.Vault.Open(credential.Ciphertext, secretAAD(host.OwnerID, credential.ID, credential.Kind))
		if err != nil {
			return nil, err
		}
		var tailcatSecret TailcatCredential
		if err := json.Unmarshal(secret, &tailcatSecret); err != nil {
			return nil, fmt.Errorf("decode tailcat credential: %w", err)
		}
		targetAddr := tailcatSecret.DialFormalAddr
		if targetAddr == "" {
			targetAddr = tailcatSecret.FormalTailcatAddr
		}
		if targetAddr == "" {
			targetAddr = host.TailcatAddr
		}
		if validated, err := tunnel.DefaultSSRFValidator.ValidateDialAddr(targetAddr); err == nil {
			targetAddr = validated
		} else {
			return nil, fmt.Errorf("validate tailcat dial address: %w", err)
		}
		client, connection, err := dialTailcatSSH(ctx, targetAddr, tailcatSecret.NodePrivate)
		if err != nil {
			return nil, err
		}
		host.AuthMethod = "private_key"
		host.Username = "herdrx"
		endpoint, err := herdr.DialSSHOnConn(ctx, connection, "tailcat-agent", herdr.SSHOptions{Host: host, Secret: []byte(tailcatSecret.SSHPrivate), Timeout: 15 * time.Second}, client)
		if err != nil {
			return nil, err
		}
		return endpoint, nil
	default:
		return nil, fmt.Errorf("transport %q is not available yet", host.Transport)
	}
}

func dialTailcatSSH(ctx context.Context, address string, nodePrivate key.NodePrivate) (*tailcat.Client, net.Conn, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		client := tailcat.NewClient(tailcat.Addr(address))
		client.Key = nodePrivate
		client.Logf = tslogger.Discard
		pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err := client.Ping(pingCtx)
		cancel()
		if err == nil {
			var connection net.Conn
			connection, err = client.DialTCPPort(ctx, 22)
			if err == nil {
				return client, connection, nil
			}
		}
		lastErr = err
		_ = client.Close()
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	return nil, nil, fmt.Errorf("connect tailcat agent: %w", lastErr)
}

type TailcatCredential struct {
	RelayProbeNode    string          `json:"relay_probe_node,omitempty"`
	BindingID         string          `json:"binding_id,omitempty"`
	EndpointVersion   int64           `json:"endpoint_version,omitempty"`
	NodePrivate       key.NodePrivate `json:"node_private"`
	SSHPrivate        string          `json:"ssh_private"`
	FormalTailcatAddr string          `json:"formal_tailcat_addr,omitempty"`
	RawFormalAddr     string          `json:"raw_formal_addr,omitempty"`
	DialFormalAddr    string          `json:"dial_formal_addr,omitempty"`
	BootstrapAddr     string          `json:"bootstrap_addr,omitempty"`
	BootstrapDialAddr string          `json:"bootstrap_dial_addr,omitempty"`
	BootstrapClient   key.NodePrivate `json:"bootstrap_client,omitempty"`
	PairSecret        string          `json:"pair_secret,omitempty"`
	EnrollmentID      string          `json:"enrollment_id,omitempty"`
	SSHHostKey        string          `json:"ssh_host_key,omitempty"`
	RequestID         string          `json:"request_id,omitempty"`
	ControllerID      string          `json:"controller_id,omitempty"`
	AgentID           string          `json:"agent_id,omitempty"`
	PreparedExpiresAt int64           `json:"prepared_expires_at,omitempty"`
}

type sharedHandle struct {
	factory *Factory
	hostID  string
	entry   *sharedEndpoint
	once    sync.Once
}

func (f *Factory) CloseHost(hostID string) {
	f.mu.Lock()
	delete(f.failures, hostID)
	if flight := f.flights[hostID]; flight != nil {
		f.cancelFlightLocked(hostID, flight, net.ErrClosed)
	}
	entry := f.endpoints[hostID]
	delete(f.endpoints, hostID)
	if entry != nil {
		entry.closed = true
		if entry.idle != nil {
			entry.idle.Stop()
		}
	}
	f.mu.Unlock()
	if entry != nil {
		_ = entry.endpoint.Close()
	}
}

func (f *Factory) Close() {
	f.mu.Lock()
	f.closed = true
	for id, flight := range f.flights {
		f.cancelFlightLocked(id, flight, net.ErrClosed)
	}
	entries := f.endpoints
	f.endpoints = nil
	f.failures = nil
	for _, entry := range entries {
		entry.closed = true
		if entry.idle != nil {
			entry.idle.Stop()
		}
	}
	f.mu.Unlock()
	for _, entry := range entries {
		_ = entry.endpoint.Close()
	}
}

func (h *sharedHandle) Snapshot(ctx context.Context) (herdr.Snapshot, error) {
	value, err := h.entry.endpoint.Snapshot(ctx)
	if ctx.Err() == nil && isTransportError(err) {
		h.invalidate()
	}
	return value, err
}
func (h *sharedHandle) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	value, err := h.entry.endpoint.Call(ctx, method, params)
	if ctx.Err() == nil && isTransportError(err) {
		h.invalidate()
	}
	return value, err
}
func (h *sharedHandle) OpenTerminal(ctx context.Context, request herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	value, err := h.entry.endpoint.OpenTerminal(ctx, request)
	if ctx.Err() == nil && isTransportError(err) {
		h.invalidate()
	}
	return value, err
}
func (h *sharedHandle) TerminalGeometry(ctx context.Context, paneID string) (herdr.TerminalGeometry, error) {
	endpoint, ok := h.entry.endpoint.(herdr.NativeScrollEndpoint)
	if !ok {
		return herdr.TerminalGeometry{}, fmt.Errorf("host cannot preserve terminal geometry")
	}
	value, err := endpoint.TerminalGeometry(ctx, paneID)
	if ctx.Err() == nil && isTransportError(err) {
		h.invalidate()
	}
	return value, err
}
func (h *sharedHandle) OpenTerminalSocket(ctx context.Context) (net.Conn, error) {
	endpoint, ok := h.entry.endpoint.(herdr.NativeScrollEndpoint)
	if !ok {
		return nil, fmt.Errorf("host does not support native terminal scrolling")
	}
	value, err := endpoint.OpenTerminalSocket(ctx)
	if ctx.Err() == nil && isTransportError(err) {
		h.invalidate()
	}
	return value, err
}
func (h *sharedHandle) StageImage(ctx context.Context, ext string, r io.Reader) (string, error) {
	value, err := h.entry.endpoint.StageImage(ctx, ext, r)
	if ctx.Err() == nil && isTransportError(err) {
		h.invalidate()
	}
	return value, err
}
func (h *sharedHandle) Close() error {
	h.once.Do(func() {
		f := h.factory
		f.mu.Lock()
		defer f.mu.Unlock()
		entry := h.entry
		entry.refs--
		if entry.closed || entry.refs != 0 {
			return
		}
		idle := f.Config.HostIdleTimeout
		if idle <= 0 {
			idle = 2 * time.Minute
		}
		entry.idleSince = time.Now()
		entry.idle = time.AfterFunc(idle, func() {
			f.mu.Lock()
			if entry.closed || entry.refs != 0 || f.endpoints[h.hostID] != entry {
				f.mu.Unlock()
				return
			}
			entry.closed = true
			delete(f.endpoints, h.hostID)
			f.mu.Unlock()
			_ = entry.endpoint.Close()
		})
	})
	return nil
}

// Invalidate discards only this observed transport after repeated failed health checks.
func (h *sharedHandle) Invalidate() { h.invalidate() }

func (h *sharedHandle) invalidate() {
	f := h.factory
	f.mu.Lock()
	entry := h.entry
	if entry.closed {
		f.mu.Unlock()
		return
	}
	entry.closed = true
	if entry.idle != nil {
		entry.idle.Stop()
	}
	if f.endpoints[h.hostID] == entry {
		delete(f.endpoints, h.hostID)
	}
	f.mu.Unlock()
	// Closing a failed transport may block; never hold the factory lock here.
	_ = entry.endpoint.Close()
}

func isTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, token := range []string{"connection reset", "broken pipe", "i/o timeout", "use of closed", "connection refused", "eof"} {
		if strings.Contains(msg, token) {
			return true
		}
	}
	return false
}

func secretAAD(ownerID, credentialID, kind string) []byte {
	return []byte(ownerID + "\x00" + credentialID + "\x00" + kind)
}

func SealCredential(vault *secure.Vault, ownerID, credentialID, kind string, secret []byte) ([]byte, error) {
	return vault.Seal(secret, secretAAD(ownerID, credentialID, kind))
}

func OpenCredential(vault *secure.Vault, ownerID, credentialID, kind string, ciphertext []byte) ([]byte, error) {
	return vault.Open(ciphertext, secretAAD(ownerID, credentialID, kind))
}
