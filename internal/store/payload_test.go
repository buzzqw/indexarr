package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func payloadTestRelease(key, protocol string) *Release {
	now := time.Now().Add(-time.Hour)
	return &Release{
		DedupKey:    key,
		Title:       "Payload " + key,
		SearchTitle: NormalizeSearch("Payload " + key),
		Size:        100,
		Category:    "5040",
		Protocol:    protocol,
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
}

func TestMigration2AddsPayloadColumnsToLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	seeded, err := Open(path)
	if err != nil {
		t.Fatalf("Open seeded: %v", err)
	}
	release := payloadTestRelease("native-release", "torrent")
	if _, err := seeded.UpsertRelease(ctx, release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	if err := seeded.Close(); err != nil {
		t.Fatalf("Close seeded: %v", err)
	}

	// Turn the database back into a version-1 one: drop the three new columns
	// and forget migration 2.
	raw, err := sql.Open("sqlite", "file:"+dsnPath(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	for _, statement := range []string{
		`ALTER TABLE releases DROP COLUMN protocol`,
		`ALTER TABLE releases DROP COLUMN payload`,
		`ALTER TABLE releases DROP COLUMN payload_type`,
		`DELETE FROM schema_migrations WHERE version = 2`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			_ = raw.Close()
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated: %v", err)
	}
	defer migrated.Close()

	version, err := migrated.schemaVersion(ctx)
	if err != nil {
		t.Fatalf("schemaVersion: %v", err)
	}
	if version != indexarr2SeedersCheckedVersion {
		t.Fatalf("schema version = %d, want %d", version, indexarr2ActivityVersion)
	}
	for _, column := range []string{"protocol", "payload", "payload_type"} {
		exists, err := migrated.columnExists("releases", column)
		if err != nil {
			t.Fatalf("columnExists(%s): %v", column, err)
		}
		if !exists {
			t.Fatalf("releases.%s was not added", column)
		}
	}

	stored, err := migrated.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("GetRelease after migration: %v", err)
	}
	if stored.Title != release.Title {
		t.Errorf("title after migration = %q", stored.Title)
	}
	if stored.Protocol != "torrent" {
		t.Errorf("protocol after migration = %q, want torrent", stored.Protocol)
	}
	if len(stored.Payload) != 0 {
		t.Errorf("payload after migration = %d bytes, want none", len(stored.Payload))
	}
}

func TestUpsertReleaseRoundTripsNZBPayloadAndPreservesIt(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	nzb := []byte(`<?xml version="1.0"?><nzb><file/></nzb>`)
	release := payloadTestRelease("usenet-payload", "usenet")
	release.Payload = nzb
	release.PayloadType = "application/x-nzb"
	release.GUID = "guid-123"
	if _, err := st.UpsertRelease(ctx, release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}

	stored, err := st.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if stored.Protocol != "usenet" {
		t.Errorf("protocol = %q", stored.Protocol)
	}
	if stored.PayloadType != "application/x-nzb" {
		t.Errorf("payload type = %q", stored.PayloadType)
	}
	if string(stored.Payload) != string(nzb) {
		t.Errorf("payload differs: %q", stored.Payload)
	}
	if len(stored.Torrent) != 0 {
		t.Errorf("torrent = %d bytes, want none", len(stored.Torrent))
	}

	// A later update that omits the payload, the protocol and the payload type
	// must keep all three.
	update := payloadTestRelease("usenet-payload", "")
	update.Title = "Renamed release"
	update.SearchTitle = NormalizeSearch(update.Title)
	if _, err := st.UpsertRelease(ctx, update); err != nil {
		t.Fatalf("UpsertRelease update: %v", err)
	}
	stored, err = st.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("GetRelease after update: %v", err)
	}
	if stored.Title != "Renamed release" {
		t.Errorf("title after update = %q", stored.Title)
	}
	if string(stored.Payload) != string(nzb) {
		t.Errorf("payload was not preserved: %q", stored.Payload)
	}
	if stored.PayloadType != "application/x-nzb" {
		t.Errorf("payload type after update = %q", stored.PayloadType)
	}
	if stored.Protocol != "usenet" {
		t.Errorf("protocol after update = %q, want usenet", stored.Protocol)
	}

	// An explicit non-empty payload type still updates the row.
	update = payloadTestRelease("usenet-payload", "usenet")
	update.PayloadType = "application/x-nzb; charset=utf-8"
	if _, err := st.UpsertRelease(ctx, update); err != nil {
		t.Fatalf("UpsertRelease payload type update: %v", err)
	}
	stored, err = st.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("GetRelease after payload type update: %v", err)
	}
	if stored.PayloadType != "application/x-nzb; charset=utf-8" {
		t.Errorf("payload type after explicit update = %q", stored.PayloadType)
	}
	if string(stored.Payload) != string(nzb) {
		t.Errorf("payload after explicit type update = %q", stored.Payload)
	}
}

