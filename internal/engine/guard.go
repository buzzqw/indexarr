package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ErrBlockedAddress is returned when a URL, redirect target or resolved IP is
// refused by the SSRF guard.
var ErrBlockedAddress = errors.New("engine: address blocked by SSRF guard")

const dnsLookupTimeout = 5 * time.Second

// Guard rejects requests to local, private and otherwise non-public targets.
// The zero value is usable and blocks private addresses. A custom Resolver is
// mainly useful in tests and for hosts with split DNS.
type Guard struct {
	AllowPrivate bool
	Resolver     *net.Resolver
}

// CheckURL validates the scheme and every address a host resolves to. Only
// http and https are accepted; when AllowPrivate is set the IP checks are
// skipped but the scheme check still applies.
func (g *Guard) CheckURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: nil URL", ErrBlockedAddress)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrBlockedAddress, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrBlockedAddress)
	}
	if ip := parseHostIP(host); ip != nil {
		return g.CheckIP(ip)
	}

	ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
	defer cancel()
	addresses, err := g.resolver().LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("engine: resolve %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("%w: %q resolves to no addresses", ErrBlockedAddress, host)
	}
	for _, address := range addresses {
		if err := g.CheckIP(address.IP); err != nil {
			return fmt.Errorf("engine: host %q: %w", host, err)
		}
	}
	return nil
}

// CheckIP rejects an individual IP. IPv4-mapped IPv6 addresses are unmapped
// first so ::ffff:127.0.0.1 is treated exactly like 127.0.0.1.
func (g *Guard) CheckIP(ip net.IP) error {
	if len(ip) == 0 {
		return fmt.Errorf("%w: invalid IP", ErrBlockedAddress)
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if g.allowPrivate() {
		return nil
	}
	if blockedIP(ip) {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
	}
	return nil
}

// DialContext returns a dialer for http.Transport that resolves the host
// itself, validates every resolved address and then dials one of those literal
// IPs. The HTTP request keeps the original Host header and TLS SNI, so a
// second DNS answer cannot swap a vetted address for an internal one
// (DNS rebinding).
//
// With AllowPrivate set the guard relies on CheckURL for the scheme and dials
// normally, preserving local hostname resolution for opt-in local indexers.
func (g *Guard) DialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("engine: dial address %q: %w", address, err)
		}
		if g.allowPrivate() {
			return dialer.DialContext(ctx, network, address)
		}

		var candidates []net.IP
		if ip := parseHostIP(host); ip != nil {
			if err := g.CheckIP(ip); err != nil {
				return nil, err
			}
			candidates = []net.IP{ip}
		} else {
			addresses, err := g.resolver().LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("engine: resolve %q: %w", host, err)
			}
			if len(addresses) == 0 {
				return nil, fmt.Errorf("%w: %q resolves to no addresses", ErrBlockedAddress, host)
			}
			for _, resolved := range addresses {
				if err := g.CheckIP(resolved.IP); err != nil {
					return nil, fmt.Errorf("engine: host %q: %w", host, err)
				}
				candidates = append(candidates, resolved.IP)
			}
		}

		var lastErr error
		for _, ip := range candidates {
			if !networkMatches(network, ip) {
				continue
			}
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, fmt.Errorf("engine: dial %q: %w", address, lastErr)
		}
		return nil, fmt.Errorf("engine: no address for %q matches network %s", address, network)
	}
}

func (g *Guard) allowPrivate() bool {
	return g != nil && g.AllowPrivate
}

func (g *Guard) resolver() *net.Resolver {
	if g != nil && g.Resolver != nil {
		return g.Resolver
	}
	return net.DefaultResolver
}

func parseHostIP(host string) net.IP {
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	return net.ParseIP(host)
}

func blockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127: // 100.64.0.0/10, CGNAT
			return true
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0: // 192.0.0.0/24
			return true
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19): // 198.18.0.0/15
			return true
		case v4[0] >= 240: // 240.0.0.0/4, including the broadcast address
			return true
		}
	}
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc { // fc00::/7, IPv6 ULA
		return true
	}
	return false
}

func networkMatches(network string, ip net.IP) bool {
	switch network {
	case "tcp4":
		return ip.To4() != nil
	case "tcp6":
		return ip.To4() == nil
	default:
		return true
	}
}
