// Package smartfeed recognises and normalises the personal phpBB Smartfeed
// used by MirCrew.
//
// A Smartfeed URL is a secret: `u` identifies the forum user and `e` is a
// token that changes every time the Smartfeed page is opened. The package has
// no HTTP client and no credentials of its own; it only parses URLs and feed
// text so the behaviour can be tested entirely offline.
package smartfeed

import (
	"bytes"
	"errors"
	"fmt"
	stdhtml "html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// DefinitionID is the catalog definition used by the latest channel.
	DefinitionID = "mircrew-smartfeed"
	// NativeDefinitionID is the MirCrew search/grab definition it pairs with.
	NativeDefinitionID = "mircrew"
	// Site groups the two channels in the indexer list.
	Site = "mircrew-releases.org"

	// AttributePending marks an item as a topic that still has to be opened by
	// the native MirCrew indexer at grab time.
	AttributePending = "smartfeed.pending"
	// AttributeForum carries the phpBB forum id when the link has one.
	AttributeForum = "smartfeed.forum"
)

// Info is the result of recognising a Smartfeed URL.
type Info struct {
	URL   *url.URL
	User  string
	Token string
	T     int
}

// Recognize reports whether raw is a phpBB Smartfeed feed URL. The user id and
// the token are both required: a bare /smartfeed/feed address is not personal
// and cannot be fetched without them. /smartfeed/ui is the page that shows the
// URL, not the feed itself.
func Recognize(raw string) (Info, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return Info{}, false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return Info{}, false
	}
	path := strings.ToLower(strings.TrimRight(parsed.Path, "/"))
	if !strings.Contains(path, "smartfeed") || strings.HasSuffix(path, "/ui") {
		return Info{}, false
	}
	query := parsed.Query()
	user := strings.TrimSpace(query.Get("u"))
	token := strings.TrimSpace(query.Get("e"))
	if user == "" || token == "" {
		return Info{}, false
	}
	postType, _ := strconv.Atoi(strings.TrimSpace(query.Get("t")))
	return Info{URL: parsed, User: user, Token: token, T: postType}, true
}

// IsSmartfeedURL is the boolean form used by URL secrecy checks.
func IsSmartfeedURL(raw string) bool {
	_, ok := Recognize(raw)
	return ok
}

// SuggestsAnyPost reports whether the URL still selects only the first post of
// every topic (t=1). MirCrew adds new episodes as replies, so t=2 is the
// recommended value.
func SuggestsAnyPost(raw string) bool {
	info, ok := Recognize(raw)
	return ok && info.T == 1
}

// WithAnyPost returns the same Smartfeed URL with t=2. Every other parameter,
// including repeated f values and d/x, is preserved verbatim. The boolean is
// false when the URL is not a Smartfeed URL.
func WithAnyPost(raw string) (string, bool) {
	info, ok := Recognize(raw)
	if !ok {
		return strings.TrimSpace(raw), false
	}
	query := info.URL.Query()
	query.Set("t", "2")
	copy := *info.URL
	copy.RawQuery = query.Encode()
	return copy.String(), true
}

// UILink returns the safe, token-free address of the Smartfeed page where the
// user copies a fresh URL. It always points at the same host as the feed.
func UILink(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "https://" + Site + "/smartfeed/ui"
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	return scheme + "://" + parsed.Host + "/smartfeed/ui"
}

// ExpiredError is the message shown when the feed answers with a login page or
// an authentication error. The token is not part of the message: only the
// public Smartfeed page is named, so the message is safe for logs and history.
func ExpiredError(raw string) error {
	return fmt.Errorf("Smartfeed expired: paste a new URL from %s", UILink(raw))
}

// PendingGrabError is returned if a pending Smartfeed item reaches the RSS
// connector's own Download. Pending topics must be grabbed through the paired
// native MirCrew indexer; the Smartfeed poll itself never opens a topic.
func PendingGrabError() error {
	return errors.New("Smartfeed item is a pending topic: configure the MirCrew indexer to grab it")
}

var (
	cdataPattern = regexp.MustCompile(`(?is)<!\[CDATA\[(.*?)\]\]>`)
	tagPattern   = regexp.MustCompile(`(?is)<[^>]*>`)
	replyPattern = regexp.MustCompile(`(?i)\s+::\s+(?:Risposta di|Autore|Reply by|Risposta|Re:).*$`)
	forumPattern = regexp.MustCompile(`^[^:]{1,40}?\s+::\s+`)
)

// NormalizeDocument repairs the one malformed shape FlareSolverr has been
// seen to return: CDATA markers escaped as HTML entities. It does not
// unescape the whole document, because that would turn the &amp; inside links
// into a bare & and make valid XML invalid.
func NormalizeDocument(data []byte) []byte {
	if bytes.Contains(data, []byte("&lt;![CDATA[")) {
		data = bytes.ReplaceAll(data, []byte("&lt;![CDATA["), []byte("<![CDATA["))
		data = bytes.ReplaceAll(data, []byte("]]&gt;"), []byte("]]>"))
	}
	return data
}

// CleanTitle drops the Smartfeed decorations around a topic title. The feed
// reads "Forum :: <title> :: Risposta di <user>"; neither the forum nor the
// reply author belongs to the release name. CDATA markers, escaped HTML and
// tags are removed too, because phpBB and FlareSolverr both add them.
func CleanTitle(raw string) string {
	title := strings.TrimSpace(raw)
	for i := 0; i < 2; i++ {
		title = stdhtml.UnescapeString(title)
		title = cdataPattern.ReplaceAllString(title, "$1")
		title = strings.ReplaceAll(title, "<![CDATA[", "")
		title = strings.ReplaceAll(title, "]]>", "")
		title = tagPattern.ReplaceAllString(title, " ")
	}
	title = strings.Join(strings.Fields(title), " ")
	title = replyPattern.ReplaceAllString(title, "")
	title = forumPattern.ReplaceAllString(title, "")
	return strings.TrimSpace(title)
}

// TopicURL canonicalises a phpBB topic link: no fragment, and only the forum
// and topic ids. A reply link points at the same topic and must deduplicate
// with it.
func TopicURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	parsed.Fragment = ""
	query := parsed.Query()
	forum := strings.TrimSpace(query.Get("f"))
	topic := strings.TrimSpace(query.Get("t"))
	if topic != "" {
		clean := url.Values{}
		if forum != "" {
			clean.Set("f", forum)
		}
		clean.Set("t", topic)
		parsed.RawQuery = clean.Encode()
	}
	return parsed.String()
}

// ForumID returns the phpBB forum id from a topic link, or 0.
func ForumID(raw string) int {
	parsed, err := url.Parse(TopicURL(raw))
	if err != nil {
		return 0
	}
	value, _ := strconv.Atoi(strings.TrimSpace(parsed.Query().Get("f")))
	return value
}

// CategoryForForum maps the MirCrew forum ids used by the Smartfeed to the
// standard Torznab categories.
func CategoryForForum(forum int) int {
	switch forum {
	case 26:
		return 2040
	case 51, 52:
		return 5040
	default:
		return 8000
	}
}

// Published returns the feed date, falling back to now only when the feed has
// no usable date. A pending topic without a date is new activity, not old.
func Published(value time.Time, now time.Time) time.Time {
	if !value.IsZero() {
		return value
	}
	return now
}
