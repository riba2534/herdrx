package agentcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

type refreshEndpointRequest struct {
	Region *tailcfg.DERPRegion    `json:"region,omitempty"`
	Relay  *tunnel.RelayBootstrap `json:"relay,omitempty"`
}
type refreshEndpointResult struct {
	Relay     string `json:"relay,omitempty"`
	Update    string `json:"update"`
	Revision  int64  `json:"revision"`
	ExpiresAt int64  `json:"expires_at"`
}

func (s *IPCServer) handleRefreshEndpoint(w http.ResponseWriter, r *http.Request) {
	if err := s.checkConfigPathHeader(r); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, tunnel.MaxConnectionStringLen+1))
	if err != nil || len(data) > tunnel.MaxConnectionStringLen {
		http.Error(w, "endpoint request exceeds 16 KiB", 400)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input refreshEndpointRequest
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid endpoint request", 400)
		return
	}
	if input.Region != nil {
		input.Region, err = tunnel.DefaultSSRFValidator.ValidateDERPRegion(input.Region)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	relayStatus := ""
	var probeKey key.NodePrivate
	if input.Relay != nil {
		if input.Region != nil {
			http.Error(w, "cannot combine relay selection and an explicit region", 400)
			return
		}
		cfg, err := s.store.Snapshot()
		if err != nil {
			http.Error(w, "load binding failed", 500)
			return
		}
		var controller key.NodePublic
		if !cfg.Paired || cfg.Binding.Status != "active" || controller.UnmarshalText([]byte(cfg.Binding.FormalClientNode)) != nil {
			http.Error(w, "endpoint refresh requires an active binding", 409)
			return
		}
		probeKey = cfg.RelayProbePrivate
		if probeKey.IsZero() {
			probeKey = key.NewNode()
		}
		input.Region, relayStatus = selectWorkbenchRelay(r.Context(), cfg, input.Relay, []key.NodePublic{cfg.Node.Private.Public(), controller, probeKey.Public()})
		if input.Region == nil {
			// Resolve the public fallback before updating a bound endpoint. A
			// nil region otherwise means 'keep current' to refreshEndpoint.
			info := tailcat.ConnInfo{RegionID: -1}
			if err := info.Expand(r.Context(), tailcat.ExpandForServer); err != nil || len(info.Region) != 1 {
				http.Error(w, "public relay selection failed; existing endpoint was retained", 502)
				return
			}
			input.Region, err = tunnel.DefaultSSRFValidator.ValidateDERPRegion(info.Region[0])
			if err != nil {
				http.Error(w, "public relay validation failed; existing endpoint was retained", 502)
				return
			}
		}
	}
	result, err := s.refreshEndpoint(input.Region, probeKey)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	result.Relay = relayStatus
	_ = json.NewEncoder(w).Encode(result)
}

func (s *IPCServer) refreshEndpoint(region *tailcfg.DERPRegion, probes ...key.NodePrivate) (refreshEndpointResult, error) {
	unlock := s.lockLifecycle()
	defer unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return refreshEndpointResult{}, errors.New("daemon is stopping")
	}
	cfg, err := s.store.Snapshot()
	if err != nil {
		return refreshEndpointResult{}, err
	}
	if cfg.Revoked || !cfg.Paired || cfg.Binding.Status != "active" || cfg.Binding.BindingID == "" || cfg.Binding.ControllerID == "" {
		return refreshEndpointResult{}, errors.New("endpoint refresh requires an active binding; use herdrx connect for a new binding")
	}
	if cfg.EndpointVersion == math.MaxInt64 {
		return refreshEndpointResult{}, errors.New("endpoint revision exhausted")
	}
	probeKey := cfg.RelayProbePrivate
	if len(probes) > 0 && !probes[0].IsZero() {
		probeKey = probes[0]
	}
	if probeKey.IsZero() {
		probeKey = key.NewNode()
	}
	signer, err := ssh.ParsePrivateKey([]byte(cfg.SSHHostPrivate))
	if err != nil {
		return refreshEndpointResult{}, err
	}
	var allowed key.NodePublic
	if err := allowed.UnmarshalText([]byte(cfg.Binding.FormalClientNode)); err != nil || allowed.IsZero() {
		return refreshEndpointResult{}, errors.New("invalid bound controller identity")
	}
	previous, err := tailcat.ParseAddr(tailcat.Addr(cfg.Binding.FormalTailcatAddr))
	if err != nil || previous.PresharedKey.IsZero() || previous.PresharedKey != cfg.FormalPSK() || previous.ServerPublic.NodePublic != cfg.Node.Private.Public() {
		return refreshEndpointResult{}, errors.New("stored endpoint does not match the stable host identity")
	}
	if region != nil {
		previous.Region = []*tailcfg.DERPRegion{region}
		previous.RegionID = 0
	}
	address := string(previous.Addr())
	// Durable intent comes first. A crash after this update restarts the same
	// host identity on the new region; it cannot restore revoked authorization.
	updated, err := s.store.Update(func(c *Config) error {
		if c.Revoked || c.Binding.Status != "active" || c.Binding.BindingID != cfg.Binding.BindingID || c.EndpointVersion != cfg.EndpointVersion {
			return errors.New("binding changed; retry endpoint refresh")
		}
		applyDERPRegion(c, region)
		if !c.RelayProbePrivate.IsZero() && !c.RelayProbePrivate.Equal(probeKey) {
			return errors.New("relay diagnostic identity changed; retry endpoint refresh")
		}
		c.RelayProbePrivate = probeKey
		c.Binding.FormalTailcatAddr = address
		c.EndpointVersion++
		return nil
	})
	if err != nil {
		return refreshEndpointResult{}, fmt.Errorf("persist endpoint update: %w", err)
	}
	s.mu.Lock()
	current := s.formalServer
	change := current == nil || current.IsClosed() || current.TailcatAddr() != address
	if change {
		s.formalServer = nil
		s.remoteListenerActive = false
	}
	s.mu.Unlock()
	if change {
		if current != nil {
			_ = current.Close()
		}
		server, startErr := tunnel.StartPermanentServer(updated.Node.Private, updated.FormalPSK(), s.twoPhaseState, updated.SetupID, signer, updated.HerdrBin, allowed, regionFromConfig(updated))
		if startErr != nil {
			return refreshEndpointResult{}, fmt.Errorf("new endpoint configuration was saved but listener startup failed; rerun herdrx connect --refresh-endpoint: %w", startErr)
		}
		if server.TailcatAddr() != address {
			_ = server.Close()
			return refreshEndpointResult{}, errors.New("runtime endpoint differs from persisted identity; endpoint update was not exported")
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = server.Close()
			return refreshEndpointResult{}, errors.New("daemon is stopping; retry after restart")
		}
		s.formalServer = server
		s.formalClientNode = allowed.String()
		s.remoteListenerActive = true
		s.mu.Unlock()
	}
	now := time.Now()
	payload := tunnel.EndpointUpdate{RelayProbeNode: relayProbePublic(updated), Version: 1, AgentID: updated.SetupID, ControllerID: updated.Binding.ControllerID, BindingID: updated.Binding.BindingID, ClientNode: updated.Binding.FormalClientNode, Address: address, Revision: updated.EndpointVersion, CreatedAt: now.Unix(), ExpiresAt: now.Add(10 * time.Minute).Unix()}
	packet, err := tunnel.BuildEndpointUpdate(payload, signer)
	if err != nil {
		return refreshEndpointResult{}, err
	}
	return refreshEndpointResult{Update: packet, Revision: payload.Revision, ExpiresAt: payload.ExpiresAt}, nil
}
