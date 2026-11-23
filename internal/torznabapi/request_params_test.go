package torznabapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexer"
)

func TestCacheParameterOverridesTheSavedLiveSetting(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured bool
		cache      string
		want       bool
	}{
		{"off by default", false, "", false},
		{"on by default", true, "", true},
		{"false forces live", false, "false", true},
		{"zero forces live", false, "0", true},
		{"no forces live", false, "NO", true},
		{"only forces stored", true, "only", false},
		{"stored forces stored", true, "STORED", false},
		{"unknown keeps the setting", true, "true", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveSearchEnabled(tc.configured, tc.cache); got != tc.want {
				t.Errorf("liveSearchEnabled(%v, %q) = %v, want %v", tc.configured, tc.cache, got, tc.want)
			}
		})
	}
}

func TestClampLimitToCapsUsesTheSmallestDeclaredMaximum(t *testing.T) {
	caps := map[int64]engine.Capabilities{
		1: {MaxLimit: 10},
		2: {MaxLimit: 0},
		3: {MaxLimit: 25},
	}
	for _, tc := range []struct {
		name  string
		limit int
		ids   []int64
		want  int
	}{
		{"single site", 100, []int64{1}, 10},
		{"smallest of many", 100, []int64{1, 2, 3}, 10},
		{"unknown never clamps", 100, []int64{2}, 100},
		{"smaller request stays", 5, []int64{1, 3}, 5},
		{"no selection", 100, nil, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampLimitToCaps(tc.limit, caps, tc.ids); got != tc.want {
				t.Errorf("clampLimitToCaps(%d, %v) = %d, want %d", tc.limit, tc.ids, got, tc.want)
			}
		})
	}
}

func TestCacheFalseRunsLiveWithTheSettingOff(t *testing.T) {
	api, _ := newAPI(t)
	calls := 0
	api.SetLiveSearch(func(context.Context, indexer.LiveQuery) (indexer.LiveResult, error) {
		calls++
		return indexer.LiveResult{}, nil
	})

	do(t, api, "/api?t=search&apikey=secret&q=Show&cache=only")
	if calls != 0 {
		t.Fatalf("cache=only called the live fan-out %d times", calls)
	}
	recorder := do(t, api, "/api?t=search&apikey=secret&q=Show&cache=false")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if calls != 1 {
		t.Fatalf("cache=false called the live fan-out %d times, want 1", calls)
	}
}

func TestCacheOnlyStopsTheConfiguredLiveFanOut(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.TorznabLiveSearch = true
	settings.TorznabLiveWait = 5
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	calls := 0
	api.SetLiveSearch(func(context.Context, indexer.LiveQuery) (indexer.LiveResult, error) {
		calls++
		return indexer.LiveResult{}, nil
	})

	recorder := do(t, api, "/api?t=search&apikey=secret&q=Show&cache=only")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if calls != 0 {
		t.Fatalf("cache=only called the live fan-out %d times", calls)
	}
}

func TestCapsMaxLimitClampsTheRequestedPage(t *testing.T) {
	api, st := newAPI(t)
	seedDefinition(t, st, "caps-def", "unit3d", "en")
	site := seedIndexer(t, st, "caps-def", "caps-site", "Caps Site", "", "torrent", true)
	for i := 0; i < 5; i++ {
		insertScopedRelease(t, st, 60+i, "Caps Show "+string(rune('A'+i)), site, true)
	}
	api.SetCapabilities(func(context.Context, []int64) (map[int64]engine.Capabilities, error) {
		return map[int64]engine.Capabilities{site.ID: {MaxLimit: 2}}, nil
	})

	recorder := do(t, api, "/torznab/caps-site/api?t=search&apikey=secret&q=Caps&limit=100")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := strings.Count(recorder.Body.String(), "<item>"); got != 2 {
		t.Errorf("items = %d, want the declared maximum of 2", got)
	}
	if !strings.Contains(recorder.Body.String(), `total="5"`) {
		t.Error("the total should still count every stored row")
	}
}
