package tunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/wgengine/filter"
)

type EnrollmentContext struct {
	EnrollmentID  string
	EphemeralKey  key.NodePrivate
	EphemeralPSK  tailcat.PresharedKey
	ClientPriv    key.NodePrivate
	AllowedClient key.NodePublic
	PairSecret    string
	ExpiresAt     time.Time
	TailcatAddr   string
}

// ConfigStore 定义与受控端配置事务管理器交互的抽象接口
type ConfigStore interface {
	Snapshot() (agent.Config, error)
	Update(mutator func(*agent.Config) error) (agent.Config, error)
}

// TwoPhaseState 封装基于 StateStore 事务落盘的两阶段绑定状态机
type TwoPhaseState struct {
	store           ConfigStore
	lifecycleMu     sync.Mutex
	mu              sync.RWMutex
	onCommitSuccess func()
}

func NewTwoPhaseState(store ConfigStore) *TwoPhaseState {
	return &TwoPhaseState{
		store: store,
	}
}

func (s *TwoPhaseState) SetOnCommitSuccess(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onCommitSuccess = fn
}

func (s *TwoPhaseState) Status() string {
	if s.store == nil {
		return "none"
	}
	cfg, err := s.store.Snapshot()
	if err != nil || cfg.Binding.Status == "" {
		return "none"
	}
	return cfg.Binding.Status
}

type BindingSnapshot struct {
	Status             string
	BindingID          string
	EnrollmentID       string
	RequestID          string
	ControllerID       string
	FormalClientNode   string
	FormalSSHPublicKey string
	FormalTailcatAddr  string
	Challenge          string
	PreparedExpiresAt  time.Time
	Epoch              int64
	Paired             bool
	Revoked            bool
}

func (s *TwoPhaseState) Snapshot() (BindingSnapshot, error) {
	if s.store == nil {
		return BindingSnapshot{}, errors.New("state store is not configured")
	}
	cfg, err := s.store.Snapshot()
	if err != nil {
		return BindingSnapshot{}, err
	}
	status := cfg.Binding.Status
	if status == "" {
		status = "none"
	}
	return BindingSnapshot{
		Status:             status,
		BindingID:          cfg.Binding.BindingID,
		EnrollmentID:       cfg.Binding.EnrollmentID,
		RequestID:          cfg.Binding.RequestID,
		ControllerID:       cfg.Binding.ControllerID,
		FormalClientNode:   cfg.Binding.FormalClientNode,
		FormalSSHPublicKey: cfg.Binding.FormalSSHPublicKey,
		FormalTailcatAddr:  cfg.Binding.FormalTailcatAddr,
		Challenge:          cfg.Binding.Challenge,
		PreparedExpiresAt:  cfg.Binding.PreparedExpiresAt,
		Epoch:              cfg.Binding.Epoch,
		Paired:             cfg.Paired,
		Revoked:            cfg.Revoked,
	}, nil
}

func (s *TwoPhaseState) FormalSSHPublicKey() string {
	if s.store == nil {
		return ""
	}
	cfg, err := s.store.Snapshot()
	if err != nil {
		return ""
	}
	return cfg.Binding.FormalSSHPublicKey
}

// LockLifecycle serializes endpoint creation with renewal, activation and revocation.
// Callers must acquire this before the daemon's endpoint mutex and must not call
// Prepare/Commit/Revoke while holding it (those methods acquire it themselves).
func (s *TwoPhaseState) LockLifecycle() func() {
	s.lifecycleMu.Lock()
	return s.lifecycleMu.Unlock
}

