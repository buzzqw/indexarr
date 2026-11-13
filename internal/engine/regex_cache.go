package engine

import (
	"regexp"
	"sync"
	"sync/atomic"
)

// Definition filters and extractors repeat the same pattern for every row or
// value. The compiled expression is immutable, so one copy per pattern is
// enough. Invalid patterns are not cached: a definition error must still be
// reported every time it is exercised.
const regexCacheMax = 4096

var (
	regexCache   sync.Map // string -> *regexp.Regexp
	regexEntries atomic.Int64
)

func compileRegex(pattern string) (*regexp.Regexp, error) {
	if cached, ok := regexCache.Load(pattern); ok {
		return cached.(*regexp.Regexp), nil
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	if regexEntries.Load() < regexCacheMax {
		if _, loaded := regexCache.LoadOrStore(pattern, compiled); !loaded {
			regexEntries.Add(1)
		}
	}
	return compiled, nil
}
