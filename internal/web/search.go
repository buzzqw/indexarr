package web

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/buzzqw/indexarr/internal/store"
)

// parseSearchTerms splits a query into include and exclude terms.
//
// Normal mode: whitespace-separated words; a leading "-" excludes a word; the
// words are matched against the normalized title.
//
// Regex mode: whitespace-separated Go/RE2 patterns; a leading "-" excludes a
// pattern; quotes keep spaces inside one pattern; a leading and trailing slash
// is accepted (JavaScript habit). Each pattern is made case-insensitive with
// (?i) unless the pattern overrides the flag.
func parseSearchTerms(query string, regexMode bool) (include, exclude []string, err error) {
	if regexMode {
		for _, term := range splitRegexTerms(query) {
			excluded := false
			if strings.HasPrefix(term, "-") && len(term) > 1 {
				excluded = true
				term = term[1:]
			}
			if term == "" {
				continue
			}
			if len(term) >= 2 && strings.HasPrefix(term, "/") && strings.HasSuffix(term, "/") {
				term = term[1 : len(term)-1]
			}
			pattern := "(?i)" + term
			if _, compileErr := regexp.Compile(pattern); compileErr != nil {
				return nil, nil, fmt.Errorf("%q: %v", term, compileErr)
			}
			if excluded {
				exclude = append(exclude, pattern)
			} else {
				include = append(include, pattern)
			}
		}
		return include, exclude, nil
	}
	for _, field := range strings.Fields(query) {
		excluded := strings.HasPrefix(field, "-") && len(field) > 1
		term := field
		if excluded {
			term = field[1:]
		}
		term = store.NormalizeSearch(term)
		if term == "" {
			continue
		}
		if excluded {
			exclude = append(exclude, term)
		} else {
			include = append(include, term)
		}
	}
	return include, exclude, nil
}

// splitRegexTerms splits on whitespace outside single or double quotes.
// Backslashes are preserved for the regex engine.
func splitRegexTerms(query string) []string {
	var terms []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			terms = append(terms, current.String())
			current.Reset()
		}
	}
	for _, r := range query {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			current.WriteRune(r)
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t', '\n':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return terms
}