func TestPayloadByReleaseIDForTorrentAndUsenet(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	torrent := []byte("d4:infod4:name4:testee")
	torrentRelease := payloadTestRelease("torrent-payload", "")
	torrentRelease.Torrent = torrent
	if _, err := st.UpsertRelease(ctx, torrentRelease); err != nil {
		t.Fatalf("UpsertRelease torrent: %v", err)
	}
	body, contentType, protocol, err := st.PayloadByReleaseID(ctx, torrentRelease.ID)
	if err != nil {
		t.Fatalf("PayloadByReleaseID torrent: %v", err)
	}
	if string(body) != string(torrent) {
		t.Errorf("torrent body = %q", body)
	}
	if contentType != "application/x-bittorrent" {
		t.Errorf("torrent content type = %q", contentType)
	}
	if protocol != "torrent" {
		t.Errorf("torrent protocol = %q", protocol)
	}

	nzb := []byte("<nzb/>")
	nzbRelease := payloadTestRelease("nzb-payload", "usenet")
	nzbRelease.Payload = nzb
	nzbRelease.PayloadType = "application/x-nzb"
	if _, err := st.UpsertRelease(ctx, nzbRelease); err != nil {
		t.Fatalf("UpsertRelease nzb: %v", err)
	}
	body, contentType, protocol, err = st.PayloadByReleaseID(ctx, nzbRelease.ID)
	if err != nil {
		t.Fatalf("PayloadByReleaseID nzb: %v", err)
	}
	if string(body) != string(nzb) {
		t.Errorf("nzb body = %q", body)
	}
	if contentType != "application/x-nzb" {
		t.Errorf("nzb content type = %q", contentType)
	}
	if protocol != "usenet" {
		t.Errorf("nzb protocol = %q", protocol)
	}

	metadata := payloadTestRelease("no-payload", "usenet")
	if _, err := st.UpsertRelease(ctx, metadata); err != nil {
		t.Fatalf("UpsertRelease metadata: %v", err)
	}
	if _, _, _, err := st.PayloadByReleaseID(ctx, metadata.ID); !errors.Is(err, ErrNoPayload) {
		t.Errorf("missing payload error = %v, want ErrNoPayload", err)
	}
}

func TestSavePayloadCachesTorrentAndUsenet(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	torrentRelease := payloadTestRelease("save-torrent", "torrent")
	if _, err := st.UpsertRelease(ctx, torrentRelease); err != nil {
		t.Fatalf("UpsertRelease torrent: %v", err)
	}
	if err := st.SavePayload(ctx, torrentRelease.ID, []byte("torrent-bytes"), "", "torrent"); err != nil {
		t.Fatalf("SavePayload torrent: %v", err)
	}
	stored, err := st.GetRelease(ctx, torrentRelease.ID)
	if err != nil {
		t.Fatalf("GetRelease torrent: %v", err)
	}
	if string(stored.Torrent) != "torrent-bytes" || stored.TorrentSize != int64(len("torrent-bytes")) {
		t.Errorf("saved torrent = %q size %d", stored.Torrent, stored.TorrentSize)
	}

	usenetRelease := payloadTestRelease("save-usenet", "usenet")
	if _, err := st.UpsertRelease(ctx, usenetRelease); err != nil {
		t.Fatalf("UpsertRelease usenet: %v", err)
	}
	if err := st.SavePayload(ctx, usenetRelease.ID, []byte("nzb-bytes"), "", "usenet"); err != nil {
		t.Fatalf("SavePayload usenet: %v", err)
	}
	stored, err = st.GetRelease(ctx, usenetRelease.ID)
	if err != nil {
		t.Fatalf("GetRelease usenet: %v", err)
	}
	if string(stored.Payload) != "nzb-bytes" || stored.PayloadType != "application/x-nzb" {
		t.Errorf("saved payload = %q type %q", stored.Payload, stored.PayloadType)
	}
}
