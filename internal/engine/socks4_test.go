package engine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type socks4Handshake struct {
	version  byte
	command  byte
	port     int
	ip       net.IP
	userID   string
	hostname string
}

// socks4TestProxy accepts one SOCKS4 handshake per connection. When relay is
// true it connects to the target named in the request and copies bytes in both
// directions, so the test exercises a real HTTP response through the tunnel.
type socks4TestProxy struct {
	listener net.Listener
	addr     string
	records  chan socks4Handshake
	hits     atomic.Int64
	reply    byte
	relay    bool
}

func newSOCKS4TestProxy(t *testing.T, reply byte, relay bool) *socks4TestProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	proxy := &socks4TestProxy{
		listener: listener,
		addr:     listener.Addr().String(),
		records:  make(chan socks4Handshake, 4),
		reply:    reply,
		relay:    relay,
	}
	t.Cleanup(func() { _ = listener.Close() })
	go proxy.serve()
	return proxy
}

func (p *socks4TestProxy) serve() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

func (p *socks4TestProxy) handle(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))

	handshake, err := readSOCKS4Handshake(client)
	if err != nil {
		return
	}
	p.hits.Add(1)
	p.records <- handshake

	if handshake.command != socks4Connect || p.reply != socks4Granted {
		_, _ = client.Write([]byte{0, 0x5b, 0, 0, 0, 0, 0, 0})
		return
	}

	var target net.Conn
	if p.relay {
		address := net.JoinHostPort(handshake.ip.String(), strconv.Itoa(handshake.port))
		target, err = net.DialTimeout("tcp", address, 2*time.Second)
		if err != nil {
			_, _ = client.Write([]byte{0, 0x5b, 0, 0, 0, 0, 0, 0})
			return
		}
		defer target.Close()
	}

	if _, err := client.Write([]byte{0, socks4Granted, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	if target == nil {
		return
	}

	_ = client.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(target, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, target)
		done <- struct{}{}
	}()
	<-done
}

func readSOCKS4Handshake(r io.Reader) (socks4Handshake, error) {
	var handshake socks4Handshake
	header := make([]byte, 8)
	if _, err := io.ReadFull(r, header); err != nil {
		return handshake, err
	}
	handshake.version = header[0]
	handshake.command = header[1]
	handshake.port = int(binary.BigEndian.Uint16(header[2:4]))
	handshake.ip = net.IPv4(header[4], header[5], header[6], header[7]).To4()

	userID, err := readSOCKS4String(r)
	if err != nil {
		return handshake, err
	}
	handshake.userID = userID
	// A SOCKS4a client asks the proxy to resolve 0.0.0.x. This dialer must
	// never send that form; capture it anyway so a test can prove it.
	if handshake.ip[0] == 0 && handshake.ip[1] == 0 && handshake.ip[2] == 0 && handshake.ip[3] != 0 {
		hostname, err := readSOCKS4String(r)
		if err != nil {
			return handshake, err
		}
		handshake.hostname = hostname
	}
	return handshake, nil
}

func readSOCKS4String(r io.Reader) (string, error) {
	var out []byte
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, one); err != nil {
			return "", err
		}
		if one[0] == 0 {
			return string(out), nil
		}
		out = append(out, one[0])
		if len(out) > socks4MaxUserIDLen {
			return "", errors.New("SOCKS4 string too long")
		}
	}
}

func TestTransportUsesSOCKS4Proxy(t *testing.T) {
	for _, scheme := range []string{"socks4", "socks4a"} {
		t.Run(scheme, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/resource" {
					http.NotFound(w, r)
					return
				}
				_, _ = io.WriteString(w, "from-target")
			}))
			defer target.Close()
			proxy := newSOCKS4TestProxy(t, socks4Granted, true)

			proxyURL, err := url.Parse(fmt.Sprintf("%s://alice:secret@%s", scheme, proxy.addr))
			if err != nil {
				t.Fatal(err)
			}
			transport := newLocalTransport(TransportConfig{ProxyURL: proxyURL.String()})

			requestURL := target.URL + "/resource"
			if scheme == "socks4a" {
				// A name target proves SOCKS4a is resolved locally: the
				// handshake must still carry the validated IPv4 address.
				_, port, err := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
				if err != nil {
					t.Fatal(err)
				}
				requestURL = "http://localhost:" + port + "/resource"
			}
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, requestURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.Do(context.Background(), "idx", req)
			if err != nil {
				t.Fatalf("Do through %s proxy: %v", scheme, err)
			}
			if string(response.Body) != "from-target" {
				t.Errorf("body = %q, want from-target", response.Body)
			}

			handshake := <-proxy.records
			if handshake.version != socks4Version {
				t.Errorf("version = %#x, want %#x", handshake.version, socks4Version)
			}
			if handshake.command != socks4Connect {
				t.Errorf("command = %#x, want CONNECT", handshake.command)
			}
			if handshake.userID != "alice" {
				t.Errorf("USERID = %q, want alice", handshake.userID)
			}
			if got := handshake.ip.String(); got != "127.0.0.1" {
				t.Errorf("target IP = %s, want 127.0.0.1", got)
			}
			if handshake.hostname != "" {
				t.Errorf("handshake leaked a hostname to the proxy: %q", handshake.hostname)
			}
			if handshake.port == 0 {
				t.Error("target port was not sent")
			}
			if proxy.hits.Load() != 1 {
				t.Errorf("proxy hits = %d, want 1", proxy.hits.Load())
			}
		})
	}
}

