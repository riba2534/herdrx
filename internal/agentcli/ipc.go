package agentcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// DaemonStatus 定义通过 IPC 导出的实时运行状态
type DaemonStatus struct {
	PID                  int             `json:"pid"`
	Version              string          `json:"version"`
	UptimeSeconds        int64           `json:"uptime_seconds"`
	Paired               bool            `json:"paired"`
	Revoked              bool            `json:"revoked"`
	TailcatAddrMasked    string          `json:"tailcat_addr_masked,omitempty"`
	ConfigPath           string          `json:"config_path"`
	HerdrPath            string          `json:"herdr_path,omitempty"`
	RemoteListenerActive bool            `json:"remote_listener_active"`
	LastError            string          `json:"last_error,omitempty"`
	HerdrStatus          PreflightResult `json:"herdr_status"`
}

type DoctorResult struct {
	DERP          []tunnel.DERPProbe `json:"derp,omitempty"`
	DaemonRunning bool               `json:"daemon_running"`
	DaemonPID     int                `json:"daemon_pid,omitempty"`
	HerdrCheck    PreflightResult    `json:"herdr_check"`
	ConfigValid   bool               `json:"config_valid"`
	ConfigPath    string             `json:"config_path"`
	Paired        bool               `json:"paired"`
	Revoked       bool               `json:"revoked"`
	OverallReady  bool               `json:"overall_ready"`
}

// IPCServer 负责管理本地控制面 Unix Domain Socket 服务
type IPCServer struct {
	closed               bool
	socketPath           string
	listener             net.Listener
	httpServer           *http.Server
	store                *StateStore
	env                  Environment
	startTime            time.Time
	remoteListenerActive bool
	lastError            string
	socketIno            uint64
	activeConnStr        string
	activeEnrollID       string
	activeConnExp        time.Time
	ephemServer          *tunnel.EphemeralTailcatServer
	formalServer         *tunnel.PermanentTailcatServer
	formalClientNode     string
	twoPhaseState        *tunnel.TwoPhaseState
	mu                   sync.Mutex
}

func NewIPCServer(env Environment, store *StateStore) *IPCServer {
	sockPath := filepath.Join(env.RuntimeDir, "control.sock")
	s := &IPCServer{
		socketPath:    sockPath,
		store:         store,
		env:           env,
		startTime:     time.Now(),
		twoPhaseState: tunnel.NewTwoPhaseState(store),
	}
	s.twoPhaseState.SetOnCommitSuccess(s.closeEphemeralAsync)
	return s
}

func (s *IPCServer) lockLifecycle() func() {
	if s.twoPhaseState == nil {
		s.twoPhaseState = tunnel.NewTwoPhaseState(s.store)
		s.twoPhaseState.SetOnCommitSuccess(s.closeEphemeralAsync)
	}
	return s.twoPhaseState.LockLifecycle()
}

func (s *IPCServer) closeEphemeralAsync() {
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closeEphemeralLocked()
	}()
}

func (s *IPCServer) closeEphemeralLocked() {
	if s.ephemServer != nil {
		_ = s.ephemServer.Close()
		s.ephemServer = nil
	}
	s.activeConnStr = ""
	s.activeEnrollID = ""
	s.activeConnExp = time.Time{}
}

func (s *IPCServer) SetRemoteStatus(active bool, lastErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteListenerActive = active
	if lastErr != nil {
		s.lastError = lastErr.Error()
	} else {
		s.lastError = ""
	}
}