var protocolID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func (s *TwoPhaseState) PrepareWithStarter(enrollmentID, reqID, ctrlID, formalNode, formalSSHPub string, startFormal func() (string, error)) (string, string, string, time.Time, error) {
	defer s.LockLifecycle()()
	fail := func(err error) (string, string, string, time.Time, error) { return "", "", "", time.Time{}, err }
	if s.store == nil || startFormal == nil {
		return fail(errors.New("state store or endpoint starter is not configured"))
	}
	if !protocolID.MatchString(enrollmentID) || !protocolID.MatchString(reqID) || !protocolID.MatchString(ctrlID) {
		return fail(errors.New("invalid enrollment/request/controller ID"))
	}
	var node key.NodePublic
	if err := node.UnmarshalText([]byte(formalNode)); err != nil || node.IsZero() {
		return fail(errors.New("invalid formal node public key"))
	}
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(formalSSHPub))
	if err != nil || len(rest) != 0 || pub.Type() != ssh.KeyAlgoED25519 {
		return fail(errors.New("formal SSH public key must be ed25519"))
	}
	formalSSHPub = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	c, err := s.store.Snapshot()
	if err != nil {
		return fail(err)
	}
	if c.Revoked || c.Binding.Status == "revoked" {
		return fail(errors.New("agent has been revoked"))
	}
	b := c.Binding
	if b.Status == "prepared" || b.Status == "active" {
		if b.EnrollmentID != enrollmentID || b.RequestID != reqID || b.ControllerID != ctrlID || b.FormalClientNode != formalNode || strings.TrimSpace(b.FormalSSHPublicKey) != formalSSHPub {
			return fail(errors.New("binding conflict: enrollment is already reserved for another controller/key"))
		}
		if b.Status == "prepared" && !time.Now().Before(b.PreparedExpiresAt) {
			return fail(errors.New("prepared binding has expired"))
		}
		return b.BindingID, b.Challenge, b.FormalTailcatAddr, b.PreparedExpiresAt, nil
	}
	if c.Paired || c.Enrollment.EnrollmentID != enrollmentID || !time.Now().Before(c.Enrollment.ExpiresAt) {
		return fail(errors.New("enrollment is unavailable or expired"))
	}
	// The lifecycle lock spans the real endpoint startup. A competing request cannot
	// start a second server with the same node identity before this claim commits.
	addr, err := startFormal()
	if err != nil {
		return fail(fmt.Errorf("start formal server: %w", err))
	}
	randBytes, chalBytes := make([]byte, 16), make([]byte, 32)
	if _, err := rand.Read(randBytes); err != nil {
		return fail(err)
	}
	if _, err := rand.Read(chalBytes); err != nil {
		return fail(err)
	}
	b = agent.BindingConfig{
		Status: "prepared", BindingID: "bind_" + hex.EncodeToString(randBytes),
		EnrollmentID: enrollmentID, RequestID: reqID, ControllerID: ctrlID,
		FormalClientNode: formalNode, FormalSSHPublicKey: formalSSHPub,
		FormalTailcatAddr: addr, Challenge: hex.EncodeToString(chalBytes),
		PreparedExpiresAt: time.Now().Add(10 * time.Minute), Epoch: agent.NextEpoch(c.Binding.Epoch),
	}
	_, err = s.store.Update(func(current *agent.Config) error {
		// Fence even writers outside this lifecycle object (offline migration and
		// injected persistence failures). Never resurrect a revoked enrollment.
		if current.Revoked || current.Paired || current.Binding.Epoch != c.Binding.Epoch || current.Enrollment.EnrollmentID != enrollmentID || !time.Now().Before(current.Enrollment.ExpiresAt) {
			return errors.New("enrollment changed during preparation")
		}
		current.Binding = b
		return nil
	})
	if err != nil {
		return fail(err)
	}
	return b.BindingID, b.Challenge, b.FormalTailcatAddr, b.PreparedExpiresAt, nil
}

func (s *TwoPhaseState) Prepare(enrollmentID, reqID, ctrlID, formalNode, formalSSHPub, formalAddr string, exp time.Time) (string, string, error) {
	bID, ch, _, _, err := s.PrepareWithStarter(enrollmentID, reqID, ctrlID, formalNode, formalSSHPub, func() (string, error) {
		return formalAddr, nil
	})
	return bID, ch, err
}

// CommitContextMessage 生成用于 Ed25519 签名的不可篡改上下文消息
func CommitContextMessage(challenge, bindingID, controllerID, agentID, enrollmentID, formalClientNode, sshPubFP, formalTCAddr string) []byte {
	return []byte(fmt.Sprintf("herdrx-commit:v1:%s:%s:%s:%s:%s:%s:%s:%s",
		challenge, bindingID, controllerID, agentID, enrollmentID, formalClientNode, sshPubFP, formalTCAddr))
}

