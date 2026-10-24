// Package feed implements the input side of indexarr: RSS/Atom parsing, the
// Torznab client, Prowlarr's JSON search API and .torrent handling. It has no
// dependency on the database, so every parser can be tested against a fake
// HTTP server.
package feed

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Item is one normalised search result, whatever the source protocol was.
type Item struct {
	Title      string
	GUID       string
	Link       string
	Magnet     string
	TorrentURL string
	InfoHash   string
	Size       int64
	Seeders    int64
	Leechers   int64
	Category   string
	Published  time.Time
	IMDbID     string
	TMDbID     string
	TVDbID     string
}

// NormalizeInfoHash accepts a 40-character hexadecimal infohash or its
// 32-character base32 form and returns the lowercase hexadecimal form.
func NormalizeInfoHash(raw string) string {
	value := strings.TrimSpace(raw)
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "urn:btih:") {
		value = value[len("urn:btih:"):]
		lower = strings.ToLower(value)
	}
	if len(value) == 40 {
		if _, err := hex.DecodeString(strings.ToLower(value)); err == nil {
			return strings.ToLower(value)
		}
	}
	if len(value) == 32 {
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(value))
		if err == nil && len(decoded) == 20 {
			return hex.EncodeToString(decoded)
		}
	}
	return ""
}

// MagnetHash extracts the BTIH hash from a magnet link.
func MagnetHash(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "magnet:") {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	for _, xt := range parsed.Query()["xt"] {
		if strings.HasPrefix(strings.ToLower(xt), "urn:btih:") {
			if hash := NormalizeInfoHash(xt); hash != "" {
				return hash
			}
		}
	}
	return ""
}

// MagnetFromHash builds a minimal magnet link carrying only the infohash and
// the display name.
func MagnetFromHash(hash, title string) string {
	hash = NormalizeInfoHash(hash)
	if hash == "" {
		return ""
	}
	magnet := "magnet:?xt=urn:btih:" + hash
	if name := strings.TrimSpace(title); name != "" {
		magnet += "&dn=" + url.QueryEscape(name)
	}
	return magnet
}

// LooksLikeTorrentURL reports whether an RSS link/enclosure is a .torrent
// download rather than a link to a web page or a media file.
func LooksLikeTorrentURL(raw string, contentType string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	if strings.HasPrefix(strings.ToLower(contentType), "application/x-bittorrent") {
		return true
	}
	path := strings.ToLower(parsed.Path)
	if strings.HasSuffix(path, ".torrent") {
		return true
	}
	// Nyaa (and feeds that republish its items) serves the torrent file at
	// /view/<id>/torrent with Content-Type application/x-bittorrent.
	if strings.HasSuffix(path, "/torrent") {
		return true
	}
	if strings.Contains(path, "/download") || strings.Contains(path, "/dl/") ||
		strings.Contains(path, "torrent/download") {
		return true
	}
	return false
}

