package torznabapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/store"
)

func insertUsenetRelease(t *testing.T, st *store.Store, title string, payload []byte) int64 {
	t.Helper()
	release := store.Release{
		DedupKey:    "usenet-" + title,
		Title:       title,
		SearchTitle: store.NormalizeSearch(title),
		Size:        4242,
		Category:    "5030",
		Protocol:    "usenet",
		Payload:     payload,
		PayloadType: "application/x-nzb",
		PublishedAt: time.Now().Add(-time.Hour),
		GUID:        "guid-" + title,
		FirstSeenAt: time.Now().Add(-time.Hour),
		LastSeenAt:  time.Now(),
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	return release.ID
}

func TestNewznabItemAndDownload(t *testing.T) {
	api, st := newAPI(t)
	nzb := []byte(`<?xml version="1.0"?><nzb><file subject="test"/></nzb>`)
	id := insertUsenetRelease(t, st, "Usenet Movie 1080p", nzb)

	recorder := do(t, api, "/api?t=search&apikey=secret&q=Usenet")
	if recorder.Code != http.StatusOK {
		t.Fatalf("search status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`<link>http://example.com/dl/` + itoa(id) + `.nzb?apikey=secret</link>`,
		`<enclosure url="http://example.com/dl/` + itoa(id) + `.nzb?apikey=secret" length="4242" type="application/x-nzb"/>`,
		`<torznab:attr name="protocol" value="usenet"/>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
	for _, unwanted := range []string{`type="application/x-bittorrent"`, `magneturl`, `magnet:?`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("usenet item contains %q\n%s", unwanted, body)
		}
	}

	download := do(t, api, "/dl/"+itoa(id)+".nzb?apikey=secret")
	if download.Code != http.StatusOK {
		t.Fatalf("nzb download status = %d: %s", download.Code, download.Body.String())
	}
	if download.Body.String() != string(nzb) {
		t.Errorf("downloaded body differs")
	}
	if contentType := download.Header().Get("Content-Type"); contentType != "application/x-nzb" {
		t.Errorf("content type = %q", contentType)
	}
	if disposition := download.Header().Get("Content-Disposition"); !strings.HasSuffix(disposition, `.nzb"`) ||
		!strings.Contains(disposition, "Usenet Movie 1080p.nzb") {
		t.Errorf("content disposition = %q, want the title plus .nzb", disposition)
	}

	byGet := do(t, api, "/api?t=get&id="+itoa(id)+"&apikey=secret")
	if byGet.Code != http.StatusOK || byGet.Body.String() != string(nzb) {
		t.Errorf("t=get nzb = %d: %s", byGet.Code, byGet.Body.String())
	}
	if contentType := byGet.Header().Get("Content-Type"); contentType != "application/x-nzb" {
		t.Errorf("t=get content type = %q", contentType)
	}

	head := httptest.NewRequest(http.MethodHead, "/dl/"+itoa(id)+".nzb?apikey=secret", nil)
	headRecorder := httptest.NewRecorder()
	api.ServeHTTP(headRecorder, head)
	if headRecorder.Code != http.StatusOK || headRecorder.Body.Len() != 0 {
		t.Errorf("HEAD nzb = %d, body %d", headRecorder.Code, headRecorder.Body.Len())
	}
	if got := headRecorder.Header().Get("Content-Length"); got != itoa(int64(len(nzb))) {
		t.Errorf("HEAD content length = %q", got)
	}

	missing := do(t, api, "/api?t=get&id=99999&apikey=secret")
	if missing.Code != http.StatusNotFound {
		t.Errorf("missing nzb status = %d", missing.Code)
	}
}

func TestTorrentItemOutputUnchanged(t *testing.T) {
	api, st := newAPI(t)
	hash := "0123456789abcdef0123456789abcdef01234567"
	id := insertRelease(t, st, "Torrent Show 1080p", "5040", hash,
		"magnet:?xt=urn:btih:"+hash, []byte("d4:infod4:name4:testee"))

	body := do(t, api, "/api?t=search&apikey=secret&q=Torrent").Body.String()
	for _, want := range []string{
		`<link>http://example.com/dl/` + itoa(id) + `.torrent?apikey=secret</link>`,
		`type="application/x-bittorrent"`,
		`<torznab:attr name="infohash" value="` + hash + `"`,
		`<torznab:attr name="magneturl" value="magnet:?xt=urn:btih:` + hash + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("torrent body missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, `name="protocol"`) || strings.Contains(body, "application/x-nzb") {
		t.Errorf("torrent item gained usenet output:\n%s", body)
	}
}

func TestDownloadResolverFetchesAndCachesOpaquePayload(t *testing.T) {
	api, st := newAPI(t)
	release := store.Release{
		DedupKey:    "opaque-usenet",
		Title:       "Opaque Release",
		SearchTitle: store.NormalizeSearch("Opaque Release"),
		Size:        77,
		Category:    "5030",
		Protocol:    "usenet",
		PublishedAt: time.Now().Add(-time.Hour),
		FirstSeenAt: time.Now().Add(-time.Hour),
		LastSeenAt:  time.Now(),
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}

	nzb := []byte(`<?xml version="1.0"?><nzb><file subject="opaque"/></nzb>`)
	calls := 0
	api.SetDownloadResolver(func(ctx context.Context, releaseID int64) ([]byte, string, string, error) {
		calls++
		if releaseID != release.ID {
			return nil, "", "", errors.New("wrong release")
		}
		// Mimic downloadproxy: cache the body on the release before serving it.
		if err := st.SavePayload(ctx, releaseID, nzb, "application/x-nzb", "usenet"); err != nil {
			return nil, "", "", err
		}
		return nzb, "application/x-nzb", "Opaque Release.nzb", nil
	})

	search := do(t, api, "/api?t=search&apikey=secret&q=Opaque")
	if !strings.Contains(search.Body.String(), "/dl/"+itoa(release.ID)+".nzb?apikey=secret") {
		t.Errorf("opaque item has no nzb link:\n%s", search.Body.String())
	}

	first := do(t, api, "/dl/"+itoa(release.ID)+".nzb?apikey=secret")
	if first.Code != http.StatusOK || first.Body.String() != string(nzb) {
		t.Fatalf("resolved download = %d: %s", first.Code, first.Body.String())
	}
	if got := first.Header().Get("Content-Disposition"); !strings.Contains(got, "Opaque Release.nzb") {
		t.Errorf("content disposition = %q", got)
	}
	if calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", calls)
	}

	second := do(t, api, "/dl/"+itoa(release.ID)+".nzb?apikey=secret")
	if second.Code != http.StatusOK || second.Body.String() != string(nzb) {
		t.Fatalf("cached download = %d: %s", second.Code, second.Body.String())
	}
	if calls != 1 {
		t.Errorf("resolver called again after caching: %d", calls)
	}

	byGet := do(t, api, "/api?t=get&id="+itoa(release.ID)+"&apikey=secret")
	if byGet.Code != http.StatusOK || byGet.Body.String() != string(nzb) {
		t.Errorf("t=get after caching = %d: %s", byGet.Code, byGet.Body.String())
	}
}

func TestMissingNZBWithoutResolverIs404(t *testing.T) {
	api, st := newAPI(t)
	release := store.Release{
		DedupKey:    "missing-usenet",
		Title:       "Missing Release",
		SearchTitle: store.NormalizeSearch("Missing Release"),
		Category:    "5030",
		Protocol:    "usenet",
		FirstSeenAt: time.Now(),
		LastSeenAt:  time.Now(),
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	if recorder := do(t, api, "/dl/"+itoa(release.ID)+".nzb?apikey=secret"); recorder.Code != http.StatusNotFound {
		t.Errorf("missing nzb status = %d, want 404", recorder.Code)
	}
	if recorder := do(t, api, "/api?t=get&id="+itoa(release.ID)+"&apikey=secret"); recorder.Code != http.StatusNotFound {
		t.Errorf("missing nzb t=get status = %d, want 404", recorder.Code)
	}
}

func TestNewznabCapsAdvertiseUsenetModes(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: "usenet", SchemaVersion: 1, Family: "newznab", State: "verificata",
		Origin: "local", Author: "test", Body: "schema: 1", Hash: "hash",
	}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "usenet", Slug: "nzb", Name: "NZB", Enabled: true, Protocol: "usenet",
	})
	if err != nil {
		t.Fatal(err)
	}
	api.SetCapabilities(func(context.Context, []int64) (map[int64]engine.Capabilities, error) {
		return map[int64]engine.Capabilities{
			id: {
				Protocol: engine.ProtocolUsenet,
				Modes: []engine.Mode{
					engine.ModeSearch, engine.ModeTV, engine.ModeMovie,
					engine.ModeMusic, engine.ModeBook,
				},
				Params: map[engine.Mode][]string{
					engine.ModeSearch: {engine.ParamQuery},
					engine.ModeTV:     {engine.ParamQuery, engine.ParamSeason, engine.ParamEpisode},
					engine.ModeMovie:  {engine.ParamQuery, engine.ParamIMDbID},
					engine.ModeMusic:  {engine.ParamQuery, engine.ParamArtist, engine.ParamAlbum},
					engine.ModeBook:   {engine.ParamQuery, engine.ParamAuthor, engine.ParamTitle},
				},
				Categories: []int{5000, 7000},
			},
		}, nil
	})

	for _, path := range []string{"/api?t=caps", "/torznab/nzb/api?t=caps"} {
		body := do(t, api, path).Body.String()
		for _, want := range []string{
			`<search available="yes" supportedParams="q"/>`,
			`<movie-search available="yes" supportedParams="q,imdbid"/>`,
			`<music-search available="yes" supportedParams="q,artist,album"/>`,
			`<book-search available="yes" supportedParams="q,author,title"/>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q\n%s", path, want, body)
			}
		}
	}
}

func TestNewznabAlias(t *testing.T) {
	api, st := newAPI(t)
	nzb := []byte("<nzb/>")
	id := insertUsenetRelease(t, st, "Alias Release", nzb)

	caps := do(t, api, "/newznab/api?t=caps")
	if caps.Code != http.StatusOK || !strings.Contains(caps.Body.String(), "<caps>") {
		t.Errorf("newznab caps = %d: %s", caps.Code, caps.Body.String())
	}
	search := do(t, api, "/newznab/api?t=search&apikey=secret&q=Alias")
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), "Alias Release") {
		t.Errorf("newznab search = %d: %s", search.Code, search.Body.String())
	}
	download := do(t, api, "/newznab/api?t=get&id="+itoa(id)+"&apikey=secret")
	if download.Code != http.StatusOK || download.Body.String() != string(nzb) {
		t.Errorf("newznab get = %d: %s", download.Code, download.Body.String())
	}
}
