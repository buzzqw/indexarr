package importers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	jackettDefaultPort = 9117
	jackettFileLimit   = 8 << 20
)

// FromJackettConfig reads a Jackett data directory or a single
// ServerConfig.json and returns one Torznab bridge indexer per configured
// Jackett indexer. Secrets stay in the plan; warnings and errors are
// redacted.
//
// The path may be:
//   - the Jackett data directory (containing Jackett/ServerConfig.json and
//     Jackett/Indexers/*.json);
//   - the Jackett directory itself (containing ServerConfig.json and
//     Indexers/);
//   - a single ServerConfig.json (its sibling Indexers/ directory is used when
//     present).
//
// One broken indexer file never fails the import: it becomes a warning and is
// skipped. A missing or invalid ServerConfig.json is an error.
func FromJackettConfig(ctx context.Context, path string) (Plan, error) {
	const op = "jackett"

	plan := Plan{Source: op}
	if err := ctx.Err(); err != nil {
		return Plan{}, fmt.Errorf("%s: %w", op, err)
	}

	configPath, indexersDir, err := locateJackettConfig(path)
	if err != nil {
		return Plan{}, err
	}
	config, err := readJSONObject(configPath)
	if err != nil {
		return Plan{}, fmt.Errorf("%s: read %s: %w", op, filepath.Base(configPath), err)
	}

	apiKey := lookupString(config, "APIKey", "ApiKey", "apiKey")
	if apiKey == "" {
		plan.Warnings = append(plan.Warnings, "jackett: ServerConfig.json has no API key")
	}

	port := intValue(lookupValue(config, "Port", "port"))
	if port <= 0 || port > 65535 {
		if _, present := lookup(config, "Port", "port"); present {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("jackett: invalid port, using %d", jackettDefaultPort))
		}
		port = jackettDefaultPort
	}
	host := jackettHost(lookupString(config, "LocalBindAddress", "localBindAddress", "LocalBind"))
	base := "http://" + net.JoinHostPort(host, strconv.Itoa(port))

	if proxy, warning := jackettProxy(config); warning != "" {
		plan.Warnings = append(plan.Warnings, warning)
	} else if proxy != nil {
		plan.Proxies = append(plan.Proxies, *proxy)
	}

	entries, err := os.ReadDir(indexersDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			plan.Warnings = append(plan.Warnings,
				fmt.Sprintf("jackett: no Indexers directory at %s", indexersDir))
			sanitizeJackettPlan(&plan, apiKey)
			return plan, nil
		}
		return Plan{}, fmt.Errorf("%s: read indexers directory: %w", op, err)
	}

	records := make([]indexerRecord, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Plan{}, fmt.Errorf("%s: %w", op, err)
		}
		if entry.IsDir() {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			plan.Warnings = append(plan.Warnings,
				fmt.Sprintf("jackett: indexer file %q is a symbolic link, skipped", entry.Name()))
			continue
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}

		filePath := filepath.Join(indexersDir, entry.Name())
		// ReadDir names cannot contain separators, but check the joined path
		// anyway: the import must never read outside the Indexers directory.
		if !pathWithin(indexersDir, filePath) {
			plan.Warnings = append(plan.Warnings,
				fmt.Sprintf("jackett: indexer file %q escapes the Indexers directory, skipped", entry.Name()))
			continue
		}

		indexer, id, warning := readJackettIndexer(filePath, base, apiKey)
		if warning != "" {
			plan.Warnings = append(plan.Warnings, warning)
			continue
		}
		records = append(records, indexerRecord{indexer: indexer, sourceID: id})
	}

	plan.Indexers = sortIndexerRecords(records)
	sanitizeJackettPlan(&plan, apiKey)
	return plan, nil
}

// sanitizeJackettPlan redacts the API key and any proxy credentials from
// warnings before they leave the package.
func sanitizeJackettPlan(plan *Plan, apiKey string) {
	secrets := []string{apiKey}
	for _, proxy := range plan.Proxies {
		secrets = append(secrets, proxy.Username, proxy.Password)
	}
	plan.Warnings = sanitizeWarnings(plan.Warnings, secrets...)
}

