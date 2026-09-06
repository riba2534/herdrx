package push

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// ValidateSubscription checks browser-supplied data before it is persisted.
// DNS is checked again by the transport on every new connection.
func ValidateSubscription(endpoint, publicKey, auth string) error {
	if err := validateEndpoint(endpoint); err != nil {
		return err
	}
	decode := func(value string) ([]byte, error) {
		return base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	}
	if len(publicKey) > 88 || len(auth) > 24 {
		return errors.New("invalid push key size")
	}
	key, err := decode(publicKey)
	if err != nil || len(key) != 65 {
		return errors.New("invalid push public key")
	}
	if _, err = ecdh.P256().NewPublicKey(key); err != nil {
		return errors.New("invalid push public key")
	}
	secret, err := decode(auth)
	if err != nil || len(secret) != 16 {
		return errors.New("invalid push authentication secret")
	}
	return nil
}

func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 4096 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") {
		return errors.New("push endpoint must be an HTTPS URL on port 443 without credentials or fragment")
	}
	host := u.Hostname()
	if strings.ContainsAny(host, "%\\ \t\r\n") || strings.HasSuffix(u.Host, ":") {
		return errors.New("invalid push endpoint host")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicPushIP(ip) {
		return errors.New("push endpoint must use a public address")
	}
	return nil
}

var reservedPushNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

func publicPushIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range reservedPushNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	// The well-known NAT64 prefix can otherwise hide an IPv4 private address.
	if netip.MustParsePrefix("64:ff9b::/96").Contains(ip) {
		raw := ip.As16()
		return publicPushIP(netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]}))
	}
	return true
}

type pushDialer struct {
	lookup func(context.Context, string, string) ([]netip.Addr, error)
	dial   func(context.Context, string, string) (net.Conn, error)
}

func (d pushDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, errors.New("invalid push dial address")
	}
	addresses, err := d.lookup(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve push endpoint: %w", err)
	}
	if len(addresses) == 0 || len(addresses) > 64 {
		return nil, errors.New("invalid push DNS result")
	}
	for _, ip := range addresses {
		if !publicPushIP(ip) {
			return nil, errors.New("push endpoint resolved to a non-public address")
		}
	}
	for _, ip := range addresses {
		// Dial a checked literal: resolving the hostname again would allow rebinding.
		connection, dialErr := d.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		err = dialErr
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("connect push endpoint: %w", err)
}

func newHTTPClient() *http.Client {
	dialer := pushDialer{lookup: net.DefaultResolver.LookupNetIP, dial: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	return &http.Client{
		Timeout: 10 * time.Second,
		// Push endpoints are supplied by users. Do not use environment proxies,
		// which would resolve destinations outside the checked dial path.
		Transport:     &http.Transport{DialContext: dialer.DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 16, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 4, IdleConnTimeout: time.Minute, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
