package store_test

import (
	"path/filepath"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestVisibleCategorySeedersIndexExists(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	var name string
	if err := st.DB().QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type = 'index' AND name = 'releases_visible_category_seeders'`).Scan(&name); err != nil {
		t.Fatalf("index missing: %v", err)
	}
}
