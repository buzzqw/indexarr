package engine

import (
	"net/url"
	"os"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

func TestDebugMirCrewForumPage(t *testing.T) {
	body, err := os.ReadFile("/home/andres/indexarr/internal/catalog/definitions/mircrew.yaml")
	if err != nil {
		t.Skip(err)
	}
	def, err := definition.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile("/tmp/forum51.html")
	if err != nil {
		t.Skip(err)
	}
	base, _ := url.Parse("https://mircrew-releases.org/")
	rows, err := extractHTMLRows(page, def.Catalog.Response, base)
	t.Logf("rows=%d err=%v", len(rows), err)
	if len(rows) > 0 {
		t.Logf("first=%v", rows[0])
	}
}