func (s *TwoPhaseState) Commit(bindingID string, sigProof []byte, agentID string) error {
	defer s.LockLifecycle()()
	if s.store == nil {
		return errors.New("state store is not configured")
	}

	var commitTrigger bool
	_, err := s.store.Update(func(c *agent.Config) error {
		if c.Revoked || (c.SetupID != "" && c.SetupID != agentID) {
			return errors.New("agent revoked or identity mismatch")
		}
		if c.Binding.BindingID != bindingID {
			return errors.New("binding ID mismatch")
		}

		// 解析正式客户端 SSH 公钥并严格验证数字签名
		sshPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.Binding.FormalSSHPublicKey))
		if err != nil {
			return fmt.Errorf("parse formal ssh public key: %w", err)
		}
		cryptoPub, ok := sshPub.(ssh.CryptoPublicKey)
		if !ok {
			return errors.New("ssh public key does not implement CryptoPublicKey")
		}
		edPub, ok := cryptoPub.CryptoPublicKey().(ed25519.PublicKey)
		if !ok {
			return errors.New("formal ssh key must be ed25519")
		}

		sshFP := ssh.FingerprintSHA256(sshPub)
		msg := CommitContextMessage(c.Binding.Challenge, c.Binding.BindingID, c.Binding.ControllerID, agentID, c.Binding.EnrollmentID, c.Binding.FormalClientNode, sshFP, c.Binding.FormalTailcatAddr)
		digest := sha256.Sum256(msg)

		if !ed25519.Verify(edPub, digest[:], sigProof) {
			return errors.New("invalid signature proof: private key verification failed")
		}

		if c.Binding.Status == "active" {
			// 已经激活，幂等通过，不受首次 TTL 限制
			return nil
		}

		if c.Binding.Status != "prepared" {
			return fmt.Errorf("cannot commit binding in status %s", c.Binding.Status)
		}

		if time.Now().After(c.Binding.PreparedExpiresAt) {
			return errors.New("prepared binding has expired")
		}

		c.Binding.Status = "active"
		c.Binding.Epoch = agent.NextEpoch(c.Binding.Epoch)
		c.Paired = true
		c.Revoked = false
		// 激活成功后销毁临时 enrollment 材料
		c.Enrollment = agent.EnrollmentConfig{}

		commitTrigger = true
		return nil
	})

	if err != nil {
		return err
	}

	if commitTrigger {
		s.mu.RLock()
		cb := s.onCommitSuccess
		s.mu.RUnlock()
		if cb != nil {
			go cb()
		}
	}
	return nil
}

func (s *TwoPhaseState) Revoke() error {
	defer s.LockLifecycle()()
	if s.store == nil {
		return nil
	}
	_, err := s.store.Update(func(c *agent.Config) error {
		c.Binding.Status = "revoked"
		c.Binding.Epoch = agent.NextEpoch(c.Binding.Epoch)
		c.Paired = false
		c.Revoked = true
		c.Enrollment = agent.EnrollmentConfig{}
		c.PairTokenHash = ""
		c.PairExpiresAt = time.Time{}
		return nil
	})
	return err
}

// EphemeralTailcatServer 负责临时配对通道的 Tailcat 实例生命周期
type EphemeralTailcatServer struct {
	server            *tailcat.Server
	ctx               *EnrollmentContext
	state             *TwoPhaseState
	agentID           string
	hostSigner        ssh.Signer
	stopChan          chan struct{}
	StartFormalServer func(formalNode key.NodePublic) (string, error)
	closed            bool
	mu                sync.Mutex
	activeConns       map[net.Conn]struct{}
}