// Start 安全启动 IPC 服务，严格检测连接被拒且属主匹配才清理 stale socket
func (s *IPCServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}

	// 检查现有文件
	if fi, err := os.Lstat(s.socketPath); err == nil {
		stat := fi.Sys().(*syscall.Stat_t)
		if int(stat.Uid) != os.Getuid() {
			return fmt.Errorf("socket file %s is owned by uid %d, not current user %d", s.socketPath, stat.Uid, os.Getuid())
		}
		if fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("control socket path %s is not a socket file", s.socketPath)
		}

		// 探活现有 socket
		conn, dialErr := net.DialTimeout("unix", s.socketPath, 500*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return errors.New("daemon is already running and listening on control socket")
		}

		// 仅当明确收到 connection refused 时才确认为孤儿 stale socket
		if errors.Is(dialErr, syscall.ECONNREFUSED) || strings.Contains(dialErr.Error(), "connection refused") {
			if err := os.Remove(s.socketPath); err != nil {
				return fmt.Errorf("remove stale socket: %w", err)
			}
		} else {
			return fmt.Errorf("dial existing socket failed with unhandled error (not stale): %w", dialErr)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/ready", s.handleReady)
	mux.HandleFunc("/doctor", s.handleDoctor)
	mux.HandleFunc("/connect", s.handleConnect)
	mux.HandleFunc("/endpoint", s.handleRefreshEndpoint)
	mux.HandleFunc("/unpair", s.handleUnpair)
	mux.HandleFunc("/legacy/pair", s.handleLegacyPair)
	s.httpServer = &http.Server{Handler: mux}

	// Restore endpoints before publishing the control socket so callers
	// cannot race a listen backlog while Tailcat Start is still running.
	var restoreErr error
	s.mu.Unlock()
	func() {
		defer s.mu.Lock()
		restoreErr = s.restoreFromStore()
	}()
	if restoreErr != nil {
		return fmt.Errorf("restore state from store: %w", restoreErr)
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix socket: %w", err)
	}
	_ = os.Chmod(s.socketPath, 0o600)
	s.listener = ln
	if fi, err := os.Lstat(s.socketPath); err == nil {
		s.socketIno = fi.Sys().(*syscall.Stat_t).Ino
	}

	go func() {
		_ = s.httpServer.Serve(ln)
	}()

	return nil
}

func regionFromConfig(cfg Config) *tailcfg.DERPRegion {
	if len(cfg.Node.Public.Region) > 0 {
		return cfg.Node.Public.Region[0]
	}
	return nil
}

func persistFixedRegion(c *Config, tailcatAddr string) {
	ci, err := tailcat.ParseAddr(tailcat.Addr(tailcatAddr))
	if err != nil || len(ci.Region) == 0 {
		return
	}
	encoded, err := json.Marshal(ci.Region)
	if err != nil {
		return
	}
	var cloned []*tailcfg.DERPRegion
	if json.Unmarshal(encoded, &cloned) != nil || len(cloned) == 0 {
		return
	}
	c.Node.Public.Region = cloned
}

func enrollmentContextFromConfig(cfg Config) (*tunnel.EnrollmentContext, error) {
	ctx := &tunnel.EnrollmentContext{
		EnrollmentID: cfg.Enrollment.EnrollmentID,
		EphemeralKey: cfg.Enrollment.EphemeralNodeKey,
		EphemeralPSK: cfg.Enrollment.EphemeralPSK,
		ClientPriv:   cfg.Enrollment.ClientPrivate,
		PairSecret:   cfg.Enrollment.PairSecret,
		ExpiresAt:    cfg.Enrollment.ExpiresAt,
	}
	if !cfg.Enrollment.ClientPrivate.IsZero() {
		ctx.AllowedClient = cfg.Enrollment.ClientPrivate.Public()
	} else if cfg.Enrollment.AllowedClientNode != "" {
		if err := ctx.AllowedClient.UnmarshalText([]byte(cfg.Enrollment.AllowedClientNode)); err != nil {
			return nil, fmt.Errorf("unmarshal allowed client node: %w", err)
		}
	}
	if ctx.AllowedClient.IsZero() || ctx.EphemeralKey.IsZero() || ctx.EphemeralPSK.IsZero() {
		return nil, errors.New("enrollment is missing ephemeral identity")
	}
	return ctx, nil
}

