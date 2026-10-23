package store

import (
	"context"
	"testing"
)

func TestAdditionalAPIKeys(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	key, err := st.AddAPIKey(ctx, "Sonarr")
	if err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}
	if len(key.Key) != 64 || key.Name != "Sonarr" {
		t.Fatalf("key = %#v", key)
	}
	list, err := st.ListAPIKeys(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %#v, %v", list, err)
	}
	found, ok, err := st.LookupAPIKey(ctx, key.Key)
	if err != nil || !ok || found.ID != key.ID {
		t.Fatalf("lookup = %#v, %v, %v", found, ok, err)
	}
	if _, ok, _ := st.LookupAPIKey(ctx, "not-a-key"); ok {
		t.Error("unknown key found")
	}
	if err := st.TouchAPIKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = st.ListAPIKeys(ctx)
	if list[0].Requests != 1 || list[0].LastUsedAt.IsZero() {
		t.Errorf("usage not recorded: %#v", list[0])
	}
	if ok, err := st.DeleteAPIKey(ctx, key.ID); err != nil || !ok {
		t.Fatalf("delete = %v, %v", ok, err)
	}
	if list, _ := st.ListAPIKeys(ctx); len(list) != 0 {
		t.Errorf("list after delete = %#v", list)
	}
}
