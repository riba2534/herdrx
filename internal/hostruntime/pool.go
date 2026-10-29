package hostruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

// ConnectionError carries an action and retry policy across HTTP and WebSocket.
type ConnectionError struct {
	Code       string
	RetryAfter time.Duration
	Permanent  bool
	Err        error
}

func (e *ConnectionError) Error() string { return e.Err.Error() }
func (e *ConnectionError) Unwrap() error { return e.Err }

func DescribeError(err error) *ConnectionError {
	var connection *ConnectionError
	if errors.As(err, &connection) {
		return connection
	}
	var unknown *herdr.UnknownHostKeyError
	if errors.As(err, &unknown) {
		return &ConnectionError{Code: "host_key_unknown", Permanent: true, Err: fmt.Errorf("请返回主机列表，核对并信任 SSH 主机指纹：%w", err)}
	}
	message := strings.ToLower(err.Error())
	for _, token := range []string{"unsupported herdr protocol", "incompatible herdr protocol", "unsupported protocol version"} {
		if strings.Contains(message, token) {
			return &ConnectionError{Code: "host_protocol_incompatible", Permanent: true, Err: fmt.Errorf("协议版本不兼容，请检查 Herdr 与 herdrx CLI 版本后重新连接：%w", err)}
		}
	}
	if strings.Contains(message, "ssh host key changed") {
		return &ConnectionError{Code: "host_key_changed", Permanent: true, Err: fmt.Errorf("%s：%w", hostKeyChangedNext, err)}
	}
	// System OpenSSH reports a changed key only in its stderr. Check it before
	// the generic "host key verification failed" also printed for unknown keys.
	for _, token := range []string{"remote host identification has changed", "has changed and you have requested strict checking", "possible dns spoofing"} {
		if strings.Contains(message, token) {
			return &ConnectionError{Code: "host_key_changed", Permanent: true, Err: fmt.Errorf("%s：%w", hostKeyChangedNext, err)}
		}
	}
	for _, token := range []string{"unable to authenticate", "no supported methods remain", "parse ssh private key", "invalid saved ssh key", "unsupported ssh authentication", "ssh password is empty"} {
		if strings.Contains(message, token) {
			return &ConnectionError{Code: "host_auth_failed", Permanent: true, Err: fmt.Errorf("SSH 认证失败，请检查用户名和密钥或密码，再重新连接：%w", err)}
		}
	}
	// The causes below are usually fixed outside the website, so they stay
	// retryable: a permanent failure would be cached until the host is edited.
	if HerdrSocketUnavailable(err) {
		return &ConnectionError{Code: "herdr_not_running", Err: fmt.Errorf("远程主机上的 Herdr 没有运行，或会话名与主机配置不一致。请在远程主机上启动 Herdr 后重新连接：%w", err)}
	}
	for _, cause := range offlineCauses {
		if slices.ContainsFunc(cause.tokens, func(token string) bool { return strings.Contains(message, token) }) {
			return &ConnectionError{Code: cause.code, Err: fmt.Errorf("%s：%w", cause.next, err)}
		}
	}
	return &ConnectionError{Code: "host_unavailable", Err: err}
}

const hostKeyChangedNext = "远程主机指纹已变化，可能存在中间人攻击。请先通过其他可信渠道核对远程主机身份，确认无误后再更新指纹并重新连接"

// offlineCauses match lowercase SSH and dial errors, including the stderr
// tail OpenSSH reports, and say what the user can do next.
var offlineCauses = []struct {
	tokens     []string
	code, next string
}{
	{[]string{"permission denied ("}, "host_auth_failed", "SSH 认证被远程主机拒绝，请检查用户名、密钥或 Kerberos 票据后重新连接"},
	{[]string{"host key verification failed"}, "host_key_unverified", "工作台主机的 known_hosts 中没有这台远程主机的指纹，请管理员用工作台服务账号核对并添加后重新连接"},
	{[]string{"could not resolve hostname", "no such host"}, "host_unreachable", "无法解析远程主机地址，请检查主机地址拼写和 DNS 后重新连接"},
	{[]string{"connection refused", "no route to host", "network is unreachable", "connection timed out", "i/o timeout"}, "host_unreachable", "连不上远程主机的 SSH 端口，请确认远程主机已开机、网络可达且 SSH 服务在运行后重新连接"},
}

