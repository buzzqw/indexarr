package definition

import (
	"crypto/sha256"
	"sync"
)

// sharedParses keeps parsed definitions for read-only callers. The scheduler
// and every Arr RSS poll ask each enabled indexer whether it publishes a
// latest list, and parsing every stored YAML document for that was about a
// tenth of the CPU of an RSS request.
var sharedParses = struct {
	sync.RWMutex
	entries map[[32]byte]*Definition
}{entries: make(map[[32]byte]*Definition)}

// sharedParsesMax bounds the cache; the catalog has under a hundred
// definitions, so the limit only matters with hundreds of imported drafts.
const sharedParsesMax = 512

// ParseShared is Parse with a process-wide cache keyed by the document
// content, so an edited definition is parsed again on its next read. The
// returned definition is shared: callers must not modify it. Errors are not
// cached.
func ParseShared(body []byte) (*Definition, error) {
	key := sha256.Sum256(body)
	sharedParses.RLock()
	def, ok := sharedParses.entries[key]
	sharedParses.RUnlock()
	if ok {
		return def, nil
	}
	def, err := Parse(body)
	if err != nil {
		return nil, err
	}
	sharedParses.Lock()
	if len(sharedParses.entries) >= sharedParsesMax {
		sharedParses.entries = make(map[[32]byte]*Definition, sharedParsesMax)
	}
	sharedParses.entries[key] = def
	sharedParses.Unlock()
	return def, nil
}
