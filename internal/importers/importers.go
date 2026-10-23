// Package importers turns the configuration of a running Prowlarr or Jackett
// instance into a neutral, validated import plan for indexarr 2.
//
// The importers never write to the store and never talk to the trackers
// themselves: they read the manager's configuration and return the indexers,
// applications and proxies the coordinator can create. Secrets (API keys,
// usernames and passwords) are carried in the plan because the caller has to
// put them in the vault, but they are removed from every warning and error and
// never written to logs by this package.
//
// The plan builders depend only on the standard library and the catalog. The
// apply path in apply.go also uses the store and the vault, so the CLI and the
// web UI share exactly the same write logic.
package importers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Protocol is the transport a configured indexer speaks.
type Protocol string

const (
	ProtocolTorrent Protocol = "torrent"
	ProtocolUsenet  Protocol = "usenet"
)

// Indexer is one indexer found in Prowlarr or Jackett, normalised to the
// fields indexarr 2 needs. APIKey, Username and Password are sensitive: they
// must be vaulted and must never be logged.
type Indexer struct {
	Name           string
	Implementation string // Prowlarr implementation, e.g. "Torznab", "Cardigann", "Newznab"
	Protocol       Protocol
	DefinitionHint string // normalized slug/id suggested for a native definition
	// RemoteID is the indexer id inside the manager: the Prowlarr indexer id
	// or the Jackett Indexers/<id>.json id. It is the per-indexer endpoint
	// segment, not a secret.
	RemoteID string
	// BaseURL is the per-indexer Torznab endpoint when the manager exposes
	// one (Jackett), otherwise the site base URL (Prowlarr).
	BaseURL string
	// SiteURL is the tracker site URL when the manager exposes it separately
	// from the API endpoint. It is used to match a verified catalog
	// definition by host.
	SiteURL    string
	APIKey     string // sensitive: never log or include in errors
	Username   string // sensitive
	Password   string // sensitive
	Categories []int
	Tags       []string
	Priority   int
	Enabled    bool
	// DisabledReason explains why a probe disabled an upstream indexer. It is
	// non-secret and only reaches the import table and the UI.
	DisabledReason string
	Fields         map[string]string // non-secret Prowlarr/Jackett fields
}

// Application is one downstream *arr instance configured in Prowlarr.
type Application struct {
	Kind      string // sonarr/radarr/lidarr/readarr/whisparr/mylar3/lazylibrarian
	Name      string
	URL       string
	APIKey    string
	SyncLevel string
}

// Proxy is an outbound proxy or challenge solver configured in the manager.
type Proxy struct {
	Kind     string
	URL      string
	Username string
	Password string // sensitive
}

// Plan is the neutral result of an import. It contains no database identity:
// the coordinator decides what already exists and what to create.
type Plan struct {
	Source string // "prowlarr" or "jackett"
	// ManagerURL is the sanitized base URL of the manager (no credentials,
	// query or fragment). It builds the per-indexer Torznab endpoint.
	ManagerURL string
	// ManagerAPIKey is the key that calls the manager's per-indexer Torznab
	// endpoint (Prowlarr's API key or Jackett's API key). Sensitive: never log
	// or include in errors.
	ManagerAPIKey string
	Indexers      []Indexer
	Applications  []Application
	Proxies       []Proxy
	Warnings      []string
}

