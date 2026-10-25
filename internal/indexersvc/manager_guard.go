package indexersvc

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// ErrManagerEndpoint is returned when a source URL belongs to Jackett or
// Prowlarr. indexarr replaces the two managers; pointing an indexer at their
// per-indexer Torznab endpoints would keep the dependency the replacement is
// meant to remove.
var ErrManagerEndpoint = errors.New("indexarr replaces Jackett and Prowlarr: manager endpoints are not accepted")

// prowlarrIndexerPath matches Prowlarr's per-indexer endpoint, for example
// /15/api. Jackett's aggregate and per-indexer endpoints both live under
// /api/v2.0/indexers/.
var prowlarrIndexerPath = regexp.MustCompile(`^/[0-9]+/api$`)

// managerEndpoint reports whether a value is an endpoint of Jackett or
// Prowlarr. It only inspects the URL shape, not the host: a manager can run on
// any address and a tracker can share the host name.
func managerEndpoint(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return false
	}
	path := strings.ToLower(strings.TrimRight(parsed.EscapedPath(), "/"))
	if path == "" {
		return false
	}
	if strings.Contains(path, "/api/v2.0/indexers/") &&
		strings.Contains(path, "/results/torznab") {
		return true
	}
	// Prowlarr's own API. Do not reject every /api/v1/ path: many trackers
	// expose a legitimate /api/v1/torrents endpoint.
	for _, prefix := range []string{
		"/api/v1/indexer", "/api/v1/search", "/api/v1/applications",
		"/api/v1/downloadclient", "/api/v1/health",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return prowlarrIndexerPath.MatchString(path)
}

// rejectManagerEndpoints scans the settings a user is about to save. It is
// deliberately applied to every value, so a manager URL cannot hide in a
// differently named field.
func rejectManagerEndpoints(settings map[string]string) error {
	for _, value := range settings {
		if managerEndpoint(value) {
			return ErrManagerEndpoint
		}
	}
	return nil
}
