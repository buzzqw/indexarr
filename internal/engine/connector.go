package engine

import (
	"context"
	"time"
)

// Protocol is the transfer protocol an indexer speaks.
type Protocol string

const (
	ProtocolTorrent Protocol = "torrent"
	ProtocolUsenet  Protocol = "usenet"
)

// Valid reports whether p is a supported protocol.
func (p Protocol) Valid() bool {
	return p == ProtocolTorrent || p == ProtocolUsenet
}

// Mode is a Torznab/Newznab search mode.
type Mode string

const (
	ModeSearch Mode = "search"
	ModeTV     Mode = "tvsearch"
	ModeMovie  Mode = "movie"
	ModeMusic  Mode = "music"
	ModeBook   Mode = "book"
)

// Valid reports whether m is a supported search mode.
func (m Mode) Valid() bool {
	switch m {
	case ModeSearch, ModeTV, ModeMovie, ModeMusic, ModeBook:
		return true
	default:
		return false
	}
}

// Search parameters understood by the engine. Connector capabilities declare
// the subset each mode actually supports.
const (
	ParamQuery    = "q"
	ParamSeason   = "season"
	ParamEpisode  = "ep"
	ParamIMDbID   = "imdbid"
	ParamTMDbID   = "tmdbid"
	ParamTVDbID   = "tvdbid"
	ParamTVMazeID = "tvmazeid"
	ParamRID      = "rid"
	ParamArtist   = "artist"
	ParamAlbum    = "album"
	ParamAuthor   = "author"
	ParamTitle    = "title"
	ParamYear     = "year"
	ParamGenre    = "genre"
)

var knownParams = map[string]struct{}{
	ParamQuery:    {},
	ParamSeason:   {},
	ParamEpisode:  {},
	ParamIMDbID:   {},
	ParamTMDbID:   {},
	ParamTVDbID:   {},
	ParamTVMazeID: {},
	ParamRID:      {},
	ParamArtist:   {},
	ParamAlbum:    {},
	ParamAuthor:   {},
	ParamTitle:    {},
	ParamYear:     {},
	ParamGenre:    {},
}

// ValidParam reports whether param is one of the parameter names defined by
// the native definition format.
func ValidParam(param string) bool {
	_, ok := knownParams[param]
	return ok
}

// IndexerRef identifies a configured indexer without importing the store.
type IndexerRef struct {
	ID       int64
	Slug     string
	Name     string
	Protocol Protocol
}

// Item is a release as returned by a connector.
type Item struct {
	Protocol    Protocol
	Title       string
	GUID        string
	DetailsURL  string
	DownloadURL string // may contain a passkey: never exposed by the APIs
	Magnet      string
	InfoHash    string
	Size        int64
	Seeders     int64
	Leechers    int64
	Categories  []int
	Published   time.Time
	IMDbID      string
	TMDbID      string
	TVDbID      string
	Attributes  map[string]string
}

// Capabilities describes what an indexer can search.
type Capabilities struct {
	Protocol   Protocol
	Modes      []Mode
	Params     map[Mode][]string
	Categories []int
	// LocalOnly marks a connector that has no remote query endpoint (an RSS
	// feed). It can answer from its cached feed but must not join the live
	// fan-out.
	LocalOnly bool
}

// Supports reports whether mode is declared in the capabilities.
func (c Capabilities) Supports(mode Mode) bool {
	for _, declared := range c.Modes {
		if declared == mode {
			return true
		}
	}
	return false
}

// SupportsParam reports whether param is declared for mode. It does not
// require mode itself to be listed in Modes.
func (c Capabilities) SupportsParam(mode Mode, param string) bool {
	for _, declared := range c.Params[mode] {
		if declared == param {
			return true
		}
	}
	return false
}

// SearchRequest is a normalized query independent of the site's own syntax.
type SearchRequest struct {
	Mode       Mode
	Query      string
	Season     int
	Episode    int
	Year       int
	IMDbID     string
	TMDbID     string
	TVDbID     string
	TVMazeID   string
	RID        string
	Artist     string
	Album      string
	Author     string
	Title      string
	Categories []int
	Limit      int
	Offset     int
	// SkipDetails keeps a live search from opening details pages even when
	// the definition declares resolve_on: search. The download reference is
	// resolved by the grab path instead.
	SkipDetails bool
}

// LatestRequest asks for recent releases, optionally in specific categories.
type LatestRequest struct {
	Categories []int
	Limit      int
}

// Payload is the bytes a download produces: a .torrent or .nzb, or a magnet
// when the tracker answered with a redirect to one.
type Payload struct {
	Body        []byte
	ContentType string
	Filename    string
	Magnet      string
}

// TestPhase is one step of an indexer test (login, search, download).
type TestPhase struct {
	Name     string
	OK       bool
	Message  string
	Duration time.Duration
}

// TestResult reports every phase of an indexer test, not just the final OK.
type TestResult struct {
	OK     bool
	Phases []TestPhase
}

// Connector is the contract implemented by native connectors and by the
// interpreter for definition-based indexers.
type Connector interface {
	Capabilities() Capabilities
	Search(ctx context.Context, req SearchRequest) ([]Item, error)
	Latest(ctx context.Context, req LatestRequest) ([]Item, error)
	Download(ctx context.Context, item Item) (Payload, error)
	Test(ctx context.Context) (TestResult, error)
}

// Config carries what a connector factory needs to build one indexer.
type Config struct {
	Indexer   IndexerRef
	Settings  map[string]string
	Transport *Transport // optional shared transport; factories may ignore it
}

// ConnectorFactory builds a connector bound to one configured indexer.
type ConnectorFactory func(Config) (Connector, error)
