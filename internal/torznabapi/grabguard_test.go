package torznabapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func linkReleaseIndexer(t *testing.T, st *store.Store, suffix string, releaseID int64) int64 {
	t.Helper()
	ctx := context.Background()
	definitionID := "guard-def-" + suffix
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: definitionID, SchemaVersion: 1, State: "verificata", Origin: "local",
		Body: "schema: 1\n", Hash: "hash-" + suffix, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	indexerID, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: definitionID,
		Protocol:     "torrent",
		Slug:         "guard-indexer-" + suffix,
		Name:         "Guard Indexer " + suffix,
		Enabled:      true,
		ConfigJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	if err := st.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: releaseID, IndexerID: indexerID,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordReleaseSource: %v", err)
	}
	return indexerID
}

func TestGrabGuardRejectsOverLimit(t *testing.T) {
	api, st := newAPI(t)
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	id := insertRelease(t, st, "Guarded Show 1080p", "5040", hash, "", []byte("d4:infod4:name4:testee"))
	indexerID := linkReleaseIndexer(t, st, "reject", id)

	checks := 0
	records := 0
	api.SetGrabGuard(func(_ context.Context, got int64) error {
		checks++
		if got != indexerID {
			t.Errorf("check indexer = %d, want %d", got, indexerID)
		}
		return errors.New("over limit")
	}, func(context.Context, int64, int64, string) error {
		records++
		return nil
	})

	for _, target := range []string{
		"/dl/" + itoa(id) + ".torrent?apikey=secret",
		"/api?t=get&id=" + itoa(id) + "&apikey=secret",
	} {
		recorder := do(t, api, target)
		if recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("%s status = %d, want 429", target, recorder.Code)
		}
		if got := recorder.Header().Get("Retry-After"); got != "3600" {
			t.Errorf("%s Retry-After = %q, want 3600", target, got)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, `<error code="400"`) || !strings.Contains(body, "grab limit reached") {
			t.Errorf("%s body = %s", target, body)
		}
	}
	if checks != 2 {
		t.Errorf("checks = %d, want 2", checks)
	}
	if records != 0 {
		t.Errorf("records = %d, want 0 for rejected grabs", records)
	}
}

func TestGrabGuardRecordsSuccessfulGetButNotHead(t *testing.T) {
	api, st := newAPI(t)
	hash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	id := insertRelease(t, st, "Recorded Show 1080p", "5040", hash, "", []byte("d4:infod4:name4:testee"))
	indexerID := linkReleaseIndexer(t, st, "record", id)

	checks := 0
	type recorded struct {
		indexerID int64
		releaseID int64
		title     string
	}
	var records []recorded
	api.SetGrabGuard(func(context.Context, int64) error {
		checks++
		return nil
	}, func(_ context.Context, gotIndexer, gotRelease int64, title string) error {
		records = append(records, recorded{indexerID: gotIndexer, releaseID: gotRelease, title: title})
		return nil
	})

	download := do(t, api, "/dl/"+itoa(id)+".torrent?apikey=secret")
	if download.Code != http.StatusOK {
		t.Fatalf("download status = %d: %s", download.Code, download.Body.String())
	}
	if checks != 1 {
		t.Errorf("checks after GET = %d, want 1", checks)
	}
	if len(records) != 1 {
		t.Fatalf("records after GET = %d, want 1", len(records))
	}
	if records[0].indexerID != indexerID || records[0].releaseID != id ||
		records[0].title != "Recorded Show 1080p" {
		t.Fatalf("recorded = %+v", records[0])
	}

	head := httptest.NewRequest(http.MethodHead, "/dl/"+itoa(id)+".torrent?apikey=secret", nil)
	headRecorder := httptest.NewRecorder()
	api.ServeHTTP(headRecorder, head)
	if headRecorder.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d", headRecorder.Code)
	}
	if headRecorder.Body.Len() != 0 {
		t.Errorf("HEAD body = %d bytes, want 0", headRecorder.Body.Len())
	}
	if checks != 2 {
		t.Errorf("checks after HEAD = %d, want 2 (HEAD is checked)", checks)
	}
	if len(records) != 1 {
		t.Errorf("records after HEAD = %d, want 1 (HEAD is not a grab)", len(records))
	}
}

func TestGrabGuardWithoutIndexerDoesNotBlock(t *testing.T) {
	api, st := newAPI(t)
	hash := "cccccccccccccccccccccccccccccccccccccccc"
	id := insertRelease(t, st, "Unattributed Show 1080p", "5040", hash, "", []byte("d4:infod4:name4:testee"))

	checks := 0
	records := 0
	api.SetGrabGuard(func(context.Context, int64) error {
		checks++
		return errors.New("limit")
	}, func(context.Context, int64, int64, string) error {
		records++
		return nil
	})

	download := do(t, api, "/dl/"+itoa(id)+".torrent?apikey=secret")
	if download.Code != http.StatusOK {
		t.Fatalf("download without provenance = %d: %s", download.Code, download.Body.String())
	}
	if checks != 0 || records != 0 {
		t.Errorf("checks = %d, records = %d, want 0/0 without an indexer", checks, records)
	}
}

func TestGrabGuardNilCallbacksKeepBehaviour(t *testing.T) {
	api, st := newAPI(t)
	hash := "dddddddddddddddddddddddddddddddddddddddd"
	id := insertRelease(t, st, "Plain Show 1080p", "5040", hash, "", []byte("d4:infod4:name4:testee"))
	linkReleaseIndexer(t, st, "nil", id)

	download := do(t, api, "/dl/"+strconv.FormatInt(id, 10)+".torrent?apikey=secret")
	if download.Code != http.StatusOK {
		t.Fatalf("download without guard = %d: %s", download.Code, download.Body.String())
	}
}