func shouldRestoreEphemeral(cfg Config) bool {
	if cfg.Enrollment.EnrollmentID == "" || cfg.Enrollment.EphemeralNodeKey.IsZero() {
		return false
	}
	now := time.Now()
	if now.Before(cfg.Enrollment.ExpiresAt) {
		return true
	}
	return cfg.Binding.Status == "prepared" &&
		cfg.Binding.EnrollmentID == cfg.Enrollment.EnrollmentID &&
		now.Before(cfg.Binding.PreparedExpiresAt)
}

func newEnrollmentID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate enrollment id: %w", err)
	}
	return "enroll_" + hex.EncodeToString(b[:]), nil
}

func (s *IPCServer) formalStarter(hostSigner ssh.Signer) func(key.NodePublic) (string, error) {
	return func(formalNode key.NodePublic) (string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()

		if s.formalServer != nil && s.formalClientNode == formalNode.String() && !s.formalServer.IsClosed() {
			return s.formalServer.TailcatAddr(), nil
		}

		freshCfg, err := s.store.Snapshot()
		if err != nil {
			return "", fmt.Errorf("snapshot config: %w", err)
		}

		formalSrv, fErr := tunnel.StartPermanentServer(
			freshCfg.Node.Private,
			freshCfg.FormalPSK(),
			s.twoPhaseState,
			freshCfg.SetupID,
			hostSigner,
			freshCfg.HerdrBin,
			formalNode,
			regionFromConfig(freshCfg),
		)
		if fErr != nil {
			// Construction failed: leave any already-reserved endpoint running.
			return "", fmt.Errorf("start permanent server: %w", fErr)
		}
		if s.formalServer != nil {
			_ = s.formalServer.Close()
		}
		s.formalServer = formalSrv
		s.formalClientNode = formalNode.String()
		s.remoteListenerActive = true
		return formalSrv.TailcatAddr(), nil
	}
}

func (s *IPCServer) restoreFromStore() error {
	cfg, err := s.store.Snapshot()
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	if cfg.Revoked || cfg.Binding.Status == "revoked" {
		return nil
	}

	var hostSigner ssh.Signer
	if cfg.SSHHostPrivate != "" {
		hostSigner, err = ssh.ParsePrivateKey([]byte(cfg.SSHHostPrivate))
		if err != nil {
			hostSigner = nil
			err = fmt.Errorf("parse host signer: %w", err)
		}
	}

	needFormal := cfg.Binding.Status == "active" ||
		(cfg.Binding.Status == "prepared" && time.Now().Before(cfg.Binding.PreparedExpiresAt))
	if needFormal {
		if hostSigner == nil {
			if err != nil {
				return err
			}
			return errors.New("missing host signer for formal recovery")
		}
		if cfg.Binding.FormalClientNode == "" {
			return errors.New("formal binding is missing client node")
		}
		var formalNode key.NodePublic
		if uerr := formalNode.UnmarshalText([]byte(cfg.Binding.FormalClientNode)); uerr != nil {
			return fmt.Errorf("unmarshal formal node key: %w", uerr)
		}
		formalSrv, ferr := tunnel.StartPermanentServer(
			cfg.Node.Private,
			cfg.FormalPSK(),
			s.twoPhaseState,
			cfg.SetupID,
			hostSigner,
			cfg.HerdrBin,
			formalNode,
			regionFromConfig(cfg),
		)
		if ferr != nil {
			return fmt.Errorf("restore permanent formal server: %w", ferr)
		}
		s.mu.Lock()
		s.formalServer = formalSrv
		s.formalClientNode = formalNode.String()
		s.remoteListenerActive = true
		s.mu.Unlock()
	}

	if !shouldRestoreEphemeral(cfg) {
		return nil
	}
	if hostSigner == nil {
		if err != nil {
			return err
		}
		return errors.New("missing host signer for enrollment recovery")
	}
	enrollCtx, eerr := enrollmentContextFromConfig(cfg)
	if eerr != nil {
		return eerr
	}
	ephemSrv, eerr := tunnel.StartEphemeralPairingServer(
		enrollCtx, s.twoPhaseState, cfg.SetupID, hostSigner, regionFromConfig(cfg), s.formalStarter(hostSigner),
	)
	if eerr != nil {
		return fmt.Errorf("restore ephemeral pairing server: %w", eerr)
	}
	s.mu.Lock()
	s.ephemServer = ephemSrv
	s.activeConnStr = cfg.Enrollment.ConnectionString
	s.activeEnrollID = cfg.Enrollment.EnrollmentID
	s.activeConnExp = cfg.Enrollment.ExpiresAt
	s.remoteListenerActive = true
	s.mu.Unlock()
	return nil
}

