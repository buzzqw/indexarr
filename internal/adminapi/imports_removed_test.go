package adminapi

import (
	"net/http"
	"testing"
)

func TestManagerImportAPIIsGone(t *testing.T) {
	e := newEnv(t, true, true)
	for _, path := range []string{"/api/v2/import/prowlarr", "/api/v2/import/jackett"} {
		res := e.do(http.MethodPost, path, "{}")
		if res.status != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, res.status)
		}
	}
}
