package appsync

import (
	"testing"
)

func reconcileSpec(name, base, key string) IndexerSpec {
	return IndexerSpec{
		Name: name, Implementation: "Torznab", ConfigContract: "TorznabSettings",
		BaseURL: base, APIKey: key, Categories: []int{5000}, Priority: 25,
		Tags: []int{}, EnableRSS: true, EnableAutomaticSearch: true,
		EnableInteractiveSearch: true, Protocol: "torrent",
	}
}

func remoteFromSpec(id int, spec IndexerSpec) RemoteIndexer {
	return RemoteIndexer{
		ID: id, Name: spec.Name, Implementation: spec.Implementation,
		ConfigContract: spec.ConfigContract, Priority: spec.Priority, Tags: spec.Tags,
		Fields: map[string]any{
			"baseUrl":    spec.BaseURL,
			"apiKey":     spec.APIKey,
			"categories": append([]int(nil), spec.Categories...),
		},
		RawHash: SpecHash(spec),
	}
}

func TestReconcileDisabled(t *testing.T) {
	t.Parallel()

	desired := []IndexerSpec{reconcileSpec("indexarr · A", "https://a.example/api", "a")}
	remote := []RemoteIndexer{remoteFromSpec(1, reconcileSpec("indexarr · B", "https://b.example/api", "b"))}

	for _, level := range []string{"", "disabled"} {
		if actions := Reconcile(desired, remote, ReconcileOptions{SyncLevel: level}); len(actions) != 0 {
			t.Errorf("level %q produced actions: %+v", level, actions)
		}
	}
}

func TestReconcileAddRemoveIgnoresUpdates(t *testing.T) {
	t.Parallel()

	old := reconcileSpec("indexarr · Existing", "https://existing.example/api", "old")
	changed := old
	changed.Priority = 40
	changed.APIKey = "new"
	added := reconcileSpec("indexarr · New", "https://new.example/api", "new-key")
	orphan := reconcileSpec("indexarr · Orphan", "https://orphan.example/api", "orphan-key")
	unmanaged := reconcileSpec("Manual", "https://manual.example/api", "manual-key")

	desired := []IndexerSpec{changed, added}
	remote := []RemoteIndexer{remoteFromSpec(1, old), remoteFromSpec(2, orphan), remoteFromSpec(3, unmanaged)}

	actions := Reconcile(desired, remote, ReconcileOptions{SyncLevel: "add-remove"})
	if got, want := len(actions), 2; got != want {
		t.Fatalf("got %d actions, want %d: %+v", got, want, actions)
	}
	if actions[0].Kind != ActionAdd || actions[0].Spec.Name != added.Name {
		t.Errorf("first action = %+v, want Add %q", actions[0], added.Name)
	}
	if actions[1].Kind != ActionDelete || actions[1].RemoteID != 2 {
		t.Errorf("second action = %+v, want Delete remote 2", actions[1])
	}
}

func TestReconcileFullUpdatesOnlyChangedHash(t *testing.T) {
	t.Parallel()

	old := reconcileSpec("indexarr · Existing", "https://existing.example/api", "key")
	changed := old
	changed.Priority = 40

	actions := Reconcile([]IndexerSpec{changed}, []RemoteIndexer{remoteFromSpec(7, old)},
		ReconcileOptions{SyncLevel: "full"})
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1: %+v", len(actions), actions)
	}
	action := actions[0]
	if action.Kind != ActionUpdate || action.RemoteID != 7 {
		t.Fatalf("action = %+v, want Update remote 7", action)
	}
	if action.Spec.Priority != 40 {
		t.Errorf("update spec priority = %d, want 40", action.Spec.Priority)
	}

	if actions := Reconcile([]IndexerSpec{old}, []RemoteIndexer{remoteFromSpec(7, old)},
		ReconcileOptions{SyncLevel: "full"}); len(actions) != 0 {
		t.Fatalf("equal hashes produced actions: %+v", actions)
	}
}

