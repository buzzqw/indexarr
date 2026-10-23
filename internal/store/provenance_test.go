package store

import (
	"context"
	"testing"
	"time"
)

func mustUpsertProvenanceRelease(t *testing.T, st *Store, key, title string, firstSeen, lastSeen time.Time, seeders, leechers int64) Release {
	t.Helper()
	release := Release{
		DedupKey:    key,
		Title:       title,
		SearchTitle: NormalizeSearch(title),
		Seeders:     seeders,
		Leechers:    leechers,
		FirstSeenAt: firstSeen,
		LastSeenAt:  lastSeen,
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease(%s): %v", key, err)
	}
	return release
}

func TestProvenance(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "prov-def")
	// The ids and the names are deliberately in opposite order to prove that
	// names are sorted, not returned in insertion order.
	first := mustCreateIndexer(t, st, "prov-def", "prov-one", "Zulu Tracker")
	second := mustCreateIndexer(t, st, "prov-def", "prov-two", "Alpha Tracker")

	t1 := time.Unix(1_700_000_000, 0)
	t2 := t1.Add(10 * time.Minute)
	firstRelease := mustUpsertProvenanceRelease(t, st, "prov-first", "First Provenance", t1, t1, 5, 1)
	secondRelease := mustUpsertProvenanceRelease(t, st, "prov-second", "Second Provenance", t1, t1, 4, 2)

	for _, source := range []ReleaseSource{
		{ReleaseID: firstRelease.ID, IndexerID: first, FirstSeenAt: t1, LastSeenAt: t1, Seeders: 5, Leechers: 1},
		{ReleaseID: firstRelease.ID, IndexerID: second, FirstSeenAt: t1, LastSeenAt: t2, Seeders: 9, Leechers: 3},
		{ReleaseID: secondRelease.ID, IndexerID: second, FirstSeenAt: t1, LastSeenAt: t1, Seeders: 4, Leechers: 2},
	} {
		if err := st.RecordReleaseSource(ctx, source); err != nil {
			t.Fatalf("RecordReleaseSource: %v", err)
		}
	}

	// Duplicate, zero and negative ids are ignored.
	provenance, err := st.ReleaseProvenance(ctx,
		[]int64{firstRelease.ID, firstRelease.ID, secondRelease.ID, 0, -1})
	if err != nil {
		t.Fatalf("ReleaseProvenance: %v", err)
	}
	if len(provenance) != 2 {
		t.Fatalf("provenance rows = %d, want 2: %#v", len(provenance), provenance)
	}
	firstProv, ok := provenance[firstRelease.ID]
	if !ok {
		t.Fatal("first release has no provenance")
	}
	if firstProv.Indexers != 2 {
		t.Errorf("first indexers = %d, want 2", firstProv.Indexers)
	}
	if len(firstProv.Names) != 2 || firstProv.Names[0] != "Alpha Tracker" || firstProv.Names[1] != "Zulu Tracker" {
		t.Errorf("first names = %#v", firstProv.Names)
	}
	if firstProv.Seeders != 9 || firstProv.Leechers != 3 {
		t.Errorf("first swarm = %d/%d, want 9/3", firstProv.Seeders, firstProv.Leechers)
	}
	if firstProv.LastSeenAt.Unix() != t2.Unix() {
		t.Errorf("first last seen = %v, want %v", firstProv.LastSeenAt, t2)
	}

	secondProv := provenance[secondRelease.ID]
	if secondProv.Indexers != 1 || len(secondProv.Names) != 1 || secondProv.Names[0] != "Alpha Tracker" {
		t.Errorf("second provenance = %#v", secondProv)
	}
	if secondProv.Seeders != 4 || secondProv.Leechers != 2 {
		t.Errorf("second swarm = %d/%d", secondProv.Seeders, secondProv.Leechers)
	}

	// A release without provenance is absent, not zero-filled.
	missing, err := st.ReleaseProvenance(ctx, []int64{firstRelease.ID + 1000})
	if err != nil {
		t.Fatalf("ReleaseProvenance missing: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing provenance = %#v", missing)
	}

	empty, err := st.ReleaseProvenance(ctx, nil)
	if err != nil {
		t.Fatalf("ReleaseProvenance empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty provenance = %#v", empty)
	}
	empty, err = st.ReleaseProvenance(ctx, []int64{-1, 0})
	if err != nil {
		t.Fatalf("ReleaseProvenance negative-only: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("negative-only provenance = %#v", empty)
	}
}