// OverrideManagerEndpoint replaces the manager base URL and API key of a
// Jackett plan. The coordinator imports from a copy of the Jackett data
// directory, but the copy may be stale and may name a bind address that is not
// reachable. The live URL and the key read from the old indexarr database win;
// the imported bridge indexers then point at the running instance.
//
// baseURL may be empty to keep the URL from the copied ServerConfig.json. The
// returned errors never include the API key.
func OverrideManagerEndpoint(plan Plan, baseURL, apiKey string) (Plan, error) {
	apiKey = strings.TrimSpace(apiKey)
	rawURL := strings.TrimSpace(baseURL)
	if rawURL != "" {
		parsed, err := url.Parse(rawURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Plan{}, errors.New("importers: manager URL must be http(s) with a host")
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		plan.ManagerURL = strings.TrimRight(parsed.String(), "/")
	}
	if apiKey != "" {
		plan.ManagerAPIKey = apiKey
	}
	if plan.ManagerURL == "" {
		return Plan{}, errors.New("importers: manager URL is empty")
	}
	if rawURL != "" || apiKey != "" {
		for i := range plan.Indexers {
			if plan.Source == "jackett" && plan.Indexers[i].RemoteID != "" {
				plan.Indexers[i].BaseURL = plan.ManagerURL + "/api/v2.0/indexers/" +
					url.PathEscape(plan.Indexers[i].RemoteID) + "/results/torznab/"
			}
			if apiKey != "" {
				plan.Indexers[i].APIKey = apiKey
			}
		}
	}
	return plan, nil
}

// indexerRecord keeps the source id for deterministic ordering without
// exposing it in the public type.
type indexerRecord struct {
	indexer  Indexer
	sourceID string
}

func sortIndexerRecords(records []indexerRecord) []Indexer {
	sort.SliceStable(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if c := strings.Compare(strings.ToLower(a.indexer.Name), strings.ToLower(b.indexer.Name)); c != 0 {
			return c < 0
		}
		if a.indexer.Name != b.indexer.Name {
			return a.indexer.Name < b.indexer.Name
		}
		return compareIDs(a.sourceID, b.sourceID) < 0
	})
	out := make([]Indexer, len(records))
	for i, record := range records {
		out[i] = record.indexer
	}
	return out
}

// compareIDs compares numeric ids numerically and everything else
// lexicographically, so "2" sorts before "10" while names stay stable.
func compareIDs(a, b string) int {
	an, aerr := strconv.ParseInt(a, 10, 64)
	bn, berr := strconv.ParseInt(b, 10, 64)
	if aerr == nil && berr == nil {
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(a, b)
}

func sortApplications(items []Application) {
	sort.SliceStable(items, func(i, j int) bool {
		if c := strings.Compare(strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)); c != 0 {
			return c < 0
		}
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].Kind < items[j].Kind
	})
}

// Slug normalizes a name into the stable slug used for imported indexers.
// Importers and the import command share it so a re-run finds the same row.
func Slug(value string) string { return slugify(value) }

// slugify lowercases a value and turns every run of non-alphanumeric
// characters into a single dash. It is the same normalisation the store uses
// for legacy slugs.
func slugify(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// definitionHint suggests a native definition id for a Prowlarr indexer.
func definitionHint(implementation, name, cardigannID string) string {
	implementationSlug := slugify(implementation)
	nameSlug := slugify(name)
	if strings.EqualFold(implementation, "Cardigann") {
		if id := slugify(cardigannID); id != "" {
			return id
		}
		return nameSlug
	}
	switch {
	case implementationSlug == "":
		return nameSlug
	case nameSlug == "":
		return implementationSlug
	default:
		return implementationSlug + "-" + nameSlug
	}
}

// normalizeProtocol trusts an explicit protocol and otherwise infers Usenet
// from the Newznab implementation.
func normalizeProtocol(explicit, implementation string) Protocol {
	switch strings.ToLower(strings.TrimSpace(explicit)) {
	case "usenet", "newznab":
		return ProtocolUsenet
	case "torrent", "torznab":
		return ProtocolTorrent
	}
	if strings.EqualFold(strings.TrimSpace(implementation), "Newznab") {
		return ProtocolUsenet
	}
	return ProtocolTorrent
}

// stringValue renders the scalar and list values Prowlarr and Jackett use in
// their JSON. Secrets are values too; callers decide where they may go.
func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case bool:
		return strconv.FormatBool(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := stringValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ",")
	case []string:
		return strings.Join(typed, ",")
	case map[string]any:
		for _, key := range []string{"name", "label", "id", "value"} {
			if text := stringValue(typed[key]); text != "" {
				return text
			}
		}
		return ""
	default:
		return ""
	}
}

func intValue(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case json.Number:
		if number, err := typed.Int64(); err == nil {
			return int(number)
		}
		if number, err := typed.Float64(); err == nil {
			return int(number)
		}
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case string:
		if number, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return number
		}
	}
	return 0
}

func boolValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	case json.Number:
		number, err := typed.Int64()
		if err == nil {
			return number != 0, true
		}
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	}
	return false, false
}