// ParseSize parses a size that may be empty.
func ParseSize(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

var dateLayouts = []string{
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	time.RFC3339Nano,
	time.RFC3339,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

// ParseTime parses the date formats found in feeds and Torznab responses.
func ParseTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range dateLayouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// DedupKey returns the stable identity of an item: the infohash when known,
// otherwise a namespaced GUID, URL or title+size fingerprint.
func DedupKey(item Item, sourceName string) string {
	if hash := NormalizeInfoHash(item.InfoHash); hash != "" {
		return hash
	}
	if hash := MagnetHash(item.Magnet); hash != "" {
		return hash
	}
	if guid := strings.TrimSpace(item.GUID); guid != "" {
		return "guid:" + hashString(sourceName+"\x00"+guid)
	}
	if link := strings.TrimSpace(item.TorrentURL); link != "" {
		return "url:" + hashString(link)
	}
	if link := strings.TrimSpace(item.Link); link != "" {
		return "url:" + hashString(link)
	}
	return "title:" + hashString(strings.ToLower(strings.TrimSpace(item.Title))+"\x00"+strconv.FormatInt(item.Size, 10))
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// NormalizeCategory returns the first numeric Torznab category found in a
// comma-separated list or in a feed's category text. Text-only categories
// ("TV > HD") carry no numeric id and are ignored: storing them would make the
// Torznab output invalid.
func NormalizeCategory(values ...string) string {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			numeric := true
			for _, r := range part {
				if r < '0' || r > '9' {
					numeric = false
					break
				}
			}
			if numeric {
				return part
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func atoi(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func describeHTTPError(status int, body []byte) error {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Errorf("HTTP %d", status)
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return fmt.Errorf("HTTP %d: %s", status, redactSecrets(text))
}

var (
	secretPattern = regexp.MustCompile(`(?i)(apikey|api_key|passkey|rsskey|authkey|token|secret)=([^&\s"']+)`)
	urlPattern    = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
)

// redactSecrets removes the credentials that net/http puts in transport error
// messages (the URL is included verbatim) before an error reaches the log, the
// run history or the web interface. Besides query parameters it masks long
// secret-like path segments, which is how many trackers embed rsskeys and
// passkeys (for example /rss/<hex>).
func redactSecrets(text string) string {
	text = secretPattern.ReplaceAllString(text, "$1=***")
	return urlPattern.ReplaceAllStringFunc(text, redactURLPath)
}

// RedactText makes arbitrary response text safe to store: every known secret
// value is removed first, then the usual URL query parameters and opaque path
// segments are masked. It suits a raw sample body that may quote tracker links
// next to ordinary release metadata.
func RedactText(text string, secrets []string) string {
	for _, secret := range secrets {
		if len(secret) >= 3 {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return redactSecrets(text)
}

func redactURLPath(raw string) string {
	trimmed := strings.TrimRight(raw, ".,;:!?)]}")
	suffix := raw[len(trimmed):]
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return raw
	}
	path, changed := redactPath(parsed.Path)
	if !changed {
		return raw
	}
	parsed.Path = path
	redacted := parsed.String() + suffix
	// url.String escapes the asterisks we use as the mask; the marker is for
	// humans reading a log, so keep it readable instead of percent-encoded.
	redacted = strings.ReplaceAll(redacted, "%2A%2A%2A", "***")
	return strings.ReplaceAll(redacted, "%2a%2a%2a", "***")
}

func redactPath(path string) (string, bool) {
	segments := strings.Split(path, "/")
	changed := false
	for i, segment := range segments {
		if looksLikeSecretPathSegment(segment) {
			segments[i] = "***"
			changed = true
		}
	}
	if !changed {
		return path, false
	}
	return strings.Join(segments, "/"), true
}

// looksLikeSecretPathSegment recognises opaque identifiers (hex keys, rsskeys,
// passkeys) while leaving normal path words and short ids alone.
func looksLikeSecretPathSegment(segment string) bool {
	if len(segment) < 16 {
		// All-uppercase opaque tokens are unusual in ordinary paths and are a
		// common shape for an rsskey embedded by a feed (for example
		// /SYNTHETICRSSKEY/feed.xml). Lower the length threshold only for
		// that shape, so normal words and short ids stay readable.
		if len(segment) < 12 || !allUpperAlphaNumeric(segment) {
			return false
		}
		return true
	}
	digits, letters := false, false
	hexOnly := true
	for _, r := range segment {
		switch {
		case r >= '0' && r <= '9':
			digits = true
		case (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
			letters = true
		case (r >= 'g' && r <= 'z') || (r >= 'G' && r <= 'Z'):
			letters = true
			hexOnly = false
		case r == '-' || r == '_' || r == '~' || r == '+' || r == '=':
			hexOnly = false
		default:
			return false
		}
	}
	if letters && hexOnly {
		return true
	}
	return digits && letters
}

func allUpperAlphaNumeric(segment string) bool {
	letters := false
	for _, r := range segment {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'A' && r <= 'Z':
			letters = true
		default:
			return false
		}
	}
	return letters
}

// IsSensitiveURL reports whether a URL carries credentials in userinfo, a
// secret-like query parameter or an opaque path segment. It is used to decide
// that a URL setting belongs in the vault instead of config_json.
func IsSensitiveURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if parsed.User != nil {
		return true
	}
	for name := range parsed.Query() {
		if isSensitiveParamName(name) {
			return true
		}
	}
	if _, changed := redactPath(parsed.Path); changed {
		return true
	}
	return false
}

func isSensitiveParamName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "rsskey", "passkey", "apikey", "api_key", "authkey", "key", "token", "secret", "e":
		return true
	}
	return false
}

// RedactURLForDisplay is RedactURL for the UI: sensitive query values and
// opaque path segments become bullets, so the operator can recognise the URL
// without reading its key.
func RedactURLForDisplay(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "[invalid URL]"
	}
	parsed.User = nil
	parsed.Fragment = ""
	if parsed.RawQuery != "" {
		query := parsed.Query()
		for name := range query {
			if isSensitiveParamName(name) {
				query.Set(name, "***")
			}
		}
		parsed.RawQuery = query.Encode()
	}
	parsed.Path, _ = redactPath(parsed.Path)
	redacted := parsed.String()
	redacted = strings.ReplaceAll(redacted, "%2A%2A%2A", "••••")
	redacted = strings.ReplaceAll(redacted, "%2a%2a%2a", "••••")
	redacted = strings.ReplaceAll(redacted, "***", "••••")
	return redacted
}

// RedactURL returns a URL that is safe to show in logs or the UI: embedded
// userinfo, the query and long secret-like path segments are removed. WP14 can
// use it for the source list without exposing rsskeys.
func RedactURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "[invalid URL]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path, _ = redactPath(parsed.Path)
	redacted := parsed.String()
	redacted = strings.ReplaceAll(redacted, "%2A%2A%2A", "***")
	return strings.ReplaceAll(redacted, "%2a%2a%2a", "***")
}