func StartEphemeralPairingServer(ctx *EnrollmentContext, state *TwoPhaseState, agentID string, hostSigner ssh.Signer, region *tailcfg.DERPRegion, starters ...func(key.NodePublic) (string, error)) (*EphemeralTailcatServer, error) {
	s := &EphemeralTailcatServer{
		ctx:         ctx,
		state:       state,
		agentID:     agentID,
		hostSigner:  hostSigner,
		stopChan:    make(chan struct{}),
		activeConns: make(map[net.Conn]struct{}),
	}

	allowed := ctx.AllowedClient
	if allowed.IsZero() && !ctx.ClientPriv.IsZero() {
		allowed = ctx.ClientPriv.Public()
	}
	if allowed.IsZero() || ctx.EphemeralKey.IsZero() || ctx.EphemeralPSK.IsZero() {
		return nil, errors.New("incomplete ephemeral identity")
	}
	if len(starters) > 0 {
		s.StartFormalServer = starters[0]
	}
	tcSrv := &tailcat.Server{
		Key:            ctx.EphemeralKey,
		PresharedKey:   ctx.EphemeralPSK,
		Region:         region,
		AllowedClients: []key.NodePublic{allowed},
		ServedTCPPorts: []filter.PortRange{{First: 22, Last: 22}},
		Logf:           func(string, ...any) {},
	}

	tcSrv.OnTCP = func(port uint16) func(net.Conn) {
		if port != 22 {
			return nil
		}
		return s.handleSSH
	}

	s.server = tcSrv
	if err := tcSrv.Start(); err != nil {
		return nil, fmt.Errorf("start ephemeral tailcat server: %w", err)
	}
	s.server = tcSrv
	ctx.TailcatAddr = string(tcSrv.TailcatAddr())

	// Watch the trusted pairing deadline. Prepared recovery for the original
	// request may outlive the initial enrollment expiry; new requests cannot
	// extend it.
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				deadline := s.pairingDeadline()
				if deadline.IsZero() || !time.Now().Before(deadline) {
					_ = s.Close()
					return
				}
			case <-s.stopChan:
				return
			}
		}
	}()

	return s, nil
}

func (s *EphemeralTailcatServer) pairingDeadline() time.Time {
	deadline := s.ctx.ExpiresAt
	if s.state == nil {
		return deadline
	}
	snap, err := s.state.Snapshot()
	if err != nil {
		return deadline
	}
	if snap.Revoked || snap.Status == "active" || snap.Status == "revoked" {
		return time.Time{}
	}
	if snap.Status == "prepared" && snap.EnrollmentID == s.ctx.EnrollmentID && snap.PreparedExpiresAt.After(deadline) {
		return snap.PreparedExpiresAt
	}
	return deadline
}

func (s *EphemeralTailcatServer) TailcatAddr() string {
	if s == nil || s.ctx == nil {
		return ""
	}
	return s.ctx.TailcatAddr
}

func (s *EphemeralTailcatServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stopChan)
	conns := make([]net.Conn, 0, len(s.activeConns))
	for c := range s.activeConns {
		conns = append(conns, c)
	}
	s.activeConns = make(map[net.Conn]struct{})
	s.mu.Unlock()

	for _, c := range conns {
		_ = c.Close()
	}

	if s.server != nil {
		return s.server.Close()
	}
	return nil
}

func (s *EphemeralTailcatServer) handleSSH(netConn net.Conn) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = netConn.Close()
		return
	}
	s.activeConns[netConn] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.activeConns, netConn)
		s.mu.Unlock()
		_ = netConn.Close()
	}()

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			deadline := s.pairingDeadline()
			if deadline.IsZero() || time.Now().After(deadline) {
				return nil, errors.New("ephemeral pairing endpoint expired")
			}
			if subtle.ConstantTimeCompare(pass, []byte(s.ctx.PairSecret)) != 1 {
				return nil, errors.New("invalid pair secret")
			}
			return nil, nil
		},
		MaxAuthTries: 3,
	}
	cfg.AddHostKey(s.hostSigner)

	_ = netConn.SetDeadline(time.Now().Add(10 * time.Second))
	srvConn, chans, reqs, err := ssh.NewServerConn(netConn, cfg)
	if err != nil {
		return
	}
	_ = netConn.SetDeadline(time.Time{})
	defer srvConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		switch newChan.ChannelType() {
		case "direct-streamlocal@openssh.com":
			_ = newChan.Reject(ssh.Prohibited, "ephemeral pairing endpoint strictly prohibits socket forwarding")
		case "session":
			go s.handleSession(newChan)
		default:
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel on ephemeral endpoint")
		}
	}
}

