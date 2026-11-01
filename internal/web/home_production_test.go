package web

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

// TestProductionCopyHasNoFalseAlarms is the §1.1 acceptance check. It runs
// against a read-only copy of the real production database when it is present
// (the developer machine); CI skips it.
func TestProductionCopyHasNoFalseAlarms(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("production acceptance check: local only")
	}
	source := filepath.Join("..", "..", "data", "indexarr.db")
	if _, err := os.Stat(source); err != nil {
		t.Skip("production database not present")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "indexarr.db")
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(target)
	if err != nil {
		in.Close()
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		in.Close()
		t.Fatal(err)
	}
	out.Close()
	in.Close()
	st, err := store.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{store: st}
	status := srv.HomeStatus(context.Background())
	if status.Level == "danger" {
		t.Fatalf("production copy is in danger: %#v", status)
	}
	// A rate limit, a flood control, a timeout or a paused breaker is the
	// site's own answer, not a mistake of the model: it depends on how busy
	// the host is when the copy is taken. The check fails on the causes that
	// only a bug can produce on a working installation, and on a pile of
	// problems that no single site condition explains.
	transient := map[string]bool{
		"home.problem.rateLimit": true, "home.problem.flood": true,
		"home.problem.timeout": true, "home.problem.circuit": true,
	}
	permanent := 0
	for _, problem := range status.Problems {
		switch {
		case transient[problem.Code]:
		case problem.Code == "home.problem.completeness" || problem.Code == "home.problem.zero":
			t.Errorf("site characteristic still counted as a problem: %#v", problem)
		case problem.Code == "home.problem.unknown":
			t.Errorf("a cause without its own explanation: %#v", problem)
		default:
			permanent++
		}
	}
	if permanent > 2 {
		t.Fatalf("production copy shows %d indexer problems: %#v", permanent, status.Problems)
	}
}
