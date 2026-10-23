package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestMaintenanceActions(t *testing.T) {
	env := newTestEnv(t)
	id, err := env.store.CreateSource(context.Background(), store.Source{
		Name: "Broken", Kind: "torznab", Manager: "auto",
		URL: "http://broken.example/api", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.DB().ExecContext(context.Background(),
		`UPDATE sources SET last_error = 'HTTP 500', consecutive_errors = 2 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	token, cookie := csrfFrom(t, env.handler, "/maintenance")
	page := get(t, env.handler, "/maintenance").Body.String()
	if !strings.Contains(page, "Sources needing attention") || !strings.Contains(page, "HTTP 500") {
		t.Fatal("failed source not shown")
	}
	for _, action := range []string{"optimize", "reindex", "reset_errors"} {
		recorder := postForm(t, env.handler, "/maintenance", url.Values{
			"csrf":   {token},
			"action": {action},
		}, cookie)
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("%s status = %d: %s", action, recorder.Code, recorder.Body.String())
		}
	}
	source, _ := env.store.GetSource(context.Background(), id)
	if source.LastError != "" || source.ConsecutiveErrors != 0 {
		t.Errorf("source after reset = %#v", source)
	}
}

func TestServiceWorkerAndManifest(t *testing.T) {
	env := newTestEnv(t)
	sw := get(t, env.handler, "/sw.js")
	if sw.Code != http.StatusOK || !strings.Contains(sw.Body.String(), "indexarr-static") {
		t.Fatalf("sw = %d", sw.Code)
	}
	if contentType := sw.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Errorf("sw content type = %q", contentType)
	}
	manifest := get(t, env.handler, "/static/manifest.webmanifest")
	if manifest.Code != http.StatusOK || !strings.Contains(manifest.Body.String(), `"name": "indexarr"`) {
		t.Fatalf("manifest = %d", manifest.Code)
	}
	page := get(t, env.handler, "/").Body.String()
	if !strings.Contains(page, `rel="manifest"`) || !strings.Contains(page, `/static/icon.svg`) {
		t.Error("PWA links missing from layout")
	}
}
