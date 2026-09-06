package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
)

// SSRFValidator 负责对外部输入的 DERP 主机与地址进行出站安全过滤
type SSRFValidator struct {
	mu             sync.RWMutex
	allowedPrivate map[netip.AddrPort]bool
	lookupIP       func(context.Context, string) ([]netip.Addr, error)
}

var DefaultSSRFValidator = NewSSRFValidator()

func NewSSRFValidator() *SSRFValidator {
	return &SSRFValidator{
		allowedPrivate: make(map[netip.AddrPort]bool),
	}
}

// AllowPrivateEndpoint 在受限测试或私有网络部署中显式放行受信任的端点（如测试自建本地 DERP）
func (v *SSRFValidator) AllowPrivateEndpoint(ap netip.AddrPort) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.allowedPrivate[ap] = true
}

func (v *SSRFValidator) ClearAllowed() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.allowedPrivate = make(map[netip.AddrPort]bool)
}

// ValidateTarget 校验主机名与端口是否合法且不触发 SSRF 攻击
func (v *SSRFValidator) ValidateTarget(host string, port int) error {
	_, err := v.resolveTarget(host, port)
	return err
}
func (v *SSRFValidator) resolveTarget(host string, port int) ([]netip.Addr, error) {
	if host == "" || host != strings.TrimSpace(host) || len(host) > 253 {
		return nil, errors.New("invalid host")
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d", port)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return nil, errors.New("scoped IP addresses are not allowed")
		}
		ip = ip.Unmap()
		if err := v.validateIP(ip, uint16(port)); err != nil {
			return nil, err
		}
		return []netip.Addr{ip}, nil
	}
	if strings.ContainsAny(host, " \t\r\n/\\@:?#") {
		return nil, errors.New("host contains forbidden characters")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lookup := v.lookupIP
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("dns lookup failed for %s: %w", host, err)
	}
	if len(ips) == 0 || len(ips) > 64 {
		return nil, errors.New("DNS returned an invalid number of addresses")
	}
	for i, ip := range ips {
		ip = ip.Unmap()
		if err := v.validateIP(ip, uint16(port)); err != nil {
			return nil, err
		}
		ips[i] = ip
	}
	return ips, nil
}

func (v *SSRFValidator) validateIP(ip netip.Addr, port uint16) error {
	v.mu.RLock()
	ap := netip.AddrPortFrom(ip, port)
	allowed := v.allowedPrivate[ap]
	v.mu.RUnlock()

	if allowed {
		return nil
	}

	// 严格拦截回环地址 (127.0.0.0/8, ::1)
	if !ip.IsValid() || ip.Zone() != "" {
		return errors.New("invalid or scoped IP address")
	}
	if ip.IsLoopback() {
		return fmt.Errorf("refusing connection to loopback address: %s", ip)
	}

	// 严格拦截链路本地地址与云元数据地址 (169.254.0.0/16, fe80::/10, 169.254.169.254)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("refusing connection to link-local address: %s", ip)
	}
	if ip.String() == "169.254.169.254" {
		return errors.New("refusing connection to cloud metadata endpoint")
	}

	// 严格拦截未指定地址 (0.0.0.0, ::)
	if ip.IsUnspecified() {
		return fmt.Errorf("refusing connection to unspecified address: %s", ip)
	}

	// 严格拦截组播与私有私网 (RFC 1918: 10/8, 172.16/12, 192.168/16)，除非在显式 allowlist 中
	if ip.IsMulticast() {
		return fmt.Errorf("refusing connection to multicast address: %s", ip)
	}
	if ip.IsPrivate() {
		return fmt.Errorf("refusing connection to unapproved private network address: %s (must be explicitly whitelisted by administrator)", ip)
	}

	return nil
}