func TestReconcileDeletesOnlyManaged(t *testing.T) {
	t.Parallel()

	remote := []RemoteIndexer{
		remoteFromSpec(1, reconcileSpec("Public Tracker", "https://public.example/api", "p")),
		remoteFromSpec(2, reconcileSpec("indexarr old", "https://lower.example/api", "l")),
		remoteFromSpec(3, reconcileSpec("indexarr · Old", "https://old.example/api", "o")),
	}
	actions := Reconcile(nil, remote, ReconcileOptions{SyncLevel: "full"})
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1: %+v", len(actions), actions)
	}
	if actions[0].Kind != ActionDelete || actions[0].RemoteID != 3 {
		t.Fatalf("action = %+v, want Delete remote 3", actions[0])
	}
}

func TestReconcileDeterministicOrder(t *testing.T) {
	t.Parallel()

	desired := []IndexerSpec{
		reconcileSpec("indexarr · B", "https://b.example/api", "b"),
		reconcileSpec("indexarr · A", "https://a.example/api", "a"),
		reconcileSpec("indexarr · C", "https://c.example/api", "c"),
	}
	remote := []RemoteIndexer{
		remoteFromSpec(2, reconcileSpec("indexarr · zeta", "https://zeta.example/api", "z")),
		remoteFromSpec(1, reconcileSpec("indexarr · alpha", "https://alpha.example/api", "x")),
	}
	actions := Reconcile(desired, remote, ReconcileOptions{SyncLevel: "full"})
	if len(actions) != 5 {
		t.Fatalf("got %d actions, want 5: %+v", len(actions), actions)
	}

	wantKinds := []ActionKind{ActionAdd, ActionAdd, ActionAdd, ActionDelete, ActionDelete}
	for i, want := range wantKinds {
		if actions[i].Kind != want {
			t.Errorf("action %d kind = %q, want %q", i, actions[i].Kind, want)
		}
	}
	if actions[0].Spec.Name != desired[0].Name ||
		actions[1].Spec.Name != desired[1].Name ||
		actions[2].Spec.Name != desired[2].Name {
		t.Errorf("adds are not in desired order: %+v", actions)
	}
	if actions[3].RemoteID != 1 || actions[4].RemoteID != 2 {
		t.Errorf("deletes are not sorted by name: %+v", actions)
	}
}

func TestReconcileMatchesByBaseURLWhenNamesDiffer(t *testing.T) {
	t.Parallel()

	old := reconcileSpec("indexarr · Old name", "https://same.example/api", "key")
	desired := old
	desired.Name = "indexarr · New name"

	actions := Reconcile([]IndexerSpec{desired}, []RemoteIndexer{remoteFromSpec(5, old)},
		ReconcileOptions{SyncLevel: "full"})
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1: %+v", len(actions), actions)
	}
	if actions[0].Kind != ActionUpdate || actions[0].RemoteID != 5 {
		t.Fatalf("action = %+v, want Update remote 5", actions[0])
	}
}

func TestReconcileNameMatchWinsAndAPIKeyIsNotUsed(t *testing.T) {
	t.Parallel()

	sameNameOld := reconcileSpec("indexarr · Same", "https://old.example/api", "same-key")
	sameNameNew := sameNameOld
	sameNameNew.BaseURL = "https://new.example/api"

	actions := Reconcile([]IndexerSpec{sameNameNew}, []RemoteIndexer{remoteFromSpec(8, sameNameOld)},
		ReconcileOptions{SyncLevel: "full"})
	if len(actions) != 1 || actions[0].Kind != ActionUpdate || actions[0].RemoteID != 8 {
		t.Fatalf("name match failed: %+v", actions)
	}

	// Same API key but a different name and URL must not cause a match.
	other := reconcileSpec("indexarr · Other", "https://other.example/api", "same-key")
	actions = Reconcile([]IndexerSpec{other}, []RemoteIndexer{remoteFromSpec(8, sameNameOld)},
		ReconcileOptions{SyncLevel: "full"})
	if len(actions) != 2 {
		t.Fatalf("got %d actions, want Add+Delete: %+v", len(actions), actions)
	}
	if actions[0].Kind != ActionAdd || actions[1].Kind != ActionDelete {
		t.Fatalf("unexpected actions: %+v", actions)
	}
}

func TestReconcileUnknownLevelIsNoop(t *testing.T) {
	t.Parallel()

	desired := []IndexerSpec{reconcileSpec("indexarr · A", "https://a.example/api", "a")}
	if actions := Reconcile(desired, nil, ReconcileOptions{SyncLevel: "sometimes"}); len(actions) != 0 {
		t.Fatalf("unknown level produced actions: %+v", actions)
	}
}
