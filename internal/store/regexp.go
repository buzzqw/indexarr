package store

import (
	"database/sql/driver"
	"regexp"
	"sync"

	sqlite "modernc.org/sqlite"
)

// SQLite has no native REGEXP operator. modernc.org/sqlite lets us register a
// package-level scalar function, which the SQL `value REGEXP pattern` syntax
// calls as regexp(pattern, value). Go's regexp is RE2: linear time, no
// catastrophic backtracking.
func init() {
	if err := sqlite.RegisterFunction("regexp", &sqlite.FunctionImpl{
		NArgs:         2,
		Deterministic: true,
		Scalar: func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			if len(args) != 2 {
				return int64(0), nil
			}
			pattern, _ := args[0].(string)
			value, _ := args[1].(string)
			compiled, err := compileRegex(pattern)
			if err != nil {
				// The web layer compiles and rejects invalid patterns before
				// the query; returning no match is the safe fallback.
				return int64(0), nil
			}
			if compiled.MatchString(value) {
				return int64(1), nil
			}
			return int64(0), nil
		},
	}); err != nil {
		panic("store: register REGEXP function: " + err.Error())
	}
}

var (
	regexCacheMu sync.Mutex
	regexCache   = map[string]*regexp.Regexp{}
)

const regexCacheMax = 256

func compileRegex(pattern string) (*regexp.Regexp, error) {
	regexCacheMu.Lock()
	defer regexCacheMu.Unlock()
	if compiled, ok := regexCache[pattern]; ok {
		return compiled, nil
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	if len(regexCache) >= regexCacheMax {
		regexCache = map[string]*regexp.Regexp{}
	}
	regexCache[pattern] = compiled
	return compiled, nil
}
