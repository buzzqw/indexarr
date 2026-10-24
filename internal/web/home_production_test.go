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
// (developer machine and the coordinator's checkout); CI skips it.
func TestProductionCopyHasNoFalseAlarms(t *testing.T) {
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
	if status.ProblemIndexers > 2 {
		t.Fatalf("production copy shows %d false indexer problems: %#v", status.ProblemIndexers, status.Problems)
	}
	for _, problem := range status.Problems {
		if problem.Code == "home.problem.completeness" || problem.Code == "home.problem.zero" {
			t.Errorf("site characteristic still counted as a problem: %#v", problem)
		}
	}
}
