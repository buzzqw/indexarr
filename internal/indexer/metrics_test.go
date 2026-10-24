package indexer

import (
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestRunFilterRejectsSmallFilesAndOldItems(t *testing.T) {
	now := time.Now()
	filter := newRunFilter(map[string]any{"min_size_mb": 50, "max_age_days": 2}, now)
	if _, _, small := filter.reject(engine.Item{Title: "sample", Size: 49 << 20, Published: now}); !small {
		t.Error("49 MB item was not rejected as small")
	}
	if _, _, small := filter.reject(engine.Item{Title: "release", Size: 50 << 20, Published: now}); small {
		t.Error("50 MB item was rejected")
	}
	if old, _, _ := filter.reject(engine.Item{Title: "old", Size: 1 << 30, Published: now.Add(-72 * time.Hour)}); !old {
		t.Error("72h item was not rejected as old")
	}
}
