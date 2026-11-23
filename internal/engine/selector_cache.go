package engine

import (
	"sync"
	"sync/atomic"

	"github.com/andybalholm/cascadia"
)

// Selector strings come from the definition and are therefore stable for the
// life of the process. Compiling one for every field of every row cost
// thousands of AST builds on a large listing page, so the parsed selector is
// kept once. The bound protects the process from a definition that builds
// selectors dynamically; past it, compilation still works without caching.
const selectorCacheMax = 4096

var (
	selectorCache   sync.Map // string -> cascadia.Selector
	selectorEntries atomic.Int64
)

// CompileSelector is the exported form used by tools that test the same
// selectors the engine runs (the definition debugger). It shares the cache
// with the engine so a selector proven in the tool behaves the same in a run.
func CompileSelector(raw string) (cascadia.Selector, error) { return compileSelector(raw) }

func compileSelector(raw string) (cascadia.Selector, error) {
	if cached, ok := selectorCache.Load(raw); ok {
		return cached.(cascadia.Selector), nil
	}
	selector, err := cascadia.Compile(raw)
	if err != nil {
		return nil, err
	}
	if selectorEntries.Load() < selectorCacheMax {
		if _, loaded := selectorCache.LoadOrStore(raw, selector); !loaded {
			selectorEntries.Add(1)
		}
	}
	return selector, nil
}
