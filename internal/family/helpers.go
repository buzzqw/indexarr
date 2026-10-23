// Package family implements native connectors for tracker and indexer
// families. A single connector covers every site that runs the same software,
// so adding a site only requires configuration.
package family

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// secretQueryPattern matches credential-bearing query parameters. Connector
// errors never carry raw URLs, but this is a second line of defence for
// transport errors and API messages that quote a request.
var secretQueryPattern = regexp.MustCompile(`(?i)(api[_-]?key|api[_-]?token|apikey|api_token|passkey|rsskey|authkey|token|secret)=([^&\s"'<>]+)`)

// safeFilename turns a release title into a portable file name with ext.
// Path separators and control characters are removed, runs of whitespace are
// collapsed and the result is truncated to a conservative byte length.
func safeFilename(title, ext string) string {
	name := strings.TrimSpace(title)
	if name == "" {
		name = "download"
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f:
			return -1
		case r == '/', r == '\\', r == ':', r == '*', r == '?', r == '"', r == '<', r == '>', r == '|':
			return '_'
		default:
			return r
		}
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "download"
	}
	for len(name) > 180 {
		_, size := utf8.DecodeLastRuneInString(name)
		if size <= 0 {
			break
		}
		name = name[:len(name)-size]
	}
	name = strings.Trim(name, ". ")

	ext = strings.TrimSpace(ext)
	if ext == "" {
		return name
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if !strings.HasSuffix(strings.ToLower(name), strings.ToLower(ext)) {
		name += ext
	}
	return name
}

// coerceString accepts a JSON string, number, or boolean and returns its
// textual form. Objects, arrays, null and invalid JSON produce "".
func coerceString(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return ""
	}
	if value[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return ""
		}
		return text
	}
	if value[0] == '{' || value[0] == '[' {
		return ""
	}
	return value
}

// coerceIDString is coerceString for identifiers: it also strips a trailing
// ".0" that some APIs emit for numeric ids.
func coerceIDString(raw json.RawMessage) string {
	value := strings.TrimSpace(coerceString(raw))
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, ".0") {
		if _, err := strconv.ParseFloat(value, 64); err == nil {
			value = strings.TrimSuffix(value, ".0")
		}
	}
	if value == "0" {
		return ""
	}
	return value
}

// coerceInt64 parses a JSON number or numeric string. Anything else is 0.
func coerceInt64(raw json.RawMessage) int64 {
	value := strings.TrimSpace(coerceString(raw))
	if value == "" {
		return 0
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(value, 64); err == nil {
		return int64(f)
	}
	return 0
}

// coerceInt parses a JSON number or numeric string. Anything else is 0.
func coerceInt(raw json.RawMessage) int {
	return int(coerceInt64(raw))
}

// parseDate accepts the formats indexers commonly use. Unknown values return
// the zero time rather than an error.
func parseDate(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// infoHashFromMagnet extracts the btih hash from a magnet URI.
func infoHashFromMagnet(magnet string) string {
	magnet = strings.TrimSpace(magnet)
	if magnet == "" {
		return ""
	}
	parsed, err := url.Parse(magnet)
	if err != nil || !strings.EqualFold(parsed.Scheme, "magnet") {
		return ""
	}
	for _, xt := range parsed.Query()["xt"] {
		lower := strings.ToLower(xt)
		if idx := strings.Index(lower, "urn:btih:"); idx >= 0 {
			hash := strings.TrimSpace(xt[idx+len("urn:btih:"):])
			if hash != "" {
				return hash
			}
		}
	}
	return ""
}

// redact removes credential query values and any explicitly supplied secret
// from text. Connector errors must pass through this before being returned.
func redact(text string, secrets ...string) string {
	text = secretQueryPattern.ReplaceAllString(text, "$1=[redacted]")
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
			text = strings.ReplaceAll(text, url.QueryEscape(secret), "[redacted]")
		}
	}
	return text
}

// redactedError keeps the transport error chain reachable through errors.Is
// while its message is the redacted one. The original error may quote a full
// URL, so its text never reaches the caller.
type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.cause }

// redactError wraps err with prefix and redacts credentials from its message.
// The returned error keeps the cause for errors.Is (the challenge sentinels,
// for example) without exposing the original text.
func redactError(prefix string, err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	message := prefix + ": " + redact(err.Error(), secrets...)
	if message == err.Error() {
		return err
	}
	return &redactedError{message: message, cause: err}
}

// parseBaseURL validates an indexer base URL.
func parseBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("base URL is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid base URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("base URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("base URL has no host")
	}
	return parsed, nil
}

// joinPath appends path to a normalized base URL.
func joinPath(base, path string) string {
	base = strings.TrimRight(base, "/")
	if path == "" {
		return base
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

// withQuery merges values into rawURL without dropping an existing query.
func withQuery(rawURL string, values url.Values) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("invalid URL")
	}
	query := parsed.Query()
	for key, list := range values {
		for _, value := range list {
			query.Add(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// resolveURL resolves ref against base when ref is relative.
func resolveURL(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || base == nil {
		return ref
	}
	parsed, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(parsed).String()
}

// firstNonEmpty returns the first non-empty trimmed value.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// parseCategoryList parses a comma or whitespace separated list of numeric
// category ids.
func parseCategoryList(value string) ([]int, error) {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
	if len(fields) == 0 {
		return nil, nil
	}
	out := make([]int, 0, len(fields))
	seen := make(map[int]struct{}, len(fields))
	for _, field := range fields {
		id, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid category id %q", field)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}
