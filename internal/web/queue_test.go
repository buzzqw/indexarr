package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestActivityQueueShowsClientProgress(t *testing.T) {
	env := newTestEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"hash":"abc","name":"Test Item","size":1048576,"total_size":1048576,"progress":0.5,"amount_left":524288,"downloaded":524288,"dlspeed":1024,"upspeed":0,"eta":60,"state":"downloading"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	if _, err := env.store.CreateDownloadClient(ctx, store.DownloadClient{
		Name: "qBittorrent", Kind: "qbittorrent", Protocol: "torrent",
		URL: server.URL, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreateDownloadClient(ctx, store.DownloadClient{
		Name: "rTorrent", Kind: "rtorrent", Protocol: "torrent",
		URL: "http://127.0.0.1:8080/RPC2", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	body := get(t, env.handler, "/activity?tab=queue&lang=en").Body.String()
	for _, want := range []string{
		"Download queue", "qBittorrent", "Test Item", "50%", "downloading", "Not available",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("queue page missing %q", want)
		}
	}
}
