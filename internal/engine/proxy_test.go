package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newForwardProxy starts an HTTP forward proxy. The request URL arriving at
// the handler is absolute, so the handler can forward it to the target without
// being the target itself.
func newForwardProxy(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !r.URL.IsAbs() {
			http.Error(w, "not a forward-proxy request", http.StatusBadRequest)
			return
		}
		outgoing := r.Clone(r.Context())
		outgoing.RequestURI = ""
		outgoing.Header.Del("Proxy-Connection")
		response, err := http.DefaultTransport.RoundTrip(outgoing)
		if err != nil {
			http.Error(w, "proxy upstream error", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for name, values := range response.Header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(proxy.Close)
	return proxy
}

func TestTransportUsesHTTPProxy(t *testing.T) {
	var proxyHits, targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		if r.URL.Path != "/resource" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "from-target")
	}))
	defer target.Close()
	proxy := newForwardProxy(t, &proxyHits)

	transport := newLocalTransport(TransportConfig{ProxyURL: proxy.URL})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target.URL+"/resource", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.Do(context.Background(), "idx", req)
	if err != nil {
		t.Fatalf("Do through proxy: %v", err)
	}
	if string(response.Body) != "from-target" {
		t.Errorf("body = %q, want from-target", response.Body)
	}
	if proxyHits.Load() != 1 {
		t.Errorf("proxy hits = %d, want 1", proxyHits.Load())
	}
	if targetHits.Load() != 1 {
		t.Errorf("target hits = %d, want 1", targetHits.Load())
	}
}

func TestTransportInvalidProxyFailsClosed(t *testing.T) {
	cases := []string{
		"ftp://user:secret@proxy.example:21",
		"socks4x://proxy.example:1080",
		"://bad",
		"http:///missing-host",
	}
	for _, proxyURL := range cases {
		t.Run(proxyURL, func(t *testing.T) {
			var transport *Transport
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("NewTransport panicked: %v", recovered)
					}
				}()
				transport = NewTransport(TransportConfig{ProxyURL: proxyURL})
			}()

			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://target.example/", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = transport.Do(context.Background(), "idx", req)
			if !errors.Is(err, ErrInvalidProxy) {
				t.Fatalf("Do error = %v, want ErrInvalidProxy", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("proxy error leaked credentials: %v", err)
			}
		})
	}
}

func TestTransportValidProxySchemes(t *testing.T) {
	for _, proxyURL := range []string{
		"http://proxy.example:8080",
		"https://proxy.example:8443",
		"socks4://proxy.example:1080",
		"socks4a://proxy.example:1080",
		"socks5://proxy.example:1080",
		"socks5h://proxy.example:1080",
	} {
		transport := NewTransport(TransportConfig{ProxyURL: proxyURL})
		if transport.proxyErr != nil {
			t.Errorf("%s rejected: %v", proxyURL, transport.proxyErr)
		}
	}
}