// locateJackettConfig resolves a directory or file argument to the server
// configuration and the Indexers directory that belongs to it.
func locateJackettConfig(path string) (string, string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return "", "", errors.New("jackett: empty config path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("jackett: config path %q: %w", path, err)
	}
	if info.IsDir() {
		candidates := []string{
			filepath.Join(path, "ServerConfig.json"),
			filepath.Join(path, "Jackett", "ServerConfig.json"),
		}
		for _, candidate := range candidates {
			configInfo, statErr := os.Stat(candidate)
			if statErr == nil && configInfo.Mode().IsRegular() {
				return candidate, filepath.Join(filepath.Dir(candidate), "Indexers"), nil
			}
		}
		return "", "", fmt.Errorf("jackett: no ServerConfig.json found under %q", path)
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("jackett: config path %q is not a regular file", path)
	}
	return path, filepath.Join(filepath.Dir(path), "Indexers"), nil
}

func readJSONObject(path string) (map[string]any, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > jackettFileLimit {
		return nil, fmt.Errorf("file is larger than %d bytes", jackettFileLimit)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("expected a JSON object")
	}
	return object, nil
}

// jackettHost turns the configured bind address into a loopback address for
// the local Torznab endpoint. Wildcard binds are not reachable as-is.
func jackettHost(bind string) string {
	bind = strings.TrimSpace(bind)
	if bind == "" || bind == "*" || strings.EqualFold(bind, "localhost") {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(strings.Trim(bind, "[]")); ip != nil {
		if ip.IsUnspecified() {
			return "127.0.0.1"
		}
		return ip.String()
	}
	return "127.0.0.1"
}

// readJackettIndexer parses one Indexers/<id>.json. It returns either an
// indexer or a warning, never an error: one bad file must not abort the
// import.
func readJackettIndexer(path, base, apiKey string) (Indexer, string, string) {
	object, err := readJSONObject(path)
	fromFields := false
	if err != nil {
		object, err = readJackettFieldObject(path)
		if err != nil {
			return Indexer{}, "", fmt.Sprintf("jackett: indexer file %q: %s", filepath.Base(path), err.Error())
		}
		fromFields = true
	}

	id := strings.TrimSpace(lookupString(object, "id", "Id", "ID"))
	if fromFields {
		// A real Jackett indexer file is an array of settings whose first
		// "id" is "sitelink", not the indexer id. The file name is the id.
		id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		object["id"] = id
	}
	if id == "" {
		return Indexer{}, "", fmt.Sprintf("jackett: indexer file %q has no id, skipped", filepath.Base(path))
	}
	if !safeJackettID(id) {
		return Indexer{}, "", fmt.Sprintf("jackett: indexer file %q has an unsafe id %q, skipped",
			filepath.Base(path), id)
	}

	name := strings.TrimSpace(lookupString(object, "name", "Name"))
	if name == "" {
		name = id
	}
	siteURL := lookupString(object, "site_link", "siteLink", "SiteLink", "sitelink", "url", "Url", "URL")
	if siteURL == "" {
		return Indexer{}, "", fmt.Sprintf("jackett: indexer %q has no URL, skipped", name)
	}

	protocol := ProtocolTorrent
	if explicit := lookupString(object, "protocol", "Protocol"); explicit != "" {
		protocol = normalizeProtocol(explicit, "")
	}
	if usenet, ok := lookupBool(object, "usenetEnabled", "UsenetEnabled"); ok && usenet {
		protocol = ProtocolUsenet
	}

	enabled := true
	if value, ok := lookupBool(object, "enabled", "Enabled"); ok {
		enabled = value
	}

	fields := map[string]string{"site_link": siteURL}
	if indexerType := lookupString(object, "type", "Type"); indexerType != "" {
		fields["type"] = indexerType
	}

	categories := parseCategories(lookupValue(object, "categories", "Categories"))
	if len(categories) == 0 {
		// Real Jackett indexer files expose the selected categories as "tags".
		categories = parseCategories(lookupValue(object, "tags", "Tags"))
	}

	return Indexer{
		Name:           name,
		Implementation: "Cardigann",
		Protocol:       protocol,
		DefinitionHint: slugify(id),
		BaseURL:        base + "/api/v2.0/indexers/" + url.PathEscape(id) + "/results/torznab/",
		APIKey:         apiKey,
		Categories:     categories,
		Tags:           parseTags(lookupValue(object, "tags", "Tags")),
		Priority:       25,
		Enabled:        enabled,
		Fields:         fields,
	}, id, ""
}

// safeJackettID keeps the derived Torznab URL inside its path segment. Real
// Jackett ids are simple slugs; anything with separators, traversal or control
// characters is rejected.
func safeJackettID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// pathWithin reports whether path is inside dir. It is belt and braces on top
// of os.ReadDir, which cannot return names containing separators.
func pathWithin(dir, path string) bool {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

func jackettProxy(config map[string]any) (*Proxy, string) {
	proxyType := strings.TrimSpace(lookupString(config, "ProxyType", "proxyType"))
	switch strings.ToLower(proxyType) {
	case "", "disabled", "none", "-1":
		return nil, ""
	}
	kind := strings.ToLower(proxyType)
	host := lookupString(config, "ProxyUrl", "proxyUrl", "ProxyHost", "proxyHost")
	port := lookupString(config, "ProxyPort", "proxyPort")
	if host == "" {
		return nil, fmt.Sprintf("jackett: proxy %q is enabled but has no URL, skipped", proxyType)
	}
	proxyURL := normalizeProxyURL(host, port, kind)
	username := lookupString(config, "ProxyUsername", "proxyUsername")
	password := lookupString(config, "ProxyPassword", "proxyPassword")
	proxyURL, username, password = splitProxyCredentials(proxyURL, username, password)
	return &Proxy{
		Kind:     kind,
		URL:      proxyURL,
		Username: username,
		Password: password,
	}, ""
}

// lookupValue is lookupString without the string conversion, for lists.
func lookupValue(object map[string]any, names ...string) any {
	value, _ := lookup(object, names...)
	return value
}

// normalizeProxyURL turns a host/port pair into an URL with the scheme implied
// by the proxy kind. A value that already has a scheme is kept as-is.
func normalizeProxyURL(host, port, kind string) string {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" {
		return ""
	}
	if strings.Contains(host, "://") {
		if port == "" {
			return host
		}
		parsed, err := url.Parse(host)
		if err != nil || parsed.Port() != "" {
			return host
		}
		parsed.Host = joinHostPort(parsed.Hostname(), port)
		return parsed.String()
	}
	if port != "" {
		host = joinHostPort(host, port)
	}
	scheme := "http"
	switch kind {
	case "socks4":
		scheme = "socks4"
	case "socks5":
		scheme = "socks5"
	}
	return scheme + "://" + host
}

func joinHostPort(host, port string) string {
	host = strings.Trim(host, "[]")
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// splitProxyCredentials moves basic-auth credentials embedded in a proxy URL
// into the dedicated fields, so the URL itself is safe to store and show.
func splitProxyCredentials(rawURL, username, password string) (string, string, string) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User == nil {
		return rawURL, username, password
	}
	if username == "" {
		username = parsed.User.Username()
	}
	if password == "" {
		password, _ = parsed.User.Password()
	}
	parsed.User = nil
	return parsed.String(), username, password
}

// jackettField is one element of a real Jackett Indexers/<id>.json file.
// Jackett stores an array of settings, not an object: {"id","name","type","value"}.
type jackettField struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// readJackettFieldObject converts the array form used by a real Jackett data
// directory into the map form the rest of the importer understands. It is
// deliberately separate from readJSONObject, which is used for
// ServerConfig.json (an object).
func readJackettFieldObject(path string) (map[string]any, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > jackettFileLimit {
		return nil, fmt.Errorf("file is larger than %d bytes", jackettFileLimit)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fields []jackettField
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, errors.New("expected a JSON object or an array of Jackett settings")
	}
	object := make(map[string]any, len(fields))
	for _, field := range fields {
		id := strings.TrimSpace(field.ID)
		if id == "" {
			continue
		}
		if len(field.Value) == 0 || string(field.Value) == "null" {
			object[id] = ""
			continue
		}
		var value any
		if err := json.Unmarshal(field.Value, &value); err != nil {
			object[id] = string(field.Value)
			continue
		}
		object[id] = value
	}
	if len(object) == 0 {
		return nil, errors.New("Jackett settings array has no usable entries")
	}
	return object, nil
}
