package torznabapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

// appFromRequest names the downstream application behind a Torznab request.
// Servarr sends a User-Agent such as "Sonarr/4.0"; an explicit header wins.
// The value is only a label for the activity page.
func appFromRequest(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	candidates := []string{
		r.Header.Get("X-Application"),
		r.Header.Get("X-Client"),
		r.Header.Get("X-Indexarr-App"),
		r.UserAgent(),
	}
	known := []struct {
		needle string
		name   string
	}{
		{"sonarr", "sonarr"},
		{"radarr", "radarr"},
		{"lidarr", "lidarr"},
		{"readarr", "readarr"},
		{"whisparr", "whisparr"},
		{"mylar", "mylar3"},
		{"lazylibrarian", "lazylibrarian"},
		{"prowlarr", "prowlarr"},
		{"jackett", "jackett"},
		{"gextto", "gextto"},
	}
	for _, candidate := range candidates {
		lower := strings.ToLower(strings.TrimSpace(candidate))
		if lower == "" {
			continue
		}
		for _, app := range known {
			if strings.Contains(lower, app.needle) {
				return app.name
			}
		}
	}
	// Gextto marks its indexarr requests with live_wait, an indexarr-only
	// parameter; Sonarr and Radarr never send it. Without this, the generic
	// Go agent of the request became the application label.
	if r.URL != nil && strings.TrimSpace(r.URL.Query().Get("live_wait")) != "" {
		return "gextto"
	}
	for _, candidate := range candidates {
		text := strings.TrimSpace(candidate)
		if text == "" {
			continue
		}
		if index := strings.IndexAny(text, " \t/"); index > 0 {
			text = text[:index]
		}
		if len(text) > 60 {
			text = text[:60]
		}
		return strings.ToLower(text)
	}
	return "unknown"
}

// recordAppSearch writes one row of the Activity → Searches view. It runs
// outside the request cancellation so a timeout is still auditable, and it
// never changes the response: a store failure is only logged.
// sanitizedParams records exactly what the app sent, minus the credentials.
// It is the evidence needed to diagnose an ID-only or alternate-title request.
func sanitizedParams(r *http.Request) string {
	if r == nil {
		return ""
	}
	values := r.URL.Query()
	values.Del("apikey")
	values.Del("api_key")
	return values.Encode()
}

func (s *Server) recordAppSearch(ctx context.Context, r *http.Request, mode string, out searchOutcome, started time.Time) {
	results := make(map[string]int64)
	for _, release := range out.Releases {
		provenance := out.Provenance[release.ID]
		if len(provenance.Names) == 0 {
			name := ""
			if len(out.IndexerNames) > 0 {
				name = out.IndexerNames[0]
			}
			results[name]++
			continue
		}
		for _, name := range provenance.Names {
			results[name]++
		}
	}
	errorText := out.Err
	if errorText == "" && out.Status >= 400 {
		errorText = "request failed"
	}
	query := out.Query
	if len(query) > 500 {
		query = query[:500]
	}
	params := sanitizedParams(r)
	live := make([]store.AppSearchLive, 0, len(out.Live))
	names := make([]string, 0, len(out.Live))
	ids := make([]int64, 0, len(out.Live))
	for _, entry := range out.Live {
		live = append(live, store.AppSearchLive{
			IndexerID: entry.IndexerID,
			Name:      entry.Name,
			Mode:      entry.Mode,
			Fallback:  entry.Fallback,
			Query:     entry.Query,
			Results:   int64(entry.Results),
			Duration:  entry.Duration.Milliseconds(),
			TimedOut:  entry.TimedOut,
			Skipped:   entry.Skipped,
			Error:     entry.Err,
		})
		if entry.Name != "" {
			names = append(names, entry.Name)
		}
		if entry.IndexerID > 0 {
			ids = append(ids, entry.IndexerID)
		}
	}
	indexerNames := out.IndexerNames
	indexerIDs := out.IndexerIDs
	if len(names) > 0 {
		indexerNames = names
		indexerIDs = ids
	}
	in := store.AppSearch{
		App:              appFromRequest(r),
		Mode:             mode,
		Query:            query,
		Params:           params,
		IndexerIDs:       indexerIDs,
		IndexerNames:     indexerNames,
		Results:          results,
		TotalResults:     out.Total,
		DuplicatesMerged: out.DuplicatesMerged,
		DurationMS:       time.Since(started).Milliseconds(),
		Error:            errorText,
		Live:             live,
		CreatedAt:        time.Now(),
	}
	if _, err := s.store.RecordAppSearch(context.WithoutCancel(ctx), in); err != nil {
		s.log.Warn("torznab: record app search", "component", "indexarr",
			"subsystem", "torznab", "error", err)
	}
}