func (s *IPCServer) Close() error {
	s.mu.Lock()
	s.closed = true
	defer s.mu.Unlock()
	var err error
	if s.ephemServer != nil {
		_ = s.ephemServer.Close()
		s.ephemServer = nil
	}
	if s.formalServer != nil {
		_ = s.formalServer.Close()
		s.formalServer = nil
	}
	if s.httpServer != nil {
		_ = s.httpServer.Close()
	}
	if s.listener != nil {
		err = s.listener.Close()
	}
	// 仅删除本实例创建的 socket 文件（对比 inode 防止误删新建实例）
	if fi, statErr := os.Lstat(s.socketPath); statErr == nil {
		if fi.Sys().(*syscall.Stat_t).Ino == s.socketIno {
			_ = os.Remove(s.socketPath)
		}
	}
	return err
}

var ErrDaemonOffline = errors.New("daemon is not running")

func (s *IPCServer) checkConfigPathHeader(r *http.Request) error {
	reqPath := r.Header.Get("X-Herdrx-Config-Path")
	if reqPath == "" {
		return nil
	}
	cleanReq, err := filepath.Abs(filepath.Clean(reqPath))
	if err != nil {
		return fmt.Errorf("invalid requested config path: %w", err)
	}
	cleanStore := filepath.Clean(s.store.ConfigPath())
	if cleanReq != cleanStore {
		return fmt.Errorf("target config mismatch: daemon is serving %s, client requested %s", cleanStore, cleanReq)
	}
	return nil
}

