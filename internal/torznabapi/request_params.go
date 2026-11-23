package torznabapi

import (
	"strings"

	"github.com/buzzqw/indexarr/internal/engine"
)

// liveSearchEnabled implements the cache parameter of a Torznab request.
// cache=false forces a live fan-out even when the saved setting is off;
// cache=only asks for the stored answer even when the setting is on. Any
// other value keeps the configured behaviour, so existing clients are
// unaffected.
func liveSearchEnabled(configured bool, cache string) bool {
	switch strings.ToLower(strings.TrimSpace(cache)) {
	case "false", "0", "no":
		return true
	case "only", "stored":
		return false
	default:
		return configured
	}
}

// clampLimitToCaps bounds a requested page size by the smallest <limits max>
// declared by the selected indexers. A missing or unknown limit never makes
// the request smaller: only a declared maximum can clamp it.
func clampLimitToCaps(limit int, caps map[int64]engine.Capabilities, ids []int64) int {
	maximum := 0
	for _, id := range ids {
		cap, ok := caps[id]
		if !ok || cap.MaxLimit <= 0 {
			continue
		}
		if maximum == 0 || cap.MaxLimit < maximum {
			maximum = cap.MaxLimit
		}
	}
	if maximum > 0 && limit > maximum {
		return maximum
	}
	return limit
}
