package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/store"
)

const blockedPage = `<html><head><title>Just a moment...</title></head>
<body><div id="cf-challenge-running">Checking your browser before accessing.</div></body></html>`

func TestRunMarksChallengeAsBlocked(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 2)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, blockedPage)
	}))
	defer site.Close()

	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, site.URL, true)

	_, err := manager.Run(context.Background(), indexerID)
	if !errors.Is(err, engine.ErrChallenge) {
		t.Fatalf("Run error = %v, want ErrChallenge", err)
	}
	history, err := st.ListIndexerHistory(context.Background(), indexerID, 10)
	if err != nil {
		t.Fatalf("ListIndexerHistory: %v", err)
	}
	if len(history) == 0 || history[0].Status != "blocked" {
		t.Fatalf("history = %+v, want a blocked run", history)
	}
	if !strings.Contains(strings.ToLower(history[0].Error), "cloudflare") {
		t.Errorf("history error = %q", history[0].Error)
	}
	state, err := st.GetIndexerState(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("GetIndexerState: %v", err)
	}
	if !isChallengeText(state.LastError) {
		t.Errorf("state last_error = %q", state.LastError)
	}
}

func TestRunSolvesChallengeThroughGlobalFlareSolverr(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 3)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, blockedPage)
	}))
	defer site.Close()

	var sawSearch bool
	solver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		body := searchHTML
		if strings.Contains(payload.URL, "/download/") {
			body = string(torrentBody)
		} else if strings.Contains(payload.URL, "/search") {
			sawSearch = true
		}
		encoded, _ := json.Marshal(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"status":   http.StatusOK,
				"response": body,
			},
		})
		_, _ = w.Write(encoded)
	}))
	defer solver.Close()

	settings := store.DefaultSettings()
	settings.FlareSolverrURL = solver.URL
	settings.FlareSolverrTimeoutSeconds = 5
	if err := st.SaveSettings(context.Background(), settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, site.URL, true)
	stats, err := manager.Run(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Seen != 2 || stats.New != 2 {
		t.Errorf("Seen/New = %d/%d, want 2/2", stats.Seen, stats.New)
	}
	if !sawSearch {
		t.Error("the definition search was not solved through FlareSolverr")
	}
	history, err := st.ListIndexerHistory(context.Background(), indexerID, 10)
	if err != nil {
		t.Fatalf("ListIndexerHistory: %v", err)
	}
	if len(history) == 0 || history[0].Status != "ok" {
		t.Fatalf("history = %+v, want a successful run", history)
	}
}

func TestChallengeSolverResolution(t *testing.T) {
	network := store.DefaultSettings()
	network.FlareSolverrURL = "http://127.0.0.1:8191"
	network.FlareSolverrTimeoutSeconds = 45

	solver, override, err := challengeSolver(map[string]string{}, network)
	if err != nil || solver == nil || override {
		t.Fatalf("global = solver %v override %t err %v", solver, override, err)
	}

	solver, override, err = challengeSolver(map[string]string{"challenge": "none"}, network)
	if err != nil || solver != nil || !override {
		t.Fatalf("none = solver %v override %t err %v", solver, override, err)
	}

	solver, override, err = challengeSolver(map[string]string{
		"challenge":            "flaresolverr",
		"flaresolverr_url":     "http://127.0.0.1:9999",
		"flaresolverr_timeout": "20",
	}, network)
	if err != nil || solver == nil || !override {
		t.Fatalf("override = solver %v override %t err %v", solver, override, err)
	}

	if _, _, err := challengeSolver(map[string]string{"challenge": "flaresolverr"}, store.DefaultSettings()); err == nil {
		t.Fatal("an enabled solver without a URL was accepted")
	}
	if _, _, err := challengeSolver(map[string]string{
		"challenge":            "flaresolverr",
		"flaresolverr_timeout": "1",
	}, network); err == nil {
		t.Fatal("an invalid per-indexer timeout was accepted")
	}
}

// isChallengeText mirrors the UI classifier used to choose the blocked state.
func isChallengeText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "cloudflare") || strings.Contains(lower, "challenge") ||
		strings.Contains(lower, "flaresolverr")
}
