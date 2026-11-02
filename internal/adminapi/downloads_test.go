package adminapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestDownloadConfirmationEndpoint(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true, false)
	release := store.Release{
		DedupKey: "admin-confirm",
		Title:    "Admin Confirm S01E01",
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
		Protocol: "torrent",
	}
	if _, err := e.store.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	if err := e.store.MarkReleaseDownloaded(ctx, release.ID); err != nil {
		t.Fatalf("MarkReleaseDownloaded: %v", err)
	}
	grabID, err := e.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		ReleaseID: release.ID, Kind: "grab", Query: release.Title,
		Status: "magnet", App: "gextto", StartedAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordIndexerHistory: %v", err)
	}

	body := jsonBody(map[string]any{
		"event":        "download.completed",
		"protocol":     "torrent",
		"hash":         strings.ToUpper(release.InfoHash),
		"title":        release.Title,
		"client":       "gextto",
		"size":         123456789,
		"completed_at": time.Now().UTC().Format(time.RFC3339),
	})
	res := e.do(http.MethodPost, "/api/v2/downloads/confirm", body)
	requireStatus(t, res, http.StatusOK)
	reply := decodeJSON[downloadConfirmationResponse](t, res.body)
	if !reply.Matched || reply.GrabID != grabID {
		t.Fatalf("reply = %+v, want matched grab %d", reply, grabID)
	}

	grabs, err := e.store.ListGrabs(ctx, time.Now().Add(-time.Hour), 0, 50, 0)
	if err != nil {
		t.Fatalf("ListGrabs: %v", err)
	}
	if len(grabs) != 1 || grabs[0].ConfirmedAt.IsZero() || grabs[0].ConfirmedBy != "gextto" {
		t.Fatalf("grab = %+v, want confirmed by gextto", grabs)
	}

	// The same report is valid and idempotent.
	again := e.do(http.MethodPost, "/api/v2/downloads/confirm", body)
	requireStatus(t, again, http.StatusOK)
	if reply := decodeJSON[downloadConfirmationResponse](t, again.body); reply.Matched {
		t.Fatalf("repeated confirmation matched again: %+v", reply)
	}
}

func TestDownloadConfirmationValidation(t *testing.T) {
	e := newEnv(t, true, false)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"unknown hash", `{"protocol":"torrent","hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, http.StatusOK},
		{"bad protocol", `{"protocol":"usenet","hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, http.StatusBadRequest},
		{"short torrent hash", `{"protocol":"torrent","hash":"abc"}`, http.StatusBadRequest},
		{"short ed2k hash", `{"protocol":"ed2k","hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, http.StatusBadRequest},
		{"bad event", `{"event":"download.started","protocol":"ed2k","hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, http.StatusBadRequest},
		{"bad timestamp", `{"protocol":"ed2k","hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","completed_at":"yesterday"}`, http.StatusBadRequest},
		{"broken json", `{`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := e.do(http.MethodPost, "/api/v2/downloads/confirm", tc.body)
			requireStatus(t, res, tc.want)
		})
	}

	noKey := e.call(http.MethodPost, "/api/v2/downloads/confirm",
		`{"protocol":"ed2k","hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, "")
	requireStatus(t, noKey, http.StatusUnauthorized)
}

func TestDownloadConfirmationEd2kHash(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true, false)
	release := store.Release{
		DedupKey: "admin-confirm-ed2k",
		Title:    "Admin Confirm S01E01",
		Ed2kHash: "abcdefabcdefabcdefabcdefabcdefab",
		Protocol: "ed2k",
	}
	if _, err := e.store.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	if err := e.store.MarkReleaseDownloaded(ctx, release.ID); err != nil {
		t.Fatalf("MarkReleaseDownloaded: %v", err)
	}
	grabID, err := e.store.RecordIndexerHistory(ctx, store.IndexerHistory{
		ReleaseID: release.ID, Kind: "grab", Query: release.Title,
		Status: "ed2k", App: "gextto", StartedAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordIndexerHistory: %v", err)
	}
	res := e.do(http.MethodPost, "/api/v2/downloads/confirm", jsonBody(map[string]any{
		"protocol": "ed2k",
		"hash":     strings.ToUpper(release.Ed2kHash),
		"client":   "gextto",
	}))
	requireStatus(t, res, http.StatusOK)
	if reply := decodeJSON[downloadConfirmationResponse](t, res.body); !reply.Matched || reply.GrabID != grabID {
		t.Fatalf("reply = %+v, want matched grab %d", reply, grabID)
	}
}