func TestTransportSOCKS4Refusal(t *testing.T) {
	proxy := newSOCKS4TestProxy(t, 0x5b, false)
	transport := newLocalTransport(TransportConfig{ProxyURL: "socks4://" + proxy.addr})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.Do(context.Background(), "idx", req)
	if err == nil {
		t.Fatal("Do succeeded through a refusing SOCKS4 proxy")
	}
	if !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "0x5b") {
		t.Fatalf("error = %v, want a clear SOCKS4 refusal", err)
	}
	if proxy.hits.Load() != 1 {
		t.Errorf("proxy hits = %d, want 1", proxy.hits.Load())
	}
}

func TestSOCKS4GuardBlocksPrivateTargetBeforeDialing(t *testing.T) {
	proxy := newSOCKS4TestProxy(t, socks4Granted, true)
	proxyURL, err := url.Parse("socks4://" + proxy.addr)
	if err != nil {
		t.Fatal(err)
	}

	dial := newSOCKS4Dialer(&Guard{}, &net.Dialer{Timeout: time.Second}, proxyURL, "tcp")
	_, err = dial(context.Background(), "tcp", "10.0.0.1:80")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("dial error = %v, want ErrBlockedAddress", err)
	}
	if proxy.hits.Load() != 0 {
		t.Fatalf("proxy was reached (%d hits) before the target was rejected", proxy.hits.Load())
	}

	// The same rejection must happen through the transport, after its own URL
	// check and before any handshake.
	transport := NewTransport(TransportConfig{ProxyURL: proxyURL.String()})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://10.0.0.1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.Do(context.Background(), "idx", req)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("Do error = %v, want ErrBlockedAddress", err)
	}
	if proxy.hits.Load() != 0 {
		t.Fatalf("proxy was reached (%d hits) by a blocked transport request", proxy.hits.Load())
	}
}

func TestSOCKS4RejectsIPv6OnlyTarget(t *testing.T) {
	proxyURL, err := url.Parse("socks4://127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	dial := newSOCKS4Dialer(&Guard{AllowPrivate: true}, &net.Dialer{Timeout: time.Second}, proxyURL, "tcp")
	_, err = dial(context.Background(), "tcp", "[::1]:80")
	if err == nil || !strings.Contains(err.Error(), "IPv6") {
		t.Fatalf("dial error = %v, want a clear IPv6-only error", err)
	}
}

func TestParseProxyURLSOCKS4(t *testing.T) {
	for _, raw := range []string{
		"socks4://proxy.example",
		"socks4a://proxy.example:1080",
		"socks4://user:pass@proxy.example:1080",
	} {
		parsed, err := parseProxyURL(raw)
		if err != nil {
			t.Errorf("%s rejected: %v", raw, err)
			continue
		}
		if !isSOCKS4Scheme(parsed.Scheme) {
			t.Errorf("%s scheme = %q, not a SOCKS4 scheme", raw, parsed.Scheme)
		}
		if parsed.Port() == "" && parsed.Scheme == "socks4a" {
			t.Errorf("%s lost its explicit port", raw)
		}
	}
	for _, raw := range []string{
		"ftp://proxy.example:21",
		"socks4x://proxy.example:1080",
		"socks://proxy.example:1080",
		"http:///missing-host",
	} {
		if _, err := parseProxyURL(raw); !errors.Is(err, ErrInvalidProxy) {
			t.Errorf("%s error = %v, want ErrInvalidProxy", raw, err)
		}
	}
}
