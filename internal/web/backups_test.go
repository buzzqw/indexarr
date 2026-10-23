package web

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/backup"
)

func TestMaintenanceBackups(t *testing.T) {
	env := newTestEnv(t)
	info, err := backup.Create(context.Background(), env.store, env.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	token, cookie := csrfFrom(t, env.handler, "/settings?section=backup")
	page := get(t, env.handler, "/settings?section=backup").Body.String()
	if !strings.Contains(page, info.Name) || !strings.Contains(page, "Back up now") {
		t.Fatal("backup listed in maintenance")
	}
	download := get(t, env.handler, "/maintenance/backups/"+info.Name)
	if download.Code != http.StatusOK || download.Body.Len() == 0 {
		t.Fatalf("download = %d, %d bytes", download.Code, download.Body.Len())
	}
	deleted := postForm(t, env.handler, "/maintenance/backups/"+info.Name+"/delete",
		url.Values{"csrf": {token}}, cookie)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d", deleted.Code)
	}
	if list, _ := backup.List(env.store.Path()); len(list) != 0 {
		t.Errorf("backups after delete = %#v", list)
	}

	// Restore upload: the handler stages <db>.restore. No restart callback is
	// configured in the test server, so it must not kill anything.
	info, err = backup.Create(context.Background(), env.store, env.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(backup.Dir(env.store.Path()), info.Name)
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("backup", info.Name)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/maintenance/restore?csrf="+token, body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("restore = %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(env.store.Path() + ".restore"); err != nil {
		t.Fatalf("restore was not staged: %v", err)
	}
	_ = os.Remove(env.store.Path() + ".restore")
}
