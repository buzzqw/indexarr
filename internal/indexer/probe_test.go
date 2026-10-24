package indexer

import (
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestEstimateLatestMeasuresADayAndMonth(t *testing.T) {
	now := time.Now()
	items := []engine.Item{
		{Title: "A", InfoHash: "a", Size: 1 << 30, Published: now.Add(-48 * time.Hour)},
		{Title: "B", InfoHash: "b", Size: 2 << 30, Published: now.Add(-24 * time.Hour)},
		{Title: "C", InfoHash: "c", Size: 3 << 30, Published: now},
	}
	probe := EstimateLatest(items, 2500, 56<<20)
	if probe.Items != 3 || probe.Dated != 3 {
		t.Fatalf("probe = %+v", probe)
	}
	if probe.Window != 48*time.Hour {
		t.Errorf("window = %v, want 48h", probe.Window)
	}
	// Three items over two days: 1.5 per day, rounded to 2. The estimate is
	// intentionally a small, measured number, not a promise.
	if probe.PerDay != 2 {
		t.Errorf("per day = %d, want 2", probe.PerDay)
	}
	if probe.AvgBytes != 2500 {
		t.Errorf("average bytes = %d, want the measured database fallback", probe.AvgBytes)
	}
	if probe.BytesPerMonth != probe.PerDay*probe.AvgBytes*30 {
		t.Errorf("bytes per month = %d", probe.BytesPerMonth)
	}
}

func TestEstimateLatestUsesFallbackAverageAndOneDayWindow(t *testing.T) {
	items := []engine.Item{
		{Title: "A", InfoHash: "a", Published: time.Now()},
		{Title: "B", InfoHash: "b", Published: time.Now().Add(-time.Hour)},
	}
	probe := EstimateLatest(items, 2500, 1000)
	if probe.PerDay != 2 {
		t.Errorf("per day = %d, want the whole page inside one day", probe.PerDay)
	}
	if probe.AvgBytes != 2500 {
		t.Errorf("average bytes = %d, want the database fallback", probe.AvgBytes)
	}
	if probe.BytesPerMonth != 2*2500*30 {
		t.Errorf("bytes per month = %d", probe.BytesPerMonth)
	}
	if probe.DBSize != 1000 {
		t.Errorf("db size = %d", probe.DBSize)
	}
}

func TestEstimateLatestDeduplicatesRepeatedItems(t *testing.T) {
	now := time.Now()
	items := []engine.Item{
		{Title: "A", InfoHash: "a", Published: now},
		{Title: "A again", InfoHash: "a", Published: now},
		{Title: "B", GUID: "b", Published: now},
	}
	probe := EstimateLatest(items, 0, 0)
	if probe.Items != 2 {
		t.Errorf("items = %d, want 2 after deduplication", probe.Items)
	}
}