func TestRecordReleaseSourceKeepsBestSwarm(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "best-def")
	first := mustCreateIndexer(t, st, "best-def", "best-one", "Best One")
	second := mustCreateIndexer(t, st, "best-def", "best-two", "Best Two")

	t1 := time.Unix(1_700_000_000, 0)
	release := mustUpsertProvenanceRelease(t, st, "best-swarm", "Best Swarm", t1, t1, 4, 1)

	// A source with a bigger swarm raises the release counters.
	t2 := t1.Add(10 * time.Minute)
	if err := st.RecordReleaseSource(ctx, ReleaseSource{
		ReleaseID: release.ID, IndexerID: first,
		FirstSeenAt: t2, LastSeenAt: t2, Seeders: 20, Leechers: 10,
	}); err != nil {
		t.Fatalf("RecordReleaseSource bigger: %v", err)
	}
	loaded, err := st.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if loaded.Seeders != 20 || loaded.Leechers != 10 {
		t.Fatalf("release swarm = %d/%d, want 20/10", loaded.Seeders, loaded.Leechers)
	}
	if loaded.LastSeenAt.Unix() != t2.Unix() {
		t.Fatalf("release last seen = %v, want %v", loaded.LastSeenAt, t2)
	}

	// A later source with a smaller swarm does not lower the release values,
	// but still advances last_seen_at and keeps its own counters.
	t3 := t2.Add(10 * time.Minute)
	if err := st.RecordReleaseSource(ctx, ReleaseSource{
		ReleaseID: release.ID, IndexerID: first,
		FirstSeenAt: t3, LastSeenAt: t3, Seeders: 3, Leechers: 2,
	}); err != nil {
		t.Fatalf("RecordReleaseSource smaller: %v", err)
	}
	loaded, err = st.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("GetRelease after smaller: %v", err)
	}
	if loaded.Seeders != 20 || loaded.Leechers != 10 {
		t.Fatalf("release swarm lowered to %d/%d", loaded.Seeders, loaded.Leechers)
	}
	if loaded.LastSeenAt.Unix() != t3.Unix() {
		t.Fatalf("release last seen = %v, want %v", loaded.LastSeenAt, t3)
	}
	sources, err := st.ListReleaseSources(ctx, release.ID)
	if err != nil {
		t.Fatalf("ListReleaseSources: %v", err)
	}
	if len(sources) != 1 || sources[0].Seeders != 3 || sources[0].Leechers != 2 {
		t.Fatalf("per-indexer source counters = %#v", sources)
	}
	if sources[0].FirstSeenAt.Unix() != t2.Unix() {
		t.Fatalf("first_seen = %v, want %v", sources[0].FirstSeenAt, t2)
	}

	// Another indexer with more seeders raises only the higher counter.
	t4 := t3.Add(10 * time.Minute)
	if err := st.RecordReleaseSource(ctx, ReleaseSource{
		ReleaseID: release.ID, IndexerID: second,
		FirstSeenAt: t4, LastSeenAt: t4, Seeders: 25, Leechers: 5,
	}); err != nil {
		t.Fatalf("RecordReleaseSource second: %v", err)
	}
	loaded, err = st.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("GetRelease after second: %v", err)
	}
	if loaded.Seeders != 25 || loaded.Leechers != 10 {
		t.Fatalf("release swarm = %d/%d, want 25/10", loaded.Seeders, loaded.Leechers)
	}
}

