package cardigann

import (
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
)

// convertCapabilities maps caps.modes to native capability lists. When the
// source declares no usable modes, the conservative native defaults are used
// and a warning is recorded.
func (c *converter) convertCapabilities() definition.Capabilities {
	var caps definition.Capabilities
	seenModes := make(map[string]bool)

	for _, mode := range c.raw.Caps.Modes {
		name := normaliseMode(mode.Name)
		if name == "" {
			c.addUnsupported("capability mode " + strings.TrimSpace(mode.Name))
			c.addWarning("unknown capability mode %q ignored", strings.TrimSpace(mode.Name))
			continue
		}
		if seenModes[name] {
			c.addWarning("duplicate capability mode %q ignored", mode.Name)
			continue
		}
		params := c.convertCapabilityParams(mode.Name, mode.Params)
		if len(params) == 0 {
			c.addWarning("capability mode %q has no supported parameters; dropped", mode.Name)
			continue
		}
		seenModes[name] = true
		switch name {
		case "search":
			caps.Search = params
		case "tv-search":
			caps.TVSearch = params
		case "movie-search":
			caps.MovieSearch = params
		case "music-search":
			caps.MusicSearch = params
		case "book-search":
			caps.BookSearch = params
		}
	}

	if len(c.raw.Caps.Modes) == 0 {
		c.addWarning("caps.modes is missing; assumed search [q] and tv-search [q, season, ep]")
		caps.Search = []string{"q"}
		caps.TVSearch = []string{"q", "season", "ep"}
		return caps
	}
	if len(caps.Search) == 0 && len(caps.TVSearch) == 0 &&
		len(caps.MovieSearch) == 0 && len(caps.MusicSearch) == 0 && len(caps.BookSearch) == 0 {
		c.addWarning("no usable capability mode found; using search [q] and tv-search [q, season, ep]")
		caps.Search = []string{"q"}
		caps.TVSearch = []string{"q", "season", "ep"}
	}
	return caps
}

func normaliseMode(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.NewReplacer("_", "-", " ", "").Replace(name)
	switch name {
	case "search":
		return "search"
	case "tvsearch", "tv-search", "tv":
		return "tv-search"
	case "moviesearch", "movie-search", "movie", "movies":
		return "movie-search"
	case "musicsearch", "music-search", "music":
		return "music-search"
	case "booksearch", "book-search", "book", "books":
		return "book-search"
	default:
		return ""
	}
}

func (c *converter) convertCapabilityParams(mode string, raw []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, param := range raw {
		name := normaliseCapabilityParam(param)
		if name == "" {
			c.addWarning("capability mode %q parameter %q is not supported by native v1; dropped", mode, param)
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func normaliseCapabilityParam(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.NewReplacer("_", "", "-", "").Replace(name)
	switch name {
	case "q", "query", "keywords":
		return "q"
	case "season":
		return "season"
	case "ep", "episode", "episodes":
		return "ep"
	case "imdb", "imdbid":
		return "imdbid"
	case "tvdb", "tvdbid":
		return "tvdbid"
	case "tmdb", "tmdbid":
		return "tmdbid"
	case "tvmaze", "tvmazeid":
		return "tvmazeid"
	case "rid", "rageid":
		return "rid"
	case "artist", "album", "author", "title", "year", "genre":
		return name
	default:
		return ""
	}
}