func (s *IPCServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if err := s.checkConfigPathHeader(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg, _ := s.store.Snapshot()
	envCopy := s.env
	if cfg.HerdrBin != "" {
		envCopy.HerdrBin = cfg.HerdrBin
	}
	preflight := RunPreflight(envCopy)

	var maskedAddr string
	if cfg.Node.Public.Addr() != "" {
		maskedAddr = MaskAddress(string(cfg.Node.Public.Addr()))
	}

	s.mu.Lock()
	rActive := s.remoteListenerActive
	lastErr := s.lastError
	s.mu.Unlock()

	status := DaemonStatus{
		PID:                  os.Getpid(),
		Version:              Version,
		UptimeSeconds:        int64(time.Since(s.startTime).Seconds()),
		Paired:               cfg.Paired,
		Revoked:              cfg.Revoked,
		TailcatAddrMasked:    maskedAddr,
		ConfigPath:           s.store.ConfigPath(),
		HerdrPath:            preflight.HerdrPath,
		RemoteListenerActive: rActive,
		LastError:            lastErr,
		HerdrStatus:          preflight,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (s *IPCServer) handleDoctor(w http.ResponseWriter, r *http.Request) {
	if err := s.checkConfigPathHeader(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg, err := s.store.Snapshot()
	cfgValid := (err == nil && cfg.Version == 1)
	envCopy := s.env
	if cfg.HerdrBin != "" {
		envCopy.HerdrBin = cfg.HerdrBin
	}
	preflight := RunPreflight(envCopy)

	doc := DoctorResult{
		DaemonRunning: true,
		DaemonPID:     os.Getpid(),
		HerdrCheck:    preflight,
		ConfigValid:   cfgValid,
		ConfigPath:    s.store.ConfigPath(),
		Paired:        cfg.Paired,
		Revoked:       cfg.Revoked,
		OverallReady:  preflight.Status == PreflightOK && cfgValid,
	}
	if r.URL.Query().Get("network") == "true" {
		doc.DERP = probeConfiguredRelay(r.Context(), cfg)
		reachable := false
		for _, probe := range doc.DERP {
			reachable = reachable || probe.Reachable
		}
		doc.OverallReady = doc.OverallReady && reachable
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

type ConnectResult struct {
	Relay            string    `json:"relay,omitempty"`
	ConnectionString string    `json:"connection_string"`
	EnrollmentID     string    `json:"enrollment_id"`
	ExpiresAt        time.Time `json:"expires_at"`
	AgentID          string    `json:"agent_id"`
}

func (s *IPCServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	if err := s.checkConfigPathHeader(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input connectRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid connect request", 400)
		return
	}

	unlock := s.lockLifecycle()
	defer unlock()

	cfg, err := s.store.Snapshot()
	if err != nil {
		http.Error(w, fmt.Sprintf("load config: %v", err), http.StatusInternalServerError)
		return
	}

	if (cfg.Paired && !cfg.Revoked) || cfg.Binding.Status == "active" {
		http.Error(w, "agent is already active and bound to a controller. Revoke locally first before connecting to a new controller.", http.StatusConflict)
		return
	}

	renew := r.URL.Query().Get("renew") == "true"
	if !renew && shouldRestoreEphemeral(cfg) {
		res, rerr := s.reuseUnexpiredEnrollment(cfg)
		if rerr != nil {
			http.Error(w, rerr.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
		return
	}

	res, err := s.issueEnrollment(cfg, renew, input.Relay)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *IPCServer) reuseUnexpiredEnrollment(cfg Config) (ConnectResult, error) {
	s.mu.Lock()
	live := s.ephemServer != nil
	connStr := s.activeConnStr
	s.mu.Unlock()
	if connStr == "" {
		connStr = cfg.Enrollment.ConnectionString
	}
	if !live {
		if err := s.startEphemeralFromConfig(cfg); err != nil {
			return ConnectResult{}, fmt.Errorf("restore unexpired enrollment: %w", err)
		}
	}
	if connStr == "" {
		rebuilt, err := s.rebuildPersistedConnectionString(cfg)
		if err != nil {
			return ConnectResult{}, err
		}
		connStr = rebuilt
	}
	return ConnectResult{
		ConnectionString: connStr,
		EnrollmentID:     cfg.Enrollment.EnrollmentID,
		ExpiresAt:        cfg.Enrollment.ExpiresAt,
		AgentID:          cfg.SetupID,
	}, nil
}

func (s *IPCServer) rebuildPersistedConnectionString(cfg Config) (string, error) {
	if cfg.Enrollment.ClientPrivate.IsZero() {
		return "", errors.New("unexpired enrollment is missing client private key")
	}
	hostSigner, err := ssh.ParsePrivateKey([]byte(cfg.SSHHostPrivate))
	if err != nil {
		return "", fmt.Errorf("parse host private key: %w", err)
	}
	s.mu.Lock()
	addr := ""
	if s.ephemServer != nil {
		addr = s.ephemServer.TailcatAddr()
	}
	s.mu.Unlock()
	if addr == "" {
		return "", errors.New("restored enrollment is missing tailcat address")
	}
	privTxt, err := cfg.Enrollment.ClientPrivate.MarshalText()
	if err != nil {
		return "", err
	}
	payload := tunnel.ConnectionPayload{
		RelayProbeNode: relayProbePublic(cfg),
		V:              1,
		AgentID:        cfg.SetupID,
		TailcatAddr:    addr,
		ClientPriv:     string(privTxt),
		SSHHostKey:     strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))),
		EnrollmentID:   cfg.Enrollment.EnrollmentID,
		PairSecret:     cfg.Enrollment.PairSecret,
		Host:           hostname(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		AgentVer:       Version,
		Exp:            cfg.Enrollment.ExpiresAt.Unix(),
	}
	connStr, err := tunnel.BuildConnectionString(payload)
	if err != nil {
		return "", err
	}
	_, _ = s.store.Update(func(c *Config) error {
		if c.Enrollment.EnrollmentID == cfg.Enrollment.EnrollmentID && c.Enrollment.ConnectionString == "" {
			c.Enrollment.ConnectionString = connStr
			persistFixedRegion(c, addr)
		}
		return nil
	})
	s.mu.Lock()
	s.activeConnStr = connStr
	s.activeEnrollID = cfg.Enrollment.EnrollmentID
	s.activeConnExp = cfg.Enrollment.ExpiresAt
	s.mu.Unlock()
	return connStr, nil
}

func (s *IPCServer) startEphemeralFromConfig(cfg Config) error {
	hostSigner, err := ssh.ParsePrivateKey([]byte(cfg.SSHHostPrivate))
	if err != nil {
		return fmt.Errorf("parse host private key: %w", err)
	}
	enrollCtx, err := enrollmentContextFromConfig(cfg)
	if err != nil {
		return err
	}
	ephemSrv, err := tunnel.StartEphemeralPairingServer(
		enrollCtx, s.twoPhaseState, cfg.SetupID, hostSigner, regionFromConfig(cfg), s.formalStarter(hostSigner),
	)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.ephemServer != nil {
		_ = ephemSrv.Close()
		s.mu.Unlock()
		return nil
	}
	s.ephemServer = ephemSrv
	s.activeConnStr = cfg.Enrollment.ConnectionString
	s.activeEnrollID = cfg.Enrollment.EnrollmentID
	s.activeConnExp = cfg.Enrollment.ExpiresAt
	s.remoteListenerActive = true
	s.mu.Unlock()
	return nil
}

func (s *IPCServer) issueEnrollment(cfg Config, renew bool, relays ...*tunnel.RelayBootstrap) (ConnectResult, error) {
	s.mu.Lock()
	if s.ephemServer != nil {
		_ = s.ephemServer.Close()
		s.ephemServer = nil
	}
	if renew && cfg.Binding.Status == "prepared" && s.formalServer != nil {
		_ = s.formalServer.Close()
		s.formalServer = nil
		s.formalClientNode = ""
	}
	s.mu.Unlock()

	enrollmentID, err := newEnrollmentID()
	if err != nil {
		return ConnectResult{}, err
	}
	pairSecret, err := secure.Token(32)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("generate pair secret: %w", err)
	}
	ephemNodeKey := key.NewNode()
	ephemPSK := tailcat.NewPresharedKey()
	tempClientKey := key.NewNode()
	expiresAt := time.Now().Add(10 * time.Minute)
	var selected *tunnel.RelayBootstrap
	if len(relays) > 0 {
		selected = relays[0]
	}
	nodes := []key.NodePublic{cfg.Node.Private.Public(), ephemNodeKey.Public(), tempClientKey.Public()}
	if selected != nil {
		if cfg.RelayProbePrivate.IsZero() {
			cfg.RelayProbePrivate = key.NewNode()
		}
		nodes = append(nodes, cfg.RelayProbePrivate.Public())
	}
	region, relayStatus := selectWorkbenchRelay(context.Background(), cfg, selected, nodes)

	hostSigner, err := ssh.ParsePrivateKey([]byte(cfg.SSHHostPrivate))
	if err != nil {
		return ConnectResult{}, fmt.Errorf("parse host private key: %v", err)
	}

	enrollCtx := &tunnel.EnrollmentContext{
		EnrollmentID:  enrollmentID,
		EphemeralKey:  ephemNodeKey,
		EphemeralPSK:  ephemPSK,
		ClientPriv:    tempClientKey,
		AllowedClient: tempClientKey.Public(),
		PairSecret:    pairSecret,
		ExpiresAt:     expiresAt,
	}

	ephemSrv, err := tunnel.StartEphemeralPairingServer(
		enrollCtx, s.twoPhaseState, cfg.SetupID, hostSigner, region, s.formalStarter(hostSigner),
	)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("start ephemeral pairing server: %v", err)
	}

	tempClientPrivTxt, err := tempClientKey.MarshalText()
	if err != nil {
		_ = ephemSrv.Close()
		return ConnectResult{}, fmt.Errorf("marshal client private key: %w", err)
	}
	payload := tunnel.ConnectionPayload{
		RelayProbeNode: relayProbePublic(cfg),
		V:              1,
		AgentID:        cfg.SetupID,
		TailcatAddr:    enrollCtx.TailcatAddr,
		ClientPriv:     string(tempClientPrivTxt),
		SSHHostKey:     strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))),
		EnrollmentID:   enrollmentID,
		PairSecret:     pairSecret,
		Host:           hostname(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		AgentVer:       Version,
		Exp:            expiresAt.Unix(),
	}
	connStr, err := tunnel.BuildConnectionString(payload)
	if err != nil {
		_ = ephemSrv.Close()
		return ConnectResult{}, fmt.Errorf("build connection string: %v", err)
	}

	_, err = s.store.Update(func(c *Config) error {
		if c.Paired || c.Binding.Status == "active" {
			return errors.New("agent binding changed during connect")
		}
		c.Revoked = false
		c.RelayProbePrivate = cfg.RelayProbePrivate
		c.Enrollment = agent.EnrollmentConfig{
			EnrollmentID:      enrollmentID,
			EphemeralNodeKey:  ephemNodeKey,
			EphemeralPSK:      ephemPSK,
			ClientPrivate:     tempClientKey,
			ConnectionString:  connStr,
			AllowedClientNode: tempClientKey.Public().String(),
			PairSecret:        pairSecret,
			ExpiresAt:         expiresAt,
		}
		persistFixedRegion(c, enrollCtx.TailcatAddr)
		if renew || c.Binding.Status != "active" {
			c.Binding = agent.BindingConfig{
				Status: "none",
				Epoch:  agent.NextEpoch(c.Binding.Epoch),
			}
		}
		return nil
	})
	if err != nil {
		_ = ephemSrv.Close()
		return ConnectResult{}, fmt.Errorf("save enrollment: %v", err)
	}

	s.mu.Lock()
	s.ephemServer = ephemSrv
	s.activeConnStr = connStr
	s.activeEnrollID = enrollmentID
	s.activeConnExp = expiresAt
	s.remoteListenerActive = true
	s.mu.Unlock()

	return ConnectResult{
		Relay:            relayStatus,
		ConnectionString: connStr,
		EnrollmentID:     enrollmentID,
		ExpiresAt:        expiresAt,
		AgentID:          cfg.SetupID,
	}, nil
}

func (s *IPCServer) handleUnpair(w http.ResponseWriter, r *http.Request) {
	if err := s.checkConfigPathHeader(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.twoPhaseState == nil {
		s.twoPhaseState = tunnel.NewTwoPhaseState(s.store)
	}
	persistErr := s.twoPhaseState.Revoke()

	s.mu.Lock()
	s.closeEphemeralLocked()
	formal := s.formalServer
	s.formalServer = nil
	s.formalClientNode = ""
	s.remoteListenerActive = false
	s.mu.Unlock()
	if formal != nil {
		// Do not hold the IPC mutex while the userspace TCP stack drains.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = formal.CloseWithDrain(ctx)
		cancel()
	}

	if persistErr != nil {
		http.Error(w, persistErr.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "unpaired"})
}

type LegacyPairResult struct {
	URL       string    `json:"url"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *IPCServer) handleLegacyPair(w http.ResponseWriter, r *http.Request) {
	if err := s.checkConfigPathHeader(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token, err := secure.Token(16)
	if err != nil {
		http.Error(w, fmt.Sprintf("generate token: %v", err), http.StatusInternalServerError)
		return
	}

	tokenHashStr := base64.RawStdEncoding.EncodeToString(secure.TokenHash(token))
	expiresAt := time.Now().Add(10 * time.Minute)

	updatedCfg, err := s.store.Update(func(c *Config) error {
		c.PairTokenHash = tokenHashStr
		c.PairExpiresAt = expiresAt
		c.Paired = false
		return nil
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("update config: %v", err), http.StatusInternalServerError)
		return
	}

	hostSigner, err := ssh.ParsePrivateKey([]byte(updatedCfg.SSHHostPrivate))
	if err != nil {
		http.Error(w, fmt.Sprintf("parse host private key: %v", err), http.StatusInternalServerError)
		return
	}

	hName, _ := os.Hostname()
	if hName == "" {
		hName = "herdr-host"
	}

	payload := map[string]any{
		"v": 1, "setup_id": updatedCfg.SetupID, "tc": updatedCfg.Node.Public.Addr(), "host": hName,
		"os": runtime.GOOS, "arch": runtime.GOARCH, "agent_ver": Version,
		"ssh_host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))),
		"token":        token, "exp": updatedCfg.PairExpiresAt.Unix(),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, fmt.Sprintf("marshal payload: %v", err), http.StatusInternalServerError)
		return
	}
	pairURL := updatedCfg.PublicURL + "/#pair=" + base64.RawURLEncoding.EncodeToString(encoded)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(LegacyPairResult{
		URL:       pairURL,
		Token:     token,
		ExpiresAt: expiresAt,
	})
}

// ClientCallIPC 通过 Unix Domain Socket 发起调用，包含真实 Body 序列化与连接池清理
func ClientCallIPC(socketPath, method, endpoint string, body any, respObj any) error {
	return ClientCallIPCWithConfig(socketPath, method, endpoint, "", body, respObj)
}

// ClientCallIPCWithConfig 显式携带目标配置路径，并对离线/网络错误进行严格分类
func ClientCallIPCWithConfig(socketPath, method, endpoint, expectedConfigPath string, body any, respObj any) error {
	return clientCallIPCContext(context.Background(), socketPath, method, endpoint, expectedConfigPath, body, respObj)
}

func clientCallIPCContext(ctx context.Context, socketPath, method, endpoint, expectedConfigPath string, body any, respObj any) error {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	url := "http://unix" + endpoint
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if expectedConfigPath != "" {
		req.Header.Set("X-Herdrx-Config-Path", expectedConfigPath)
	}

	resp, err := client.Do(req)
	if err != nil {
		// 严格分类：仅当 socket 确实不存在 (ENOENT) 或连接被拒 (ECONNREFUSED) 时，才判定为 ErrDaemonOffline
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "no such file or directory") {
			return ErrDaemonOffline
		}
		return fmt.Errorf("call daemon ipc: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotImplemented {
		return errors.New("command not implemented yet")
	}
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("daemon error (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	if respObj != nil {
		return json.NewDecoder(resp.Body).Decode(respObj)
	}
	return nil
}

// MaskAddress 将连接地址脱敏，不泄漏敏感凭据
func MaskAddress(addr string) string {
	if len(addr) <= 24 {
		return "***"
	}
	parts := strings.Split(addr, "?")
	if len(parts) == 1 {
		if len(addr) > 20 {
			return addr[:12] + "..." + addr[len(addr)-6:]
		}
		return addr[:8] + "..."
	}
	base := parts[0]
	if len(base) > 16 {
		base = base[:10] + "..." + base[len(base)-4:]
	}
	return base + "?psk=[REDACTED]"
}