func TestStaleProvenance(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustCreateDefinition(t, st, "stale-def")
	first := mustCreateIndexer(t, st, "stale-def", "stale-one", "Stale One")
	second := mustCreateIndexer(t, st, "stale-def", "stale-two", "Stale Two")

	t1 := time.Unix(1_700_000_000, 0)
	oldest := mustUpsertProvenanceRelease(t, st, "stale-oldest", "Oldest Release", t1, t1, 1, 0)
	middle := mustUpsertProvenanceRelease(t, st, "stale-middle", "Middle Release", t1, t1, 2, 0)
	newest := mustUpsertProvenanceRelease(t, st, "stale-newest", "Newest Release", t1, t1, 3, 0)

	if err := st.RecordReleaseSource(ctx, ReleaseSource{
		ReleaseID: newest.ID, IndexerID: first, FirstSeenAt: t1, LastSeenAt: t1.Add(3 * time.Hour),
	}); err != nil {
		t.Fatalf("RecordReleaseSource newest: %v", err)
	}
	if err := st.RecordReleaseSource(ctx, ReleaseSource{
		ReleaseID: middle.ID, IndexerID: second, FirstSeenAt: t1, LastSeenAt: t1.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("RecordReleaseSource middle: %v", err)
	}
	if err := st.RecordReleaseSource(ctx, ReleaseSource{
		ReleaseID: oldest.ID, IndexerID: first, FirstSeenAt: t1, LastSeenAt: t1.Add(time.Hour),
	}); err != nil {
		t.Fatalf("RecordReleaseSource oldest: %v", err)
	}

	cutoff := t1.Add(3 * time.Hour)
	stale, err := st.StaleProvenance(ctx, cutoff, 0)
	if err != nil {
		t.Fatalf("StaleProvenance: %v", err)
	}
	if len(stale) != 2 {
		t.Fatalf("stale rows = %d, want 2: %#v", len(stale), stale)
	}
	if stale[0].ReleaseID != oldest.ID || stale[0].IndexerID != first ||
		stale[0].Name != "Stale One" || stale[0].Title != "Oldest Release" {
		t.Errorf("oldest row = %#v", stale[0])
	}
	if stale[1].ReleaseID != middle.ID || stale[1].Name != "Stale Two" ||
		stale[1].Title != "Middle Release" {
		t.Errorf("middle row = %#v", stale[1])
	}
	if stale[0].LastSeenAt.Unix() != t1.Add(time.Hour).Unix() {
		t.Errorf("oldest last seen = %v", stale[0].LastSeenAt)
	}

	// The oldest row comes first and the limit applies after ordering.
	limited, err := st.StaleProvenance(ctx, cutoff, 1)
	if err != nil {
		t.Fatalf("StaleProvenance limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ReleaseID != oldest.ID {
		t.Fatalf("limited stale = %#v", limited)
	}

	// A cutoff before every row is empty.
	none, err := st.StaleProvenance(ctx, t1, 0)
	if err != nil {
		t.Fatalf("StaleProvenance none: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("stale rows before cutoff = %#v", none)
	}

	all, err := st.StaleProvenance(ctx, cutoff.Add(time.Hour), 0)
	if err != nil {
		t.Fatalf("StaleProvenance all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("stale rows after cutoff = %d, want 3", len(all))
	}
}

func TestClampStaleLimit(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, staleProvenanceDefaultLimit},
		{-10, staleProvenanceDefaultLimit},
		{25, 25},
		{staleProvenanceMaxLimit + 1, staleProvenanceMaxLimit},
	}
	for _, test := range tests {
		if got := clampStaleLimit(test.in); got != test.want {
			t.Errorf("clampStaleLimit(%d) = %d, want %d", test.in, got, test.want)
		}
	}
}