// HerdrSocketUnavailable reports a reachable host whose Herdr API socket is
// missing: Herdr is not running there, or the session name does not match.
func HerdrSocketUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "herdr socket is unavailable") {
		return true
	}
	if strings.Contains(message, "connect herdr api socket") {
		return strings.Contains(message, "no such file") || strings.Contains(message, "connection refused")
	}
	if strings.Contains(message, "forward remote herdr socket") {
		return strings.Contains(message, "connect failed") || strings.Contains(message, "open failed")
	}
	return false
}

func capacityError() error {
	return &ConnectionError{Code: "host_capacity", RetryAfter: 5 * time.Second, Err: errors.New("主机连接已达到实例上限，请关闭暂不用的工作台后重试，或联系管理员调整连接上限")}
}

type dialFlight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	waiters  int
	finished bool
	entry    *sharedEndpoint
	err      error
}
type dialFailure struct {
	err      *ConnectionError
	attempts int
	until    time.Time
	touched  time.Time
}

// openSharedLocked always releases mu. A flight owns one dial independent of
// individual observers; only the last departing observer cancels that dial.
func (f *Factory) openSharedLocked(ctx context.Context, host store.Host) (herdr.Endpoint, error) {
	if f.endpoints == nil {
		f.endpoints = make(map[string]*sharedEndpoint)
	}
	if f.flights == nil {
		f.flights = make(map[string]*dialFlight)
	}
	if f.failures == nil {
		f.failures = make(map[string]dialFailure)
	}
	if f.dialSlots == nil {
		slots := f.Config.HostDialConcurrency
		if slots <= 0 {
			slots = 4
		}
		f.dialSlots = make(chan struct{}, slots)
	}
	if entry := f.endpoints[host.ID]; entry != nil {
		entry.refs++
		if entry.idle != nil {
			entry.idle.Stop()
			entry.idle = nil
		}
		f.mu.Unlock()
		return &sharedHandle{factory: f, hostID: host.ID, entry: entry}, nil
	}
	if failure, ok := f.failures[host.ID]; ok && (failure.err.Permanent || time.Now().Before(failure.until)) {
		result := *failure.err
		result.RetryAfter = max(0, time.Until(failure.until))
		f.mu.Unlock()
		return nil, &result
	}
	if flight := f.flights[host.ID]; flight != nil {
		if flight.waiters >= 128 {
			f.mu.Unlock()
			return nil, capacityError()
		}
		flight.waiters++
		f.mu.Unlock()
		return f.awaitFlight(ctx, host.ID, flight)
	}
	limit := f.Config.MaxHostConnections
	if limit <= 0 {
		limit = 20
	}
	var evicted *sharedEndpoint
	if len(f.endpoints)+len(f.flights) >= limit {
		var oldestID string
		for id, entry := range f.endpoints {
			if entry.refs == 0 && (evicted == nil || entry.idleSince.Before(evicted.idleSince)) {
				evicted = entry
				oldestID = id
			}
		}
		if evicted == nil {
			f.mu.Unlock()
			return nil, capacityError()
		}
		delete(f.endpoints, oldestID)
		evicted.closed = true
		if evicted.idle != nil {
			evicted.idle.Stop()
		}
	}
	timeout := f.Config.HostDialTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	flight := &dialFlight{done: make(chan struct{}), cancel: cancel, waiters: 1}
	f.flights[host.ID] = flight
	f.mu.Unlock()
	go f.runDial(dialCtx, host, flight, evicted)
	return f.awaitFlight(ctx, host.ID, flight)
}

