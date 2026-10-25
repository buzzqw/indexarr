package appsync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Snapshotter is the optional capability used by the app sync to store the
// application's indexer list before a change and to restore a previous one.
// Clients that cannot manage indexers (Mylar3, LazyLibrarian) do not implement
// it: their snapshots would always be empty and a restore could never work.
type Snapshotter interface {
	// IndexerSnapshot returns the application's indexer list as JSON. It is
	// the same list ListIndexers returns, in the application's own field
	// names, so a snapshot stays useful after an indexarr upgrade.
	IndexerSnapshot(ctx context.Context) ([]byte, error)
}

// SnapshotSpecs parses a snapshot payload and returns the specs of the
// indexers indexarr manages (those whose name starts with ManagedPrefix).
// Indexers created by the operator are deliberately excluded: a restore must
// never rewrite them. A malformed payload is an error, never an empty list:
// restoring an unreadable snapshot must stop instead of deleting everything.
func SnapshotSpecs(payload []byte) ([]IndexerSpec, error) {
	payload = []byte(strings.TrimSpace(string(payload)))
	if len(payload) == 0 {
		return nil, nil
	}

	var entries []map[string]any
	if err := json.Unmarshal(payload, &entries); err != nil {
		return nil, fmt.Errorf("appsync: decode snapshot: %w", err)
	}
	out := make([]IndexerSpec, 0, len(entries))
	for _, entry := range entries {
		name := asString(entry["name"])
		if !strings.HasPrefix(name, ManagedPrefix) {
			continue
		}
		out = append(out, specFromSnapshotEntry(entry))
	}
	return out, nil
}

// CountSnapshotIndexers counts the indexers stored in a snapshot, without
// requiring the payload to be a valid list.
func CountSnapshotIndexers(payload []byte) int {
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(payload))), &entries); err != nil {
		return 0
	}
	return len(entries)
}

// specFromSnapshotEntry rebuilds the part of an indexer that indexarr owns.
// It mirrors remoteFromMap, but keeps the stored values instead of a hash.
func specFromSnapshotEntry(entry map[string]any) IndexerSpec {
	fields := fieldMap(entry["fields"])
	return IndexerSpec{
		Name:                    asString(entry["name"]),
		Implementation:          asString(entry["implementation"]),
		ConfigContract:          asString(entry["configContract"]),
		BaseURL:                 asString(fields["baseUrl"]),
		APIKey:                  asString(fields["apiKey"]),
		Categories:              asIntSlice(fields["categories"]),
		Priority:                asInt(entry["priority"]),
		Tags:                    asIntSlice(entry["tags"]),
		EnableRSS:               asBool(entry["enableRss"]),
		EnableAutomaticSearch:   asBool(entry["enableAutomaticSearch"]),
		EnableInteractiveSearch: asBool(entry["enableInteractiveSearch"]),
		Protocol:                asString(entry["protocol"]),
	}
}
