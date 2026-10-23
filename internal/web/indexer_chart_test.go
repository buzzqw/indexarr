package web

import (
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestRunChartUsesRealTimeAxis(t *testing.T) {
	start := time.Date(2026, time.October, 17, 12, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	points := []store.RunSeriesPoint{
		{StartedAt: start.Add(time.Hour), New: 1, Discarded: 0},
		{StartedAt: start.Add(5 * time.Hour), New: 2, Discarded: 1},
		{StartedAt: end.Add(-time.Hour), New: 3, Discarded: 2},
	}
	chart := string(renderRunChartWindow(points, start, end))
	matches := regexp.MustCompile(`class="chart-new" x="([0-9.]+)"`).FindAllStringSubmatch(chart, -1)
	if len(matches) != 3 {
		t.Fatalf("bars = %d, want 3: %s", len(matches), chart)
	}
	positions := make([]float64, 0, len(matches))
	for _, match := range matches {
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			t.Fatalf("parse x %q: %v", match[1], err)
		}
		positions = append(positions, value)
	}
	if !(positions[0] < 80 && positions[1] < 80 && positions[2] > 650) {
		t.Fatalf("real-time positions = %v; two runs five hours apart must stay on the left and a run six days later on the right", positions)
	}
	if positions[1]-positions[0] >= positions[2]-positions[1] {
		t.Fatalf("time gaps were not respected: %v", positions)
	}
}
