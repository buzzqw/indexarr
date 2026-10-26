package web

import (
	"context"
	"strings"
	"sync"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
)

// parsedDefinitionCache keeps the YAML parse of a stored definition between
// requests. Definitions change only when the operator installs, imports or
// edits one, but the indexers list, the source detail page and the definitions
// page read them on every request. Parsing the 86 stored documents (about
// 180 KB of YAML) for one page cost roughly 100 ms of CPU and several MB of
// garbage before this cache existed.
//
// The key carries the content hash, so saving an edited definition replaces
// the entry on the next read without any explicit invalidation. Entries are
// shared and must be treated as read-only, exactly like the embedded catalog.
type parsedDefinitionCache struct {
	mu      sync.RWMutex
	entries map[string]cachedDefinition
	order   []string
}

type cachedDefinition struct {
	hash string
	def  *definition.Definition
}

// parsedDefinitionCacheMax bounds the memory of the cache. The catalog has 85
// built-in definitions plus local ones, so the limit only matters on a database
// filled with hundreds of imported drafts.
const parsedDefinitionCacheMax = 256

// lookup returns the parsed definition when the cached one is still current.
// The zero value of parsedDefinitionCache is ready to use.
func (c *parsedDefinitionCache) lookup(id, hash string) (*definition.Definition, bool) {
	c.mu.RLock()
	entry, ok := c.entries[id]
	c.mu.RUnlock()
	if !ok || entry.hash != hash || entry.def == nil {
		return nil, false
	}
	return entry.def, true
}

// store keeps one parsed definition, evicting the oldest id when the cache is
// full. Replacing the value of an existing id keeps its position in the order.
func (c *parsedDefinitionCache) store(id, hash string, def *definition.Definition) {
	if def == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]cachedDefinition, 8)
	}
	if _, ok := c.entries[id]; !ok {
		if len(c.order) >= parsedDefinitionCacheMax {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldest)
		}
		c.order = append(c.order, id)
	}
	c.entries[id] = cachedDefinition{hash: hash, def: def}
}

// parseStoredDefinition parses a definition row, reusing the cached tree when
// its hash is unchanged. A row that still matches the embedded catalog reuses
// the catalog's already parsed definition instead of parsing the same YAML a
// second time.
func (s *Server) parseStoredDefinition(row store.Definition) (*definition.Definition, error) {
	if def, ok := s.definitions.lookup(row.ID, row.Hash); ok {
		return def, nil
	}
	if entry, ok := catalog.Get(row.ID); ok && entry.Hash == row.Hash && entry.Definition != nil {
		s.definitions.store(row.ID, row.Hash, entry.Definition)
		return entry.Definition, nil
	}
	def, err := definition.Parse([]byte(row.Body))
	if err != nil {
		return nil, err
	}
	s.definitions.store(row.ID, row.Hash, def)
	return def, nil
}

// parsedDefinition resolves one definition by id: a stored row first, then the
// embedded catalog. It returns false when the id is empty, unknown or the YAML
// does not parse; callers keep their previous fallback behaviour.
func (s *Server) parsedDefinition(ctx context.Context, id string) (*definition.Definition, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, false
	}
	if stored, err := s.store.GetDefinition(ctx, id); err == nil {
		def, parseErr := s.parseStoredDefinition(stored)
		if parseErr != nil {
			return nil, false
		}
		return def, true
	}
	if entry, ok := catalog.Get(id); ok && entry.Definition != nil {
		return entry.Definition, true
	}
	return nil, false
}

// indexerDefinitions parses the definitions referenced by the given indexers.
// Pages used to parse every stored definition (the whole catalog, 86 rows)
// even when only a handful of sources were installed.
func (s *Server) indexerDefinitions(ctx context.Context, indexers []store.Indexer) map[string]*definition.Definition {
	out := make(map[string]*definition.Definition, len(indexers))
	for _, in := range indexers {
		id := strings.TrimSpace(in.DefinitionID)
		if id == "" {
			continue
		}
		if _, ok := out[id]; ok {
			continue
		}
		if def, ok := s.parsedDefinition(ctx, id); ok {
			out[id] = def
		}
	}
	return out
}