func ProxyEnvSet() bool {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

// ValidateDialAddr checks every DERP/STUN node and IP that Tailcat will actually
// use, pins unused address families to "none", and returns a dial address.
// The raw input is left unchanged for signatures.
func (v *SSRFValidator) ValidateDialAddr(raw string) (string, error) {
	if len(raw) > MaxConnectionStringLen {
		return "", errors.New("tailcat address exceeds 16 KiB")
	}
	if ProxyEnvSet() {
		return "", errors.New("HTTP/HTTPS/ALL_PROXY is not supported for Tailcat dials because CONNECT would re-resolve HostName; unset the proxy environment")
	}
	ci, err := tailcat.ParseAddr(tailcat.Addr(raw))
	if err != nil {
		return "", fmt.Errorf("invalid tailcat address: %w", err)
	}
	if len(ci.Region) != 1 {
		return "", errors.New("tailcat address must embed a full DERP region; short RegionID-only addresses are not expanded")
	}
	for i, region := range ci.Region {
		if region == nil {
			return "", fmt.Errorf("region %d is null", i)
		}
		if len(region.Nodes) == 0 || len(region.Nodes) > 8 {
			return "", fmt.Errorf("region %d has no nodes", i)
		}
		for j, node := range region.Nodes {
			if node == nil {
				return "", fmt.Errorf("region %d node %d is null", i, j)
			}
			if err := v.validateDERPNode(node); err != nil {
				return "", err
			}
		}
	}
	return string(ci.Addr()), nil
}

func (v *SSRFValidator) validateDERPNode(node *tailcfg.DERPNode) error {
	derpPort := node.DERPPort
	if derpPort == 0 {
		derpPort = 443
	}
	ips, err := v.resolveTarget(node.HostName, derpPort)
	if err != nil {
		return fmt.Errorf("DERP host: %w", err)
	}
	if node.STUNPort < -1 || node.STUNPort > 65535 {
		return errors.New("invalid STUN port")
	}
	if len(node.CertName) > 253 || strings.ContainsAny(node.CertName, " \t\r\n/\\@?#") {
		return errors.New("invalid DERP certificate name")
	}
	for _, family := range []struct {
		target *string
		ipv4   bool
	}{{&node.IPv4, true}, {&node.IPv6, false}} {
		if *family.target == "" {
			*family.target = "none"
			for _, ip := range ips {
				if ip.Is4() == family.ipv4 {
					*family.target = ip.String()
					break
				}
			}
		}
		if *family.target == "none" {
			continue
		}
		ip, err := netip.ParseAddr(*family.target)
		if err != nil || ip.Is4() != family.ipv4 || ip.Zone() != "" {
			return errors.New("DERP address family must contain a literal matching IP or none")
		}
		if err := v.validateIP(ip.Unmap(), uint16(derpPort)); err != nil {
			return err
		}
		// Both TLS and STUN must use already-validated addresses; an allowlist for
		// one TCP endpoint is not an authorization to probe another private port.
		if node.STUNPort != -1 {
			port := node.STUNPort
			if port == 0 {
				port = 3478
			}
			if err := v.validateIP(ip.Unmap(), uint16(port)); err != nil {
				return err
			}
		}
	}
	if node.IPv4 == "none" && node.IPv6 == "none" {
		return errors.New("DERP node has no usable address family")
	}
	if node.STUNTestIP != "" {
		ip, err := netip.ParseAddr(node.STUNTestIP)
		if err != nil {
			return errors.New("STUN test override must be a literal IP")
		}
		port := node.STUNPort
		if port == 0 {
			port = 3478
		}
		if port == -1 {
			return errors.New("STUN test override conflicts with disabled STUN")
		}
		if err := v.validateIP(ip.Unmap(), uint16(port)); err != nil {
			return err
		}
	}
	if node.InsecureForTests {
		ip, err := netip.ParseAddr(node.HostName)
		if err != nil {
			return errors.New("test TLS bypass requires an explicitly allowlisted literal IP")
		}
		v.mu.RLock()
		allowed := v.allowedPrivate[netip.AddrPortFrom(ip.Unmap(), uint16(derpPort))]
		v.mu.RUnlock()
		if !allowed {
			return errors.New("test TLS bypass requires an explicitly allowlisted endpoint")
		}
	}
	// Prevent captive-portal probes to an unrelated port 80.
	node.CanPort80 = false
	return nil
}
