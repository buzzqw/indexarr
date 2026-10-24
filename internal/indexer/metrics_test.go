package indexer

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestRunFilterRejectsSmallFilesAndOldItems(t *testing.T) {
	body, err := os.ReadFile("testdata/small_release.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Title     string `json:"title"`
		SizeBytes int64  `json:"size_bytes"`
		AgeHours  int    `json:"age_hours"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	filter := newRunFilter(map[string]any{"min_size_mb": 50, "max_age_days": 2}, now)
	if _, _, small := filter.reject(engine.Item{Title: fixture.Title, Size: fixture.SizeBytes, Published: now}); !small {
		t.Errorf("%s (%d bytes) was not rejected as small", fixture.Title, fixture.SizeBytes)
	}
	if _, _, small := filter.reject(engine.Item{Title: "release", Size: 50 << 20, Published: now}); small {
		t.Error("50 MB item was rejected")
	}
	if old, _, _ := filter.reject(engine.Item{Title: "old", Size: 1 << 30, Published: now.Add(-72 * time.Hour)}); !old {
		t.Error("72h item was not rejected as old")
	}
}