func (f *Factory) awaitFlight(ctx context.Context, hostID string, flight *dialFlight) (herdr.Endpoint, error) {
	select {
	case <-ctx.Done():
	case <-flight.done:
	}
	f.mu.Lock()
	if err := ctx.Err(); err != nil {
		flight.waiters--
		var handle *sharedHandle
		if flight.finished && flight.entry != nil {
			handle = &sharedHandle{factory: f, hostID: hostID, entry: flight.entry}
		} else if !flight.finished && flight.waiters == 0 {
			f.cancelFlightLocked(hostID, flight, context.Canceled)
		}
		f.mu.Unlock()
		if handle != nil {
			_ = handle.Close()
		}
		return nil, err
	}
	entry, err := flight.entry, flight.err
	if entry != nil && entry.closed {
		entry = nil
		err = net.ErrClosed
	}
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &sharedHandle{factory: f, hostID: hostID, entry: entry}, nil
}

func (f *Factory) cancelFlightLocked(hostID string, flight *dialFlight, err error) {
	if flight.finished {
		return
	}
	flight.cancel()
	flight.finished = true
	flight.err = err
	if f.flights[hostID] == flight {
		delete(f.flights, hostID)
	}
	close(flight.done)
}

func (f *Factory) runDial(ctx context.Context, host store.Host, flight *dialFlight, evicted *sharedEndpoint) {
	defer flight.cancel()
	// Close may wait on network I/O; neither the pool mutex nor another host's
	// dial slot is held during eviction.
	if evicted != nil {
		_ = evicted.endpoint.Close()
	}
	var endpoint herdr.Endpoint
	var err error
	select {
	case f.dialSlots <- struct{}{}:
		if err = ctx.Err(); err == nil {
			dial := f.openUncached
			if f.dial != nil {
				dial = f.dial
			}
			endpoint, err = dial(ctx, host)
		}
		<-f.dialSlots
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	f.mu.Lock()
	if flight.finished {
		f.mu.Unlock()
		if endpoint != nil {
			_ = endpoint.Close()
		}
		return
	}
	if err != nil {
		detail := DescribeError(err)
		failure := f.failures[host.ID]
		failure.attempts = min(failure.attempts+1, 5)
		failure.touched = time.Now()
		failure.until = failure.touched.Add(min(time.Duration(1<<failure.attempts)*time.Second, 30*time.Second))
		if detail.Permanent {
			failure.until = time.Time{}
		}
		copy := *detail
		if !copy.Permanent {
			copy.RetryAfter = time.Until(failure.until)
		}
		failure.err = &copy
		// Bound metadata even when hosts are continually added and removed.
		if len(f.failures) >= 1024 {
			oldestID := ""
			oldest := time.Now()
			for id, v := range f.failures {
				if v.touched.Before(oldest) {
					oldestID = id
					oldest = v.touched
				}
			}
			delete(f.failures, oldestID)
		}
		f.failures[host.ID] = failure
		flight.err = &copy
	} else {
		delete(f.failures, host.ID)
		// Reserve each observer's reference before publishing, including observers
		// canceled concurrently with completion. awaitFlight releases those refs.
		flight.entry = &sharedEndpoint{endpoint: endpoint, refs: flight.waiters}
		f.endpoints[host.ID] = flight.entry
	}
	flight.finished = true
	delete(f.flights, host.ID)
	close(flight.done)
	f.mu.Unlock()
	if err != nil && endpoint != nil {
		_ = endpoint.Close()
	}
}

type PoolStats struct {
	Connections int `json:"connections"`
	Active      int `json:"active"`
	Idle        int `json:"idle"`
	Pending     int `json:"pending"`
	Dialing     int `json:"dialing"`
	Limit       int `json:"limit"`
}

func (f *Factory) Stats() PoolStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	stats := PoolStats{Connections: len(f.endpoints), Pending: len(f.flights), Dialing: len(f.dialSlots), Limit: f.Config.MaxHostConnections}
	if stats.Limit <= 0 {
		stats.Limit = 20
	}
	for _, entry := range f.endpoints {
		if entry.refs == 0 {
			stats.Idle++
		} else {
			stats.Active++
		}
	}
	return stats
}
