// Package textutil contains the small normalisation helpers shared by the
// store and the feed parsers.
package textutil

import (
	"strings"
	"unicode"
)

// NormalizeTitle turns a release title into the lowercase, punctuation-free
// form used for searching and duplicate detection.
func NormalizeTitle(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	space := true
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// NormalizeID keeps the digits of an external id and drops the "tt" prefix
// IMDb uses, so "tt0133093" and "0133093" are the same id.
func NormalizeID(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimPrefix(raw, "tt")
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// likeEscaper is built once: search builds the same LIKE pattern for every
// request, and constructing a Replacer dominates the cost of escaping a token.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike escapes the LIKE wildcards of a search token.
func EscapeLike(token string) string {
	return likeEscaper.Replace(token)
}

// Truncate shortens text to max runes, appending an ellipsis when it cuts.
func Truncate(text string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
