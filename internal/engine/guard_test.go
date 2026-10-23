package engine

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"
)

func TestGuardCheckIP(t *testing.T) {
	guard := &Guard{}
	blocked := []string{
		"127.0.0.1",
		"::1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.10",
		"169.254.169.254",
		"fd00::1",
		"fe80::1",
		"100.64.0.1",
		"0.0.0.0",
		"::",
		"224.0.0.1",
		"ff02::1",
		"192.0.0.5",
		"198.18.0.1",
		"240.0.0.1",
		"255.255.255.255",
		"::ffff:127.0.0.1",
	}
	for _, raw := range blocked {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("test IP %q does not parse", raw)
		}
		if err := guard.CheckIP(ip); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("CheckIP(%s) = %v, want ErrBlockedAddress", raw, err)
		}
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, raw := range allowed {
		if err := guard.CheckIP(net.ParseIP(raw)); err != nil {
			t.Errorf("CheckIP(%s) = %v, want nil", raw, err)
		}
	}

	if err := guard.CheckIP(nil); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("CheckIP(nil) = %v, want ErrBlockedAddress", err)
	}
}

func TestGuardAllowPrivate(t *testing.T) {
	guard := &Guard{AllowPrivate: true}
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "fd00::1"} {
		if err := guard.CheckIP(net.ParseIP(raw)); err != nil {
			t.Errorf("CheckIP(%s) with AllowPrivate = %v, want nil", raw, err)
		}
	}

	// The scheme check is independent from the private-address opt-in.
	if err := guard.CheckURL(mustParseURL(t, "ftp://127.0.0.1/file")); err == nil {
		t.Error("non-HTTP scheme accepted with AllowPrivate")
	}
	if err := guard.CheckURL(mustParseURL(t, "http://127.0.0.1:8080/")); err != nil {
		t.Errorf("local HTTP URL rejected with AllowPrivate: %v", err)
	}
}

func TestGuardCheckURL(t *testing.T) {
	guard := &Guard{}
	for _, raw := range []string{
		"ftp://example.com/file",
		"file:///etc/passwd",
		"gopher://example.com/",
		"http://127.0.0.1/",
		"https://169.254.169.254/latest/meta-data",
		"http://[::1]/",
		"http://0.0.0.0/",
	} {
		if err := guard.CheckURL(mustParseURL(t, raw)); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("CheckURL(%s) = %v, want ErrBlockedAddress", raw, err)
		}
	}

	for _, raw := range []string{"http://8.8.8.8/", "https://1.1.1.1/"} {
		if err := guard.CheckURL(mustParseURL(t, raw)); err != nil {
			t.Errorf("CheckURL(%s) = %v, want nil", raw, err)
		}
	}

	if err := guard.CheckURL(nil); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("CheckURL(nil) = %v, want ErrBlockedAddress", err)
	}
}

func TestGuardDialContext(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("ok"))
		_ = conn.Close()
		close(accepted)
	}()

	dial := (&Guard{AllowPrivate: true}).DialContext(&net.Dialer{Timeout: time.Second})
	conn, err := dial(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial with AllowPrivate: %v", err)
	}
	buffer := make([]byte, 2)
	if _, err := conn.Read(buffer); err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = conn.Close()
	if string(buffer) != "ok" {
		t.Errorf("read %q, want ok", buffer)
	}
	<-accepted
}

func TestGuardDialContextBlocksLiteralPrivateIP(t *testing.T) {
	guard := &Guard{}
	dial := guard.DialContext(&net.Dialer{Timeout: time.Second})
	_, err := dial(context.Background(), "tcp", "127.0.0.1:9")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("dial = %v, want ErrBlockedAddress", err)
	}
}

func TestGuardNetworkMatches(t *testing.T) {
	if !networkMatches("tcp", net.ParseIP("8.8.8.8")) {
		t.Error("tcp does not match IPv4")
	}
	if !networkMatches("tcp4", net.ParseIP("127.0.0.1")) {
		t.Error("tcp4 does not match IPv4")
	}
	if networkMatches("tcp4", net.ParseIP("::1")) {
		t.Error("tcp4 matches IPv6")
	}
	if !networkMatches("tcp6", net.ParseIP("::1")) {
		t.Error("tcp6 does not match IPv6")
	}
	if networkMatches("tcp6", net.ParseIP("127.0.0.1")) {
		t.Error("tcp6 matches IPv4")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}
