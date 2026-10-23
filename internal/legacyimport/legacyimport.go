// Package legacyimport builds a read-only preview of the migration from the
// legacy sources table to native indexers.
//
// The preview only classifies what the coordinator can migrate and how. It
// never disables a source, never writes to the store and never copies a
// credential: API keys and HTTP Basic passwords are reported as booleans, and
// URLs are stripped of userinfo and credential-bearing query parameters.
//
// Applying the preview is future work: the coordinator will create a native
// Torznab indexer for every Native candidate, move its API key into the vault,
// keep its bridged slug when one already exists, and only then disable the
// legacy source. RSS sources stay on the bridge until a native RSS family
// exists.
package legacyimport

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/buzzqw/indexarr/internal/store"
)

// Candidate is one legacy source and whether it can become a native indexer.
type Candidate struct {
	SourceID     int64  `json:"source_id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	URL          string `json:"url"`  // sanitized: no userinfo or credential query values
	Slug         string `json:"slug"` // slug of the existing bridged indexer, when present
	HasAPIKey    bool   `json:"has_api_key"`
	HasBasicAuth bool   `json:"has_basic_auth"`
	Native       bool   `json:"native"`
	Reason       string `json:"reason"` // why the source is skipped; empty for native candidates
}

// Preview classifies every row of sources. The order follows ListSources
// (name, case-insensitive). It performs no writes.
func Preview(ctx context.Context, st *store.Store) ([]Candidate, error) {
	if st == nil {
		return nil, errors.New("legacyimport: store is required")
	}
	sources, err := st.ListSources(ctx)
	if err != nil {
		return nil, fmt.Errorf("legacyimport: list sources: %w", err)
	}
	indexers, err := st.ListIndexers(ctx)
	if err != nil {
		return nil, fmt.Errorf("legacyimport: list indexers: %w", err)
	}

	bridgedSlugs := make(map[int64]string, len(indexers))
	for _, in := range indexers {
		if in.LegacySourceID <= 0 {
			continue
		}
		if _, exists := bridgedSlugs[in.LegacySourceID]; exists {
			continue
		}
		bridgedSlugs[in.LegacySourceID] = in.Slug
	}

	out := make([]Candidate, 0, len(sources))
	for _, source := range sources {
		kind := strings.ToLower(strings.TrimSpace(source.Kind))
		candidate := Candidate{
			SourceID:  source.ID,
			Name:      source.Name,
			Kind:      kind,
			URL:       sanitizedURL(source.URL),
			Slug:      bridgedSlugs[source.ID],
			HasAPIKey: strings.TrimSpace(source.APIKey) != "",
			HasBasicAuth: strings.TrimSpace(source.Username) != "" ||
				strings.TrimSpace(source.Password) != "",
		}

		switch {
		case kind == "torznab":
			if err := nativeURL(source.URL); err != nil {
				candidate.Reason = err.Error()
				break
			}
			candidate.Native = true
		case kind == "rss":
			candidate.Reason = "RSS sources have no native family yet"
		case kind == "":
			candidate.Reason = "source has no kind"
		default:
			candidate.Reason = fmt.Sprintf("%s sources are not supported natively", kind)
		}
		out = append(out, candidate)
	}
	return out, nil
}

// Counts returns how many candidates are native and how many are skipped.
func Counts(candidates []Candidate) (native, skipped int) {
	for _, candidate := range candidates {
		if candidate.Native {
			native++
			continue
		}
		skipped++
	}
	return native, skipped
}

// nativeURL validates that raw is an http or https URL with a host.
func nativeURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("source URL is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("source URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("source URL must use http or https")
	}
	if parsed.Host == "" {
		return errors.New("source URL has no host")
	}
	return nil
}

// secretParams are query parameters that must not appear in a preview.
var secretParams = []string{
	"apikey", "apitoken", "passkey", "password", "passwd",
	"token", "secret", "rsskey", "authkey", "cookie", "credentials",
}

// sanitizedURL removes userinfo, fragments and credential-bearing query
// parameters from a source URL. Invalid URLs produce an empty string rather
// than risking a secret leak.
func sanitizedURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.Fragment = ""
	query := parsed.Query()
	changed := false
	for key := range query {
		if isSecretParam(key) {
			query.Del(key)
			changed = true
		}
	}
	if changed {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func isSecretParam(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.NewReplacer("_", "", "-", "", ".", "").Replace(normalized)
	for _, secret := range secretParams {
		if normalized == secret || strings.Contains(normalized, secret) {
			return true
		}
	}
	return false
}
