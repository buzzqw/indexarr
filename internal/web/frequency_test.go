package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func seedFrequencyIndexer(t *testing.T, env *nativeEnv, slug string, interval int, config string) int64 {
	t.Helper()
	id, err := env.store.CreateIndexer(context.Background(), store.Indexer{
		DefinitionID:    "test-native",
		Protocol:        "usenet",
		Slug:            slug,
		Name:            slug,
		Enabled:         true,
		Priority:        25,
		ConfigJSON:      config,
		IntervalSeconds: interval,
	})
	if err != nil {
		t.Fatalf("CreateIndexer(%s): %v", slug, err)
	}
	return id
}

// TestFrequencyIsVisibleOutsideAdvanced checks the request behind the change:
// the per-source interval must be a normal field, not a value hidden in the
// Advanced block, and it must keep the seconds value the API stores.
func TestFrequencyIsVisibleOutsideAdvanced(t *testing.T) {
	env := newNativeEnv(t)
	id := seedFrequencyIndexer(t, env, "frequency-form", 7200, `{"url":"https://example.invalid/api"}`)

	body := get(t, env.handler, "/indexers/"+strconv.FormatInt(id, 10)+"/edit").Body.String()
	visible := stripDetails(body)
	for _, want := range []string{
		`name="interval_seconds"`,
		`value="7200"`,
		`data-interval-picker`,
		"every 2 h",
		"This frequency applies to this source only",
	} {
		if !strings.Contains(visible, want) {
			t.Errorf("the frequency field outside Advanced is missing %q", want)
		}
	}
	if strings.Count(body, `name="interval_seconds"`) != 1 {
		t.Error("the interval field is rendered more than once")
	}
	for _, want := range []string{`value="3600"`, `value="86400"`, `value="custom"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the quick choices are missing %q", want)
		}
	}
}

// TestFrequencyListShowsEffectiveValue checks that the list shows the cadence
// that is actually in force, including the global default when the source has
// no value of its own.
func TestFrequencyListShowsEffectiveValue(t *testing.T) {
	env := newNativeEnv(t)
	seedFrequencyIndexer(t, env, "frequency-own", 7200, `{"url":"https://example.invalid/api"}`)
	seedFrequencyIndexer(t, env, "frequency-global", 0, `{"url":"https://example.invalid/api"}`)

	body := get(t, env.handler, "/indexers").Body.String()
	for _, want := range []string{"every 2 h", "set for this source", "every 1 h", "global default"} {
		if !strings.Contains(body, want) {
			t.Errorf("the indexer list is missing %q", want)
		}
	}
}

// TestReduceFrequencyDoublesOneSource checks the one-click action: it doubles
// the interval in force for that source and leaves every other source alone.
func TestReduceFrequencyDoublesOneSource(t *testing.T) {
	env := newNativeEnv(t)
	keep := seedFrequencyIndexer(t, env, "freq-keep", 3600, `{"url":"https://example.invalid/api"}`)
	target := seedFrequencyIndexer(t, env, "freq-target", 7200, `{"url":"https://example.invalid/api"}`)

	token, cookie := csrfFrom(t, env.handler, "/indexers")
	recorder := postForm(t, env.handler, "/indexers/"+strconv.FormatInt(target, 10)+"/frequency",
		url.Values{"csrf": {token}, "return": {"/indexers"}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("reduce status = %d: %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.HasPrefix(location, "/indexers?") {
		t.Fatalf("reduce redirect = %q, want the list", location)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("Location parse: %v", err)
	}
	if msg := parsed.Query().Get("msg"); !strings.Contains(msg, "reduced") || !strings.Contains(msg, "every 4 h") {
		t.Errorf("confirmation = %q, want the new frequency", msg)
	}

	changed, err := env.store.GetIndexer(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if changed.IntervalSeconds != 14400 {
		t.Errorf("target interval = %d, want 14400", changed.IntervalSeconds)
	}
	untouched, err := env.store.GetIndexer(context.Background(), keep)
	if err != nil {
		t.Fatal(err)
	}
	if untouched.IntervalSeconds != 3600 {
		t.Errorf("the other source changed to %d", untouched.IntervalSeconds)
	}
}

// TestReduceFrequencyUsesGlobalDefault checks the first click on a source that
// never had its own interval: the global default is doubled and stored as the
// source's own value.
func TestReduceFrequencyUsesGlobalDefault(t *testing.T) {
	env := newNativeEnv(t)
	settings, err := env.store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.DefaultIntervalSeconds = 1800
	if err := env.store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	target := seedFrequencyIndexer(t, env, "freq-default", 0, `{"url":"https://example.invalid/api"}`)

	token, cookie := csrfFrom(t, env.handler, "/indexers/"+strconv.FormatInt(target, 10))
	recorder := postForm(t, env.handler, "/indexers/"+strconv.FormatInt(target, 10)+"/frequency",
		url.Values{"csrf": {token}, "return": {"/indexers/" + strconv.FormatInt(target, 10)}}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("reduce status = %d: %s", recorder.Code, recorder.Body.String())
	}
	changed, err := env.store.GetIndexer(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if changed.IntervalSeconds != 3600 {
		t.Errorf("interval = %d, want 3600 (double the 1800 global default)", changed.IntervalSeconds)
	}
}

// TestReduceFrequencyCapsAndRejectsOpenRedirect covers the upper bound and the
// fact that the return field cannot point outside indexarr.
func TestReduceFrequencyCapsAndRejectsOpenRedirect(t *testing.T) {
	env := newNativeEnv(t)
	id := seedFrequencyIndexer(t, env, "freq-capped", 0, `{"url":"https://example.invalid/api"}`)
	token, cookie := csrfFrom(t, env.handler, "/indexers")

	recorder := postForm(t, env.handler, "/indexers/"+strconv.FormatInt(id, 10)+"/frequency",
		url.Values{"csrf": {token}, "return": {"https://evil.invalid/"}}, cookie)
	location := recorder.Header().Get("Location")
	if strings.Contains(location, "evil.invalid") {
		t.Fatalf("open redirect accepted: %q", location)
	}
	if !strings.HasPrefix(location, "/indexers/"+strconv.FormatInt(id, 10)) {
		t.Fatalf("fallback redirect = %q", location)
	}

	// A source already at the 30-day ceiling gets a message, not a new value.
	capped := seedFrequencyIndexer(t, env, "freq-ceiling", 30*24*3600, `{"url":"https://example.invalid/api"}`)
	token, cookie = csrfFrom(t, env.handler, "/indexers")
	recorder = postForm(t, env.handler, "/indexers/"+strconv.FormatInt(capped, 10)+"/frequency",
		url.Values{"csrf": {token}}, cookie)
	parsed, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if errText := parsed.Query().Get("err"); !strings.Contains(errText, "30 days") {
		t.Errorf("ceiling message = %q", errText)
	}
	unchanged, err := env.store.GetIndexer(context.Background(), capped)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.IntervalSeconds != 30*24*3600 {
		t.Errorf("interval changed at the ceiling: %d", unchanged.IntervalSeconds)
	}
}

// TestRateLimitProblemFromLiveHistory covers the production path: a live app
// search got a 429 and no scheduled run has run yet, so the state is empty and
// the sentence comes from the history.
func TestRateLimitProblemFromLiveHistory(t *testing.T) {
	env := newNativeEnv(t)
	id := seedFrequencyIndexer(t, env, "freq-429-live", 3600, `{"url":"https://example.invalid/api"}`)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := env.store.RecordIndexerHistory(ctx, store.IndexerHistory{
			IndexerID: id,
			Kind:      "search",
			Status:    "error",
			Error:     "indexer freq-429-live: search: request failed: engine: rate limited by indexer",
			StartedAt: time.Now().Add(-time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("RecordIndexerHistory: %v", err)
		}
	}
	body := get(t, env.handler, "/indexers").Body.String()
	if !strings.Contains(body, `action="/indexers/`+strconv.FormatInt(id, 10)+`/frequency"`) {
		t.Errorf("a live 429 does not offer the per-source action: %s", body)
	}
}

// TestRateLimitProblemHasPerSourceAction checks that the 429 sentence no longer
// sends the operator to the global Settings page: the remedy is a POST bound to
// that one source.
func TestRateLimitProblemHasPerSourceAction(t *testing.T) {
	env := newNativeEnv(t)
	id := seedFrequencyIndexer(t, env, "freq-429", 3600, `{"url":"https://example.invalid/api"}`)
	ctx := context.Background()
	if err := env.store.UpsertIndexerState(ctx, store.IndexerState{
		IndexerID: id,
		LastError: "search failed: HTTP 429 too many requests",
	}); err != nil {
		t.Fatalf("UpsertIndexerState: %v", err)
	}

	body := get(t, env.handler, "/indexers").Body.String()
	if strings.Contains(body, `<a class="problem-action" href="/settings"`) {
		t.Error("the rate action still points at the global Settings page")
	}
	if !strings.Contains(body, `action="/indexers/`+strconv.FormatInt(id, 10)+`/frequency"`) {
		t.Errorf("the rate action is not bound to source %d: %s", id, body)
	}
	if !strings.Contains(body, "Reduce the frequency") || !strings.Contains(body, `name="csrf"`) {
		t.Error("the rate action is not a CSRF-protected POST")
	}
}
