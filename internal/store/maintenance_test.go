package store

import (
	"context"
	"testing"
)

func TestMaintenanceHelpers(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	id, err := st.CreateSource(ctx, testSource("Broken"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE sources SET last_error = 'boom', consecutive_errors = 3 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	reset, err := st.ResetSourceErrors(ctx)
	if err != nil || reset != 1 {
		t.Fatalf("ResetSourceErrors = %d, %v", reset, err)
	}
	source, err := st.GetSource(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if source.LastError != "" || source.ConsecutiveErrors != 0 {
		t.Errorf("source after reset = %#v", source)
	}
	if err := st.Optimize(ctx); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if err := st.Reindex(ctx); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
}
