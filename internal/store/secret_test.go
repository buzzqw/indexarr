package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestDeleteSecret(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "delete-secret")
	id := mustCreateIndexer(t, st, "delete-secret", "delete-secret", "Delete Secret")

	if err := st.PutSecret(ctx, Secret{
		IndexerID:  id,
		Key:        "api_key",
		Nonce:      []byte("nonce"),
		Ciphertext: []byte("ciphertext"),
	}); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}

	if err := st.DeleteSecret(ctx, id, "api_key"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if _, err := st.GetSecret(ctx, id, "api_key"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetSecret after delete: err = %v, want sql.ErrNoRows", err)
	}

	// Deleting a key that does not exist is idempotent.
	if err := st.DeleteSecret(ctx, id, "api_key"); err != nil {
		t.Fatalf("DeleteSecret missing: %v", err)
	}
}