func (s *EphemeralTailcatServer) handleSession(newChan ssh.NewChannel) {
	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}
	defer ch.Close()

	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if len(req.Payload) > 8192 {
			_ = req.Reply(false, nil)
			return
		}
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			continue
		}

		cmd := strings.TrimSpace(payload.Command)
		fields := strings.Fields(cmd)
		if len(fields) == 0 {
			_ = req.Reply(false, nil)
			return
		}

		switch fields[0] {
		case "pairing-prepare":
			_ = req.Reply(true, nil)
			s.handlePrepare(ch, fields[1:])
			return
		case "pairing-status":
			_ = req.Reply(true, nil)
			if snap, err := s.state.Snapshot(); err == nil {
				_, _ = io.WriteString(ch, bindingStatusLine(snap, s.agentID))
			} else {
				_, _ = io.WriteString(ch, fmt.Sprintf("status=%s enrollment=%s expires_at=%s\n",
					s.state.Status(), s.ctx.EnrollmentID, s.ctx.ExpiresAt.Format(time.RFC3339)))
			}
			sendExitStatus(ch, 0)
			return
		default:
			_ = req.Reply(false, nil)
			_, _ = io.WriteString(ch.Stderr(), fmt.Sprintf("command %q prohibited on ephemeral pairing endpoint\n", fields[0]))
			sendExitStatus(ch, 126)
			return
		}
	}
}

func (s *EphemeralTailcatServer) handlePrepare(ch ssh.Channel, args []string) {
	if len(args) != 4 {
		_, _ = io.WriteString(ch.Stderr(), "usage: pairing-prepare <request_id> <controller_id> <formal_client_node> <formal_ssh_pub_hex>\n")
		sendExitStatus(ch, 1)
		return
	}
	sshPubBytes, err := hex.DecodeString(args[3])
	if err != nil {
		_, _ = io.WriteString(ch.Stderr(), fmt.Sprintf("decode ssh pub hex: %v\n", err))
		sendExitStatus(ch, 1)
		return
	}

	reqID := args[0]
	ctrlID := args[1]
	formalNode := args[2]
	formalSSHPub := string(sshPubBytes)

	if s.StartFormalServer == nil {
		_, _ = io.WriteString(ch.Stderr(), "production error: StartFormalServer handler is not configured\n")
		sendExitStatus(ch, 1)
		return
	}

	var fNode key.NodePublic
	if err := fNode.UnmarshalText([]byte(formalNode)); err != nil {
		_, _ = io.WriteString(ch.Stderr(), fmt.Sprintf("invalid formal node key: %v\n", err))
		sendExitStatus(ch, 1)
		return
	}

	bindingID, challenge, formalAddr, exp, err := s.state.PrepareWithStarter(
		s.ctx.EnrollmentID,
		reqID,
		ctrlID,
		formalNode,
		formalSSHPub,
		func() (string, error) {
			return s.StartFormalServer(fNode)
		},
	)
	if err != nil {
		_, _ = io.WriteString(ch.Stderr(), fmt.Sprintf("prepare failed: %v\n", err))
		sendExitStatus(ch, 1)
		return
	}

	hostSignerPub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.hostSigner.PublicKey())))
	_, _ = io.WriteString(ch, fmt.Sprintf("status=prepared binding_id=%s challenge=%s formal_addr=%s agent_ssh_host_key=%s prepared_expires_at=%d\n",
		bindingID, challenge, formalAddr, hostSignerPub, exp.Unix()))
	sendExitStatus(ch, 0)
}

// PermanentTailcatServer 负责正式绑定通道的 Tailcat 实例生命周期，接入真实受限 Herdr 终端与转发
type PermanentTailcatServer struct {
	server      *tailcat.Server
	nodeKey     key.NodePrivate
	psk         tailcat.PresharedKey
	state       *TwoPhaseState
	agentID     string
	hostSigner  ssh.Signer
	herdrBin    string
	formalAddr  string
	closed      bool
	mu          sync.Mutex
	activeConns map[net.Conn]struct{}
}