// parseCategories accepts numbers, comma-separated strings and category
// objects (Prowlarr capabilities use {"id": 2000}; Jackett uses {"ID": 2000}).
// The result is deduplicated and sorted ascending.
func parseCategories(value any) []int {
	var ids []int
	var walk func(any)
	walk = func(item any) {
		switch typed := item.(type) {
		case nil:
		case []any:
			for _, element := range typed {
				walk(element)
			}
		case map[string]any:
			for _, key := range []string{"id", "ID", "Id"} {
				if raw, ok := typed[key]; ok {
					walk(raw)
					return
				}
			}
		case string:
			for _, part := range strings.Split(typed, ",") {
				if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
					walk(id)
				}
			}
		default:
			if id := intValue(item); id > 0 {
				ids = append(ids, id)
			}
		}
	}
	walk(value)

	seen := make(map[int]bool, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// parseTags accepts a list of ids or objects with name/label.
func parseTags(value any) []string {
	var items []any
	switch typed := value.(type) {
	case []any:
		items = typed
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		items = []any{typed}
	default:
		if text := stringValue(value); text != "" {
			items = []any{text}
		}
	}
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		tag := stringValue(item)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

// parseFields flattens Prowlarr's [{name, value}] field list. It keeps the
// values verbatim because the caller has to extract secrets from them; the
// public Fields map is filtered separately by isSecretField.
func parseFields(value any) map[string]string {
	out := make(map[string]string)
	items, ok := value.([]any)
	if !ok {
		if fields, ok := value.(map[string]any); ok {
			for name, raw := range fields {
				out[strings.TrimSpace(name)] = stringValue(raw)
			}
		}
		return out
	}
	for _, item := range items {
		field, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(stringValue(field["name"]))
		if name == "" {
			continue
		}
		out[name] = stringValue(field["value"])
	}
	return out
}

// fieldValue looks a field up by any of the given names, tolerating the
// casing differences between manager versions.
func fieldValue(fields map[string]string, names ...string) string {
	for _, name := range names {
		if value, ok := fields[name]; ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, name := range names {
			if strings.EqualFold(key, name) {
				if trimmed := strings.TrimSpace(fields[key]); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

// isSecretField reports whether a manager field must stay out of the
// non-secret Fields map. Usernames are treated as sensitive too.
func isSecretField(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.NewReplacer("_", "", "-", "", " ", "").Replace(normalized)
	switch normalized {
	case "apikey", "password", "passwd", "username", "user", "passkey", "rsskey",
		"cookie", "cookies", "token", "secret", "authkey", "credentials",
		"session", "sessionid", "login":
		return true
	}
	for _, marker := range []string{"password", "apikey", "passkey", "rsskey",
		"token", "secret", "cookie"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func publicFields(fields map[string]string) map[string]string {
	out := make(map[string]string, len(fields))
	for name, value := range fields {
		if name == "" || isSecretField(name) {
			continue
		}
		out[name] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sanitizeURL removes userinfo and query parameters so a URL is safe to put
// in an error or warning.
func sanitizeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[invalid URL]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// redactSecrets removes known secret values (and their URL-escaped forms)
// from free-form text. Longer values go first so a secret that contains
// another one is removed whole.
func redactSecrets(text string, secrets ...string) string {
	unique := make(map[string]bool, len(secrets)*3)
	var values []string
	for _, secret := range secrets {
		for _, candidate := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if candidate == "" || unique[candidate] {
				continue
			}
			unique[candidate] = true
			values = append(values, candidate)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	return text
}

func addWarning(warnings *[]string, message string, secrets ...string) {
	*warnings = append(*warnings, redactSecrets(message, secrets...))
}

func sanitizeWarnings(warnings []string, secrets ...string) []string {
	for i, warning := range warnings {
		warnings[i] = redactSecrets(warning, secrets...)
	}
	return warnings
}

// decodeObject decodes one raw JSON value into an object, rejecting arrays,
// scalars and null entries instead of failing the whole import.
func decodeObject(raw json.RawMessage) (map[string]any, bool) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, false
	}
	return object, true
}

// lookup finds a value by any key, case-insensitively. Jackett's JSON uses
// PascalCase while Prowlarr uses camelCase.
func lookup(object map[string]any, names ...string) (any, bool) {
	for _, name := range names {
		if value, ok := object[name]; ok {
			return value, true
		}
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, name := range names {
			if strings.EqualFold(key, name) {
				return object[key], true
			}
		}
	}
	return nil, false
}

func lookupString(object map[string]any, names ...string) string {
	value, _ := lookup(object, names...)
	return stringValue(value)
}

func lookupBool(object map[string]any, names ...string) (bool, bool) {
	value, ok := lookup(object, names...)
	if !ok {
		return false, false
	}
	return boolValue(value)
}
