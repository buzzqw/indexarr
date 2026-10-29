package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	socks4Version      = 0x04
	socks4Connect      = 0x01
	socks4Granted      = 0x5a
	defaultSOCKS4Port  = "1080"
	socks4ReplySize    = 8
	socks4MaxUserIDLen = 255
)

// socks4Dialer implements a SOCKS4/SOCKS4a CONNECT tunnel. net/http only
// understands SOCKS5 through a proxy URL, so the transport installs this
// dialer as DialContext and leaves Proxy unset.
//
// SOCKS4 only carries IPv4 addresses. SOCKS4a normally lets the proxy resolve a
// hostname, but this dialer always resolves and validates the target locally
// through the Guard and sends the resulting IPv4 address. That keeps the SSRF
// protection in front of every connection and prevents the proxy from seeing a
// name that could resolve differently there.
//
// The proxy URL username is sent as the SOCKS4 USERID. There is no standard
// password field in SOCKS4, so a password from user:pass is accepted and
// ignored.
type socks4Dialer struct {
	guard        *Guard
	dialer       *net.Dialer
	proxyHost    string
	proxyPort    string
	userID       string
	proxyNetwork string
}

// newSOCKS4Dialer returns a DialContext function that connects to target
// host:port through the given SOCKS4 or SOCKS4a proxy. proxyNetwork selects the
// address family used for the connection to the proxy itself; the target is
// always IPv4 because the protocol requires it.
func newSOCKS4Dialer(guard *Guard, dialer *net.Dialer, proxyURL *url.URL, proxyNetwork string) func(context.Context, string, string) (net.Conn, error) {
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	d := &socks4Dialer{
		guard:        guard,
		dialer:       dialer,
		proxyNetwork: proxyNetwork,
	}
	if proxyNetwork == "" {
		d.proxyNetwork = "tcp"
	}
	if proxyURL != nil {
		d.proxyHost = proxyURL.Hostname()
		d.proxyPort = proxyURL.Port()
		if d.proxyPort == "" {
			d.proxyPort = defaultSOCKS4Port
		}
		if proxyURL.User != nil {
			d.userID = proxyURL.User.Username()
		}
	}
	return d.dialContext
}

func (d *socks4Dialer) dialContext(ctx context.Context, _, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("engine: SOCKS4 dial address %q: %w", address, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("engine: SOCKS4 invalid target port %q", port)
	}
	if d.proxyHost == "" {
		return nil, fmt.Errorf("%w: SOCKS4 proxy has no host", ErrInvalidProxy)
	}
	if strings.IndexByte(d.userID, 0) >= 0 || len(d.userID) > socks4MaxUserIDLen {
		return nil, fmt.Errorf("%w: invalid SOCKS4 user name", ErrInvalidProxy)
	}

	// Resolve and validate before opening the proxy connection: the target
	// checks must happen even when the proxy itself is unreachable.
	target, err := d.targetIPv4(ctx, host)
	if err != nil {
		return nil, err
	}

	proxyAddress := net.JoinHostPort(d.proxyHost, d.proxyPort)
	conn, err := d.dialer.DialContext(ctx, d.proxyNetwork, proxyAddress)
	if err != nil {
		return nil, fmt.Errorf("engine: dial SOCKS4 proxy: %w", err)
	}
	connected := false
	defer func() {
		if !connected {
			_ = conn.Close()
		}
	}()

	if deadline, ok := socks4HandshakeDeadline(ctx, d.dialer.Timeout); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("engine: SOCKS4 handshake deadline: %w", err)
		}
	}
	if err := d.handshake(conn, target, uint16(portNumber)); err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("engine: clear SOCKS4 deadline: %w", err)
	}
	connected = true
	return conn, nil
}

// targetIPv4 validates every address the target resolves to and returns the
// first IPv4 candidate. AllowPrivate keeps its usual meaning because the check
// goes through Guard.CheckIP.
func (d *socks4Dialer) targetIPv4(ctx context.Context, host string) (net.IP, error) {
	if ip := parseHostIP(host); ip != nil {
		if err := d.guard.CheckIP(ip); err != nil {
			return nil, err
		}
		v4 := ip.To4()
		if v4 == nil {
			return nil, fmt.Errorf("engine: SOCKS4 requires an IPv4 target: %q is IPv6", host)
		}
		return v4, nil
	}

	addresses, err := d.guard.resolver().LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("engine: resolve %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("%w: %q resolves to no addresses", ErrBlockedAddress, host)
	}
	var chosen net.IP
	for _, address := range addresses {
		if err := d.guard.CheckIP(address.IP); err != nil {
			return nil, fmt.Errorf("engine: host %q: %w", host, err)
		}
		if chosen == nil {
			chosen = address.IP.To4()
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("engine: SOCKS4 requires an IPv4 target: %q resolves only to IPv6", host)
	}
	return chosen, nil
}

// handshake sends CONNECT for an already validated IPv4 target and reads the
// fixed 8-byte reply. Code 0x5a is the only success code.
func (d *socks4Dialer) handshake(conn net.Conn, target net.IP, port uint16) error {
	request := make([]byte, 0, 9+len(d.userID))
	request = append(request, socks4Version, socks4Connect)
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], port)
	request = append(request, portBytes[:]...)
	request = append(request, target.To4()...)
	request = append(request, d.userID...)
	request = append(request, 0)
	// SOCKS4a would append the hostname here. The name was resolved locally
	// and validated, so the request already carries its IPv4 address.

	if _, err := conn.Write(request); err != nil {
		return fmt.Errorf("engine: write SOCKS4 request: %w", err)
	}

	reply := make([]byte, socks4ReplySize)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("engine: read SOCKS4 reply: %w", err)
	}
	if reply[1] != socks4Granted {
		return fmt.Errorf("engine: SOCKS4 proxy refused connection: %s", socks4ReplyText(reply[1]))
	}
	return nil
}

func socks4ReplyText(code byte) string {
	switch code {
	case 0x5b:
		return "request rejected or failed (0x5b)"
	case 0x5c:
		return "identd unreachable (0x5c)"
	case 0x5d:
		return "user ID mismatch (0x5d)"
	default:
		return fmt.Sprintf("code 0x%02x", code)
	}
}

// socks4HandshakeDeadline bounds the handshake by the context deadline when
// there is one, and by the dial timeout otherwise. When both exist the earlier
// one wins, so a long dial timeout cannot keep a canceled request waiting.
func socks4HandshakeDeadline(ctx context.Context, timeout time.Duration) (time.Time, bool) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok {
			if deadline.IsZero() || ctxDeadline.Before(deadline) {
				deadline = ctxDeadline
			}
		}
	}
	return deadline, !deadline.IsZero()
}