func StartPermanentServer(nodeKey key.NodePrivate, psk tailcat.PresharedKey, state *TwoPhaseState, agentID string, hostSigner ssh.Signer, herdrBin string, allowedNode key.NodePublic, region *tailcfg.DERPRegion) (*PermanentTailcatServer, error) {
	s := &PermanentTailcatServer{
		nodeKey:     nodeKey,
		psk:         psk,
		state:       state,
		agentID:     agentID,
		hostSigner:  hostSigner,
		herdrBin:    herdrBin,
		activeConns: make(map[net.Conn]struct{}),
	}

	tcSrv := &tailcat.Server{
		Key:            nodeKey,
		PresharedKey:   psk,
		Region:         region,
		AllowedClients: []key.NodePublic{allowedNode},
		ServedTCPPorts: []filter.PortRange{{First: 22, Last: 22}},
		Logf:           func(string, ...any) {},
	}

	tcSrv.OnTCP = func(port uint16) func(net.Conn) {
		if port != 22 {
			return nil
		}
		return s.handleSSH
	}

	s.server = tcSrv
	if err := tcSrv.Start(); err != nil {
		return nil, fmt.Errorf("start permanent tailcat server: %w", err)
	}
	s.server = tcSrv
	s.formalAddr = string(tcSrv.TailcatAddr())
	return s, nil
}

func (s *PermanentTailcatServer) TailcatAddr() string {
	return s.formalAddr
}

func (s *PermanentTailcatServer) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *PermanentTailcatServer) Close() error {
	return s.close(nil)
}

// CloseWithDrain closes access immediately, then keeps the userspace network
// stack alive until TCP shutdown is acknowledged or the caller's budget ends.
// Without draining, closing Tailcat can discard FIN packets already queued by
// net.Conn.Close and leave an idle controller waiting indefinitely for EOF.
func (s *PermanentTailcatServer) CloseWithDrain(ctx context.Context) error {
	return s.close(ctx)
}

func (s *PermanentTailcatServer) close(drainCtx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := make([]net.Conn, 0, len(s.activeConns))
	for c := range s.activeConns {
		conns = append(conns, c)
	}
	s.activeConns = make(map[net.Conn]struct{})
	s.mu.Unlock()

	for _, c := range conns {
		_ = c.Close()
	}

	if s.server != nil {
		if drainCtx != nil {
			// Active close can remain in TCP TIME-WAIT, and an offline peer
			// cannot acknowledge shutdown. Neither condition delays revocation
			// beyond the caller's deadline; all access connections are closed.
			_ = s.server.DrainTCP(drainCtx)
		}
		return s.server.Close()
	}
	return nil
}

func (s *PermanentTailcatServer) handleSSH(netConn net.Conn) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = netConn.Close()
		return
	}
	s.activeConns[netConn] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.activeConns, netConn)
		s.mu.Unlock()
		_ = netConn.Close()
	}()

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			snap, err := s.state.Snapshot()
			if err != nil {
				return nil, fmt.Errorf("read state snapshot: %w", err)
			}
			if snap.Revoked || snap.Status == "revoked" {
				return nil, errors.New("agent has been revoked")
			}

			// 必须严格校验客户端 SSH 公钥是否匹配 binding 记录！
			if snap.FormalSSHPublicKey == "" {
				return nil, errors.New("no formal public key registered for binding")
			}
			expectedPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(snap.FormalSSHPublicKey))
			if err != nil || !agent.KeyEqual(expectedPub, key) {
				return nil, fmt.Errorf("SSH public key is not authorized for this formal binding")
			}

			// 角色固定于认证时刻！
			role := "prepared"
			if snap.Status == "active" && snap.Paired {
				role = "active"
			} else if snap.Status == "prepared" {
				if time.Now().After(snap.PreparedExpiresAt) {
					return nil, errors.New("prepared binding has expired")
				}
				role = "prepared"
			} else {
				return nil, fmt.Errorf("binding is in status %s, authentication refused", snap.Status)
			}

			return &ssh.Permissions{
				Extensions: map[string]string{
					"role":  role,
					"epoch": fmt.Sprintf("%d", snap.Epoch),
				},
			}, nil
		},
		MaxAuthTries: 3,
	}
	cfg.AddHostKey(s.hostSigner)

	_ = netConn.SetDeadline(time.Now().Add(10 * time.Second))
	srvConn, chans, reqs, err := ssh.NewServerConn(netConn, cfg)
	if err != nil {
		return
	}
	_ = netConn.SetDeadline(time.Time{})
	defer srvConn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srvConn.Wait(); cancel() }()
	go func() {
		for req := range reqs {
			snap, err := s.state.Snapshot()
			ok := err == nil && !snap.Revoked && snap.Paired && snap.Status == "active" && srvConn.Permissions != nil && srvConn.Permissions.Extensions["role"] == "active" && srvConn.Permissions.Extensions["epoch"] == fmt.Sprint(snap.Epoch)
			_ = req.Reply(ok && req.Type == "keepalive@herdrx", nil)
		}
	}()

	isActAtAuth := (srvConn.Permissions != nil && srvConn.Permissions.Extensions["role"] == "active")
	authEpoch := ""
	if srvConn.Permissions != nil {
		authEpoch = srvConn.Permissions.Extensions["epoch"]
	}

	for newChan := range chans {
		switch newChan.ChannelType() {
		case "direct-streamlocal@openssh.com":
			curSnap, _ := s.state.Snapshot()
			// 仅当认证时刻角色为 active 且当前未被 revoke/未变更为新 epoch 时放行真实 Herdr socket 转发
			if !isActAtAuth || curSnap.Status != "active" || !curSnap.Paired || curSnap.Revoked || fmt.Sprintf("%d", curSnap.Epoch) != authEpoch {
				_ = newChan.Reject(ssh.Prohibited, "socket forwarding requires active binding matching auth epoch")
				continue
			}
			go s.handleStreamLocal(ctx, newChan)
		case "session":
			go s.handleSession(ctx, newChan, isActAtAuth, authEpoch)
		default:
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel")
		}
	}
}

