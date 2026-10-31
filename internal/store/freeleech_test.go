package store

import (
	"context"
	"testing"
	"time"
)

func TestFreeleechRoundTripKeepsUnknownAndOverridesKnown(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	release := Release{
		DedupKey:       "freeleech-roundtrip",
		Title:          "Free.Release.2026.1080p",
		SearchTitle:    NormalizeSearch("Free.Release.2026.1080p"),
		Size:           1_000,
		Seeders:        10,
		Leechers:       2,
		Freeleech:      true,
		FreeleechKnown: true,
		FirstSeenAt:    now,
		LastSeenAt:     now,
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	got, found, err := st.ReleaseByKey(ctx, release.DedupKey)
	if err != nil || !found {
		t.Fatalf("ReleaseByKey = %+v, %v, %v", got, found, err)
	}
	if !got.Freeleech || !got.FreeleechKnown {
		t.Fatalf("free release = free:%v known:%v, want true/true", got.Freeleech, got.FreeleechKnown)
	}

	// A later poll without the field (and with unknown swarm counts) must not
	// erase what a previous poll knew: this is the metadata-preservation rule
	// the announce-driven indexers learned the hard way.
	update := Release{
		DedupKey:    release.DedupKey,
		Title:       release.Title,
		SearchTitle: release.SearchTitle,
		Size:        release.Size,
		Seeders:     -1,
		Leechers:    -1,
		FirstSeenAt: now,
		LastSeenAt:  now.Add(time.Minute),
	}
	if _, err := st.UpsertRelease(ctx, &update); err != nil {
		t.Fatalf("UpsertRelease(update): %v", err)
	}
	got, _, err = st.ReleaseByKey(ctx, release.DedupKey)
	if err != nil {
		t.Fatalf("ReleaseByKey(update): %v", err)
	}
	if !got.Freeleech || !got.FreeleechKnown {
		t.Fatalf("unknown update erased freeleech: free:%v known:%v", got.Freeleech, got.FreeleechKnown)
	}
	if got.Seeders != 10 || got.Leechers != 2 {
		t.Fatalf("unknown swarm erased the known counts: %d/%d", got.Seeders, got.Leechers)
	}

	// A source that explicitly says "paid" must win over the old free value.
	paid := update
	paid.Seeders = 11
	paid.Leechers = 3
	paid.Freeleech = false
	paid.FreeleechKnown = true
	if _, err := st.UpsertRelease(ctx, &paid); err != nil {
		t.Fatalf("UpsertRelease(paid): %v", err)
	}
	got, _, err = st.ReleaseByKey(ctx, release.DedupKey)
	if err != nil {
		t.Fatalf("ReleaseByKey(paid): %v", err)
	}
	if got.Freeleech || !got.FreeleechKnown {
		t.Fatalf("paid update = free:%v known:%v, want false/true", got.Freeleech, got.FreeleechKnown)
	}
}

func TestPruneReleasesKeepsDownloadedAndFavorites(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	makeRelease := func(key string) Release {
		rel := Release{
			DedupKey: key, Title: key, SearchTitle: NormalizeSearch(key),
			FirstSeenAt: now, LastSeenAt: now,
		}
		if _, err := st.UpsertRelease(ctx, &rel); err != nil {
			t.Fatalf("UpsertRelease(%s): %v", key, err)
		}
		if _, err := st.DB().ExecContext(ctx,
			`UPDATE releases SET last_seen_at = ? WHERE id = ?`, now.AddDate(0, 0, -30).Unix(), rel.ID); err != nil {
			t.Fatalf("age %s: %v", key, err)
		}
		return rel
	}
	downloaded := makeRelease("downloaded")
	favorite := makeRelease("favorite")
	plain := makeRelease("plain")
	if err := st.MarkReleaseDownloaded(ctx, downloaded.ID); err != nil {
		t.Fatalf("MarkReleaseDownloaded: %v", err)
	}
	if _, err := st.SetReleaseFlags(ctx, favorite.ID, true, false); err != nil {
		t.Fatalf("SetReleaseFlags: %v", err)
	}

	removed, err := st.PruneReleases(ctx, now.AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("PruneReleases: %v", err)
	}
	if removed != 1 {
		t.Fatalf("PruneReleases removed %d, want only the plain release", removed)
	}
	for _, key := range []string{downloaded.DedupKey, favorite.DedupKey} {
		if _, found, err := st.ReleaseByKey(ctx, key); err != nil || !found {
			t.Errorf("ReleaseByKey(%s) = found:%v err:%v, want kept", key, found, err)
		}
	}
	if _, found, _ := st.ReleaseByKey(ctx, plain.DedupKey); found {
		t.Errorf("plain release was kept by PruneReleases")
	}
}
