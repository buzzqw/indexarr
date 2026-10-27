package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

// TestTransportInsecureTLSIsOptIn pins the per-definition TLS exception: a
// transport that did not ask for it rejects a self-signed certificate, and one
// that asked reads the same page. The exception never becomes the default.
func TestTransportInsecureTLSIsOptIn(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := NewTransport(TransportConfig{AllowPrivate: true}).Do(context.Background(), "strict", request); err == nil {
		t.Error("a transport without InsecureTLS accepted a self-signed certificate")
	}

	request, err = http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := NewTransport(TransportConfig{AllowPrivate: true, InsecureTLS: true}).Do(context.Background(), "insecure", request)
	if err != nil {
		t.Fatalf("InsecureTLS transport: %v", err)
	}
	if string(response.Body) != "ok" {
		t.Errorf("body = %q", response.Body)
	}
}

// TestConnectorInsecureTLSIsPerDefinition checks the wiring: only a public
// definition may ask for the exception, and the connector derives a transport
// instead of mutating the shared one.
func TestConnectorInsecureTLSIsPerDefinition(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><table><tr><td><a href="/t/1">Matrix 1999 1080p</a></td><td>1.2 GB</td></tr></table></body></html>`))
	}))
	t.Cleanup(server.Close)

	shared := NewTransport(TransportConfig{AllowPrivate: true})
	def := &definition.Definition{
		Schema: 1,
		Meta: definition.Meta{
			ID: "tls-check", Name: "TLS check", Type: "public",
			Protocol: "torrent", URLs: []string{server.URL + "/"}, InsecureTLS: true,
		},
		Capabilities: definition.Capabilities{Search: []string{"q"}},
		Search: definition.Search{
			Request: definition.Request{Get: "/"},
			Response: definition.Response{
				Type: "html",
				Rows: definition.RowSelector{Selector: definition.Selector{"table tr"}},
				Fields: map[string]definition.Field{
					"title":   {Selector: definition.Selector{"a"}},
					"details": {Selector: definition.Selector{"a"}, Attr: "href", Type: "url"},
				},
			},
		},
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer: IndexerRef{Slug: "tls-check"}, Definition: def, Transport: shared,
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	if !connector.transport.Config().InsecureTLS {
		t.Error("the connector did not derive an insecure transport")
	}
	if shared.Config().InsecureTLS {
		t.Error("the shared transport was mutated")
	}

	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "matrix", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}

	private := *def
	private.Meta.Type = "private"
	if _, err := NewDefinitionConnector(DefinitionConfig{
		Indexer: IndexerRef{Slug: "tls-check"}, Definition: &private, Transport: shared,
	}); err == nil {
		t.Error("a private definition was allowed to skip TLS verification")
	}
}