func (s *PermanentTailcatServer) handleStreamLocal(ctx context.Context, newChan ssh.NewChannel) {
	var payload struct {
		SocketPath string
		Reserved0  string
		Reserved1  uint32
	}
	if ssh.Unmarshal(newChan.ExtraData(), &payload) != nil || !agent.AllowedSocket(payload.SocketPath) {
		_ = newChan.Reject(ssh.Prohibited, "socket path is not allowed")
		return
	}
	// 真实 net.Dial 到受控端的 Herdr socket，彻底移除 echo 回环！
	local, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", payload.SocketPath)
	if err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, "Herdr socket is unavailable")
		return
	}
	ch, reqs, err := newChan.Accept()
	if err != nil {
		_ = local.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	defer ch.Close()
	defer local.Close()
	stop := context.AfterFunc(ctx, func() { _ = local.Close(); _ = ch.Close() })
	defer stop()

	done := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(ch, local)
		_ = ch.CloseWrite()
		done <- struct{}{}
	}()
	_, _ = io.Copy(local, ch)
	if closer, ok := local.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
	}
	_ = local.Close()
	<-done
}

func (s *PermanentTailcatServer) handleSession(ctx context.Context, newChan ssh.NewChannel, isActAtAuth bool, authEpoch string) {
	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}
	defer ch.Close()

	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if len(req.Payload) > 8192 {
			_ = req.Reply(false, nil)
			return
		}
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			continue
		}

		cmd := strings.TrimSpace(payload.Command)
		fields := strings.Fields(cmd)
		if len(fields) == 0 {
			_ = req.Reply(false, nil)
			return
		}

		// 处于 prepared 认证时刻：仅允许 pairing-commit 与 pairing-status，绝不原地升权！
		if !isActAtAuth {
			if fields[0] == "pairing-commit" && len(fields) >= 3 {
				_ = req.Reply(true, nil)
				sigProof, hexErr := hex.DecodeString(fields[2])
				if hexErr != nil {
					_, _ = io.WriteString(ch.Stderr(), "invalid signature hex\n")
					sendExitStatus(ch, 1)
					return
				}
				cErr := s.state.Commit(fields[1], sigProof, s.agentID)
				if cErr != nil {
					_, _ = io.WriteString(ch.Stderr(), fmt.Sprintf("commit failed: %v\n", cErr))
					sendExitStatus(ch, 1)
					return
				}
				snap, _ := s.state.Snapshot()
				_, _ = io.WriteString(ch, fmt.Sprintf("status=active binding_id=%s epoch=%d\n", fields[1], snap.Epoch))
				sendExitStatus(ch, 0)
				return
			}
			if fields[0] == "pairing-status" {
				_ = req.Reply(true, nil)
				snap, _ := s.state.Snapshot()
				_, _ = io.WriteString(ch, bindingStatusLine(snap, s.agentID))
				sendExitStatus(ch, 0)
				return
			}
			// 其余任何终端命令在 prepared 认证时刻均被严格拒绝（exit 126）
			_ = req.Reply(false, nil)
			_, _ = io.WriteString(ch.Stderr(), "command prohibited before binding is active\n")
			sendExitStatus(ch, 126)
			return
		}

		// 处于 active 认证时刻：校验当前全局状态与 epoch 依然合法
		curSnap, _ := s.state.Snapshot()
		if curSnap.Status != "active" || !curSnap.Paired || curSnap.Revoked || fmt.Sprintf("%d", curSnap.Epoch) != authEpoch {
			_ = req.Reply(false, nil)
			_, _ = io.WriteString(ch.Stderr(), "connection epoch expired or agent revoked\n")
			sendExitStatus(ch, 126)
			return
		}

		// active 状态下对 pairing-status 与 pairing-commit 支持幂等重试
		if fields[0] == "pairing-status" {
			_ = req.Reply(true, nil)
			_, _ = io.WriteString(ch, bindingStatusLine(curSnap, s.agentID))
			sendExitStatus(ch, 0)
			return
		}
		if fields[0] == "pairing-commit" && len(fields) >= 3 {
			_ = req.Reply(true, nil)
			sigProof, hexErr := hex.DecodeString(fields[2])
			if hexErr != nil {
				_, _ = io.WriteString(ch.Stderr(), "invalid signature hex\n")
				sendExitStatus(ch, 1)
				return
			}
			if err := s.state.Commit(fields[1], sigProof, s.agentID); err != nil {
				_, _ = io.WriteString(ch.Stderr(), fmt.Sprintf("commit failed: %v\n", err))
				sendExitStatus(ch, 1)
				return
			}
			_, _ = io.WriteString(ch, fmt.Sprintf("status=active binding_id=%s epoch=%d\n", fields[1], curSnap.Epoch))
			sendExitStatus(ch, 0)
			return
		}

		// 处于 active 阶段：白名单校验，真实调用受控端命令与 HerdrBin 绝对路径，彻底移除 fake 模拟！
		spec, err := agent.ParseCommand(cmd)
		if err != nil {
			_ = req.Reply(false, nil)
			_, _ = io.WriteString(ch.Stderr(), err.Error()+"\n")
			sendExitStatus(ch, 126)
			return
		}

		_ = req.Reply(true, nil)
		status := uint32(0)
		switch spec.Internal {
		case "config-path":
			_, _ = io.WriteString(ch, agent.ConfigPathOutput())
		case "uname":
			_, _ = fmt.Fprintf(ch, "%s %s\n", runtime.GOOS, runtime.GOARCH)
		case "terminal-geometry":
			if err := agent.WriteTerminalGeometry(ch, spec.Args[0]); err != nil {
				_, _ = io.WriteString(ch.Stderr(), err.Error()+"\n")
				status = 1
			}
		case "stage-image":
			ext := spec.Args[0]
			remotePath, sErr := agent.StageImageFromReader(ch, ext)
			if sErr != nil {
				_, _ = io.WriteString(ch.Stderr(), sErr.Error()+"\n")
				status = 1
			} else {
				_, _ = io.WriteString(ch, remotePath+"\n")
			}
		default:
			// 真实执行配置中指定的 HerdrBin 绝对路径！
			base := agent.ExecutableCommand(spec, s.herdrBin)
			command := exec.CommandContext(ctx, base.Path, base.Args[1:]...)
			command.WaitDelay = 2 * time.Second
			command.Stdin = ch
			command.Stdout = ch
			command.Stderr = ch.Stderr()
			if rErr := command.Run(); rErr != nil {
				status = 1
			}
		}
		sendExitStatus(ch, status)
		return
	}
}

func sendExitStatus(channel ssh.Channel, status uint32) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, status)
	_, _ = channel.SendRequest("exit-status", false, payload)
}

// Recovery responses carry the immutable binding context; an unqualified "active"
// is insufficient for a controller to finalize a pending enrollment.
func bindingStatusLine(s BindingSnapshot, agentID string) string {
	return fmt.Sprintf("status=%s binding_id=%s agent_id=%s controller_id=%s request_id=%s enrollment_id=%s epoch=%d prepared_expires_at=%d\n", s.Status, s.BindingID, agentID, s.ControllerID, s.RequestID, s.EnrollmentID, s.Epoch, s.PreparedExpiresAt.Unix())
}
