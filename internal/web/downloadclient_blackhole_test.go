package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestBlackholeClientNeedsFolderAndNoURL covers the one kind whose address may
// stay empty: the form accepts it and stores the save folder instead.
func TestBlackholeClientNeedsFolderAndNoURL(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/download-clients/new")
	missing := postForm(t, env.handler, "/download-clients", url.Values{
		"csrf":     {token},
		"name":     {"Blackhole"},
		"kind":     {"torrentblackhole"},
		"protocol": {"torrent"},
		"url":      {""},
		"enabled":  {"on"},
	}, cookie)
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "save folder") {
		t.Fatalf("missing folder = %d: %s", missing.Code, missing.Body.String())
	}
	created := postForm(t, env.handler, "/download-clients", url.Values{
		"csrf":      {token},
		"name":      {"Blackhole"},
		"kind":      {"torrentblackhole"},
		"protocol":  {"torrent"},
		"url":       {""},
		"save_path": {"/downloads/blackhole"},
		"enabled":   {"on"},
	}, cookie)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	clients, err := env.store.ListDownloadClients(context.Background())
	if err != nil || len(clients) != 1 {
		t.Fatalf("clients = %v, %v", clients, err)
	}
	if clients[0].Kind != "torrentblackhole" || clients[0].URL != "" ||
		clients[0].SavePath != "/downloads/blackhole" {
		t.Errorf("stored client = %+v", clients[0])
	}
}
