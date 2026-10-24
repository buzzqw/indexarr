package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

// fixedSettingNames are rendered as explicit fields of the indexer form, so a
// definition setting with the same name is not repeated.
var fixedSettingNames = map[string]bool{
	"url":                  true,
	"categories":           true,
	"query":                true,
	"user_agent":           true,
	"allow_private":        true,
	"default_category":     true,
	"challenge":            true,
	"flaresolverr_url":     true,
	"flaresolverr_timeout": true,
}

// ---------------------------------------------------------------- definitions

type definitionRow struct {
	ID         string
	Name       string
	Family     string
	Protocol   string
	State      string
	Origin     string
	Hash       string
	HashShort  string
	UpdatedAt  time.Time
	Indexers   int
	ParseError string
	Builtin    bool
	Derived    bool
}

type definitionsPage struct {
	base
	CatalogRows []definitionRow
	LocalRows   []definitionRow
}

func (s *Server) definitionsPage(w http.ResponseWriter, r *http.Request) {
	data := definitionsPage{base: s.newBaseT(r, "definitions.title", "definitions")}
	data.CatalogRows, data.LocalRows = s.definitionRows(r.Context(), &data.base)
	s.render(w, r, "definitions", data, http.StatusOK)
}

// definitionRows builds the catalog/local tables used by the old Definitions
// page and by Settings → Advanced. The base is optional and only used to log a
// catalog parse warning.
func (s *Server) definitionRows(ctx context.Context, data *base) ([]definitionRow, []definitionRow) {
	defs, err := s.store.ListDefinitions(ctx)
	if err != nil {
		return nil, nil
	}
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		return nil, nil
	}
	counts := make(map[string]int, len(indexers))
	for _, in := range indexers {
		counts[in.DefinitionID]++
	}
	var catalogRows, localRows []definitionRow
	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		seen[def.ID] = true
		row := definitionRowFromStore(def, counts[def.ID])
		if def.Origin == catalog.OriginBuiltin {
			row.Builtin = true
			catalogRows = append(catalogRows, row)
			continue
		}
		localRows = append(localRows, row)
	}

	entries, catalogErr := catalog.Builtins()
	if catalogErr != nil && data != nil {
		s.log.Warn("load catalog for definitions page", "error", catalogErr)
	}
	for _, entry := range entries {
		if seen[entry.ID] {
			continue
		}
		row := definitionRow{
			ID:       entry.ID,
			Name:     entry.Definition.Meta.Name,
			Family:   entry.Definition.Connector,
			Protocol: entry.Definition.Meta.Protocol,
			State:    entry.State,
			Origin:   catalog.OriginBuiltin,
			Hash:     entry.Hash,
			Builtin:  true,
		}
		row.HashShort = shortHash(row.Hash)
		catalogRows = append(catalogRows, row)
	}
	return catalogRows, localRows
}

func definitionRowFromStore(def store.Definition, indexers int) definitionRow {
	row := definitionRow{
		ID:        def.ID,
		Name:      def.ID,
		Family:    def.Family,
		State:     def.State,
		Origin:    def.Origin,
		Hash:      def.Hash,
		HashShort: shortHash(def.Hash),
		UpdatedAt: def.UpdatedAt,
		Indexers:  indexers,
	}
	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil {
		row.ParseError = err.Error()
		return row
	}
	row.Name = parsed.Meta.Name
	row.Protocol = parsed.Meta.Protocol
	row.Derived = parsed.IsDerived()
	if connector := strings.TrimSpace(parsed.Connector); connector != "" {
		row.Family = connector
	}
	return row
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12] + "…"
	}
	return hash
}

type definitionPage struct {
	base
	Definition store.Definition
	Name       string
	Family     string
	Protocol   string
	HashShort  string
	Problems   []string
	Body       string
	Indexers   []store.Indexer
	Builtin    bool
	Derived    bool
}

func (s *Server) definitionPage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		s.renderError(w, r, http.StatusNotFound, s.translate(r, "definition.error.notFound"))
		return
	}
	def, builtin, err := s.storedOrCatalogDefinition(r, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, indexersvc.ErrDefinitionNotFound) {
			s.renderError(w, r, http.StatusNotFound, s.translate(r, "definition.error.notFound"))
			return
		}
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	indexers, err := s.store.ListIndexers(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	used := make([]store.Indexer, 0)
	for _, in := range indexers {
		if in.DefinitionID == id {
			used = append(used, in)
		}
	}

	data := definitionPage{
		base:       s.newBase(r, id, "definitions"),
		Definition: def,
		Name:       id,
		Family:     def.Family,
		HashShort:  shortHash(def.Hash),
		Body:       def.Body,
		Indexers:   used,
		Builtin:    builtin,
	}
	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil {
		data.Problems = validationProblems(err, s.translate(r, "definitionForm.invalidShort"))
	} else {
		data.Name = parsed.Meta.Name
		data.Protocol = parsed.Meta.Protocol
		data.Derived = parsed.IsDerived()
		data.Title = data.Name
		if connector := strings.TrimSpace(parsed.Connector); connector != "" {
			data.Family = connector
		}
	}
	s.render(w, r, "definition", data, http.StatusOK)
}

// storedOrCatalogDefinition loads a stored definition, falling back to the
// embedded catalog so a built-in entry has a detail page before installation.
func (s *Server) storedOrCatalogDefinition(r *http.Request, id string) (store.Definition, bool, error) {
	def, err := s.store.GetDefinition(r.Context(), id)
	if err == nil {
		return def, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.Definition{}, false, err
	}
	entry, ok := catalog.Get(id)
	if !ok {
		return store.Definition{}, false, sql.ErrNoRows
	}
	return store.Definition{
		ID:            entry.ID,
		SchemaVersion: entry.Definition.Schema,
		Family:        entry.Definition.Connector,
		State:         entry.State,
		Origin:        catalog.OriginBuiltin,
		Author:        entry.Author,
		Body:          string(entry.Body),
		Hash:          entry.Hash,
	}, true, nil
}

type definitionFormPage struct {
	base
	Body     string
	Problems []string
}

func (s *Server) definitionNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "definition_form", definitionFormPage{
		base: s.newBaseT(r, "definitionForm.title", "definitions"),
		Body: "schema: 1\nmeta:\n  id: my-indexer\n  name: My Indexer\n  type: private\n  protocol: torrent\n  urls:\n    - https://indexer.example/\n",
	}, http.StatusOK)
}

func (s *Server) definitionCreate(w http.ResponseWriter, r *http.Request) {
	body, err := s.definitionBodyFromRequest(r)
	if err != nil {
		s.renderDefinitionFormError(w, r, string(body), []string{err.Error()})
		return
	}
	parsed, err := definition.Parse(body)
	if err != nil {
		s.renderDefinitionFormError(w, r, string(body), validationProblems(err, s.translate(r, "definitionForm.invalidShort")))
		return
	}

	overwrite := r.FormValue("overwrite") != ""
	if _, err := s.store.GetDefinition(r.Context(), parsed.Meta.ID); err == nil {
		if !overwrite {
			s.renderDefinitionFormError(w, r, string(body), []string{
				s.translate(r, "definitionForm.duplicate", parsed.Meta.ID),
			})
			return
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.renderDefinitionFormError(w, r, string(body), []string{err.Error()})
		return
	}

	stored := store.Definition{
		ID:            parsed.Meta.ID,
		SchemaVersion: parsed.Schema,
		Family:        parsed.Connector,
		State:         "bozza",
		Origin:        "local",
		Author:        "ui",
		Body:          string(body),
		Hash:          definition.Fingerprint(body),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := s.store.UpsertDefinition(r.Context(), stored); err != nil {
		s.renderDefinitionFormError(w, r, string(body), []string{err.Error()})
		return
	}
	redirect(w, r, "/definitions/"+stored.ID, s.translate(r, "definition.saved"), "")
}

func (s *Server) renderDefinitionFormError(w http.ResponseWriter, r *http.Request, body string, problems []string) {
	data := definitionFormPage{
		base:     s.newBaseT(r, "definitionForm.title", "definitions"),
		Body:     body,
		Problems: problems,
	}
	data.Error = s.translate(r, "definitionForm.invalid")
	s.render(w, r, "definition_form", data, http.StatusBadRequest)
}

// definitionBodyFromRequest accepts either the textarea or an uploaded YAML
// file. Multipart forms carry the CSRF token in the query string.
func (s *Server) definitionBodyFromRequest(r *http.Request) ([]byte, error) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			return nil, err
		}
		file, _, err := r.FormFile("file")
		switch {
		case err == nil:
			defer file.Close()
			data, err := io.ReadAll(io.LimitReader(file, 4<<20))
			if err != nil {
				return nil, err
			}
			if len(bytes.TrimSpace(data)) > 0 {
				return data, nil
			}
		case !errors.Is(err, http.ErrMissingFile):
			return nil, err
		}
	}
	body := []byte(r.FormValue("body"))
	if len(bytes.TrimSpace(body)) == 0 {
		return body, errors.New(s.translate(r, "definitionForm.empty"))
	}
	return body, nil
}

func validationProblems(err error, fallback string) []string {
	var invalid *definition.ValidationError
	if errors.As(err, &invalid) {
		return append([]string(nil), invalid.Problems...)
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = fallback
	}
	return []string{message}
}

// ------------------------------------------------------------------- helpers

func definitionName(def store.Definition) string {
	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil || strings.TrimSpace(parsed.Meta.Name) == "" {
		return def.ID
	}
	return parsed.Meta.Name
}

type indexerConfigEntry struct {
	Key   string
	Value string
}

func statusForIndexerError(err error) int {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, indexersvc.ErrIndexerNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func (s *Server) indexerErrorMessage(r *http.Request, err error) string {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, indexersvc.ErrIndexerNotFound) {
		return s.translate(r, "indexer.error.notFound")
	}
	return err.Error()
}

// publicConfig decodes config_json and drops any key that belongs to the vault,
// as a second line of defence against a polluted legacy row.
func publicConfig(raw string) []indexerConfigEntry {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil || values == nil {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if indexersvc.IsSecretKey(key) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]indexerConfigEntry, 0, len(keys))
	for _, key := range keys {
		out = append(out, indexerConfigEntry{Key: key, Value: configText(values[key])})
	}
	return out
}

func configText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

// ---------------------------------------------------------------- form pages

type indexerSettingOption struct {
	Value    string
	Label    string
	Selected bool
}

type indexerSettingField struct {
	Name        string
	Label       string
	Help        string
	Type        string
	Value       string
	Placeholder string
	Required    bool
	Options     []indexerSettingOption
	Checked     bool
	Secret      bool
	HasSecret   bool
}

func (s *Server) indexerFriendlyError(r *http.Request, err error) string {
	switch {
	case errors.Is(err, indexersvc.ErrSlugTaken):
		return s.translate(r, "indexer.error.slugTaken")
	case errors.Is(err, indexersvc.ErrVaultRequired):
		return s.translate(r, "indexer.error.vaultRequired")
	case errors.Is(err, indexersvc.ErrDefinitionNotFound):
		return s.translate(r, "indexer.error.definitionNotFound")
	case errors.Is(err, indexersvc.ErrIndexerNotFound):
		return s.translate(r, "indexer.error.notFound")
	default:
		return err.Error()
	}
}

func settingFieldType(setting definition.Setting) string {
	switch strings.ToLower(strings.TrimSpace(setting.Type)) {
	case "password":
		return "password"
	case "checkbox", "bool", "boolean":
		return "checkbox"
	case "select":
		return "select"
	default:
		if len(setting.Options) > 0 {
			return "select"
		}
		return "text"
	}
}

func truthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func publicConfigMap(raw string) map[string]string {
	out := map[string]string{}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil || values == nil {
		return out
	}
	for key, value := range values {
		if indexersvc.IsSecretKey(key) {
			continue
		}
		out[key] = configText(value)
	}
	return out
}

// loadDefinitionForForm reads a stored definition or a built-in catalog entry.
func (s *Server) loadDefinitionForForm(r *http.Request, id string) (*definition.Definition, bool, error) {
	if id == "" {
		return nil, false, errors.New(s.translate(r, "indexerForm.chooseDefinition"))
	}
	if def, err := s.store.GetDefinition(r.Context(), id); err == nil {
		parsed, err := definition.Parse([]byte(def.Body))
		if err != nil {
			return nil, true, err
		}
		return parsed, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	entry, ok := catalog.Get(id)
	if ok {
		return entry.Definition, false, nil
	}
	if parsed, body, err := catalogSiteDefinition(id); err == nil {
		_ = body
		return parsed, false, nil
	}
	return nil, false, indexersvc.ErrDefinitionNotFound
}

func catalogSiteDefinition(id string) (*definition.Definition, []byte, error) {
	sites, err := catalog.Sites()
	if err != nil {
		return nil, nil, err
	}
	for _, site := range sites {
		if site.ID != id {
			continue
		}
		body, err := catalog.DefinitionForSite(site)
		if err != nil {
			return nil, nil, err
		}
		parsed, err := definition.Parse(body)
		if err != nil {
			return nil, nil, err
		}
		return parsed, body, nil
	}
	return nil, nil, indexersvc.ErrDefinitionNotFound
}

// ensureStoredDefinition installs a built-in definition the first time an
// indexer references it, because indexers have a foreign key to definitions.
func (s *Server) ensureStoredDefinition(r *http.Request, id string) error {
	if _, err := s.store.GetDefinition(r.Context(), id); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	entry, ok := catalog.Get(id)
	if ok {
		return s.store.UpsertDefinition(r.Context(), store.Definition{
			ID:            entry.ID,
			SchemaVersion: entry.Definition.Schema,
			Family:        entry.Definition.Connector,
			State:         entry.State,
			Origin:        catalog.OriginBuiltin,
			Author:        entry.Author,
			Body:          string(entry.Body),
			Hash:          entry.Hash,
			UpdatedAt:     time.Now().UTC(),
		})
	}
	parsed, body, err := catalogSiteDefinition(id)
	if err != nil {
		return indexersvc.ErrDefinitionNotFound
	}
	return s.store.UpsertDefinition(r.Context(), store.Definition{
		ID:            parsed.Meta.ID,
		SchemaVersion: parsed.Schema,
		Family:        parsed.Connector,
		State:         "bozza",
		Origin:        "catalog",
		Author:        "indexarr",
		Body:          string(body),
		Hash:          definition.Fingerprint(body),
		UpdatedAt:     time.Now().UTC(),
	})
}

// indexerInputFromForm parses the generated fields. Empty password fields are
// omitted (keep the stored secret); the clear checkbox sends an empty value,
// which the service interprets as a deletion.
func (s *Server) indexerInputFromForm(r *http.Request, parsed *definition.Definition) (indexersvc.Input, error) {
	priority := 25
	if raw := strings.TrimSpace(r.FormValue("priority")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return indexersvc.Input{}, errors.New(s.translate(r, "indexerForm.error.priorityNumber"))
		}
		priority = value
	}
	enabled := r.FormValue("enabled") != ""
	categories := strings.TrimSpace(r.FormValue("categories"))
	if values := r.Form["categories"]; len(values) > 0 {
		joined := make([]string, 0, len(values))
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				joined = append(joined, value)
			}
		}
		categories = strings.Join(joined, ",")
	}
	challenge := strings.ToLower(strings.TrimSpace(r.FormValue("challenge")))
	switch challenge {
	case "", "none", "flaresolverr":
	default:
		return indexersvc.Input{}, errors.New(s.translate(r, "indexerForm.error.challenge"))
	}
	flareURL := strings.TrimSpace(r.FormValue("flaresolverr_url"))
	if flareURL != "" {
		normalized, ok := normalizeHTTPURL(flareURL)
		if !ok {
			return indexersvc.Input{}, errors.New(s.translate(r, "indexerForm.error.flareSolverrURL"))
		}
		flareURL = normalized
	}
	flareTimeout := strings.TrimSpace(r.FormValue("flaresolverr_timeout"))
	if flareTimeout != "" {
		value, err := strconv.Atoi(flareTimeout)
		if err != nil || value < 5 || value > 300 {
			return indexersvc.Input{}, errors.New(s.translate(r, "indexerForm.error.flareSolverrTimeout"))
		}
	}
	settings := map[string]string{
		"categories":           categories,
		"store_latest":         strconv.FormatBool(r.FormValue("store_latest") != ""),
		"query":                strings.TrimSpace(r.FormValue("query")),
		"user_agent":           strings.TrimSpace(r.FormValue("user_agent")),
		"default_category":     strings.TrimSpace(r.FormValue("default_category")),
		"allow_private":        "false",
		"challenge":            challenge,
		"flaresolverr_url":     flareURL,
		"flaresolverr_timeout": flareTimeout,
	}
	// An empty URL means "keep the stored value" when the URL is a vault
	// secret; a new value replaces it. On create the field is required, so an
	// empty value is still rejected by the service.
	if rawURL := strings.TrimSpace(r.FormValue("url")); rawURL != "" {
		normalized, ok := normalizeHTTPURL(rawURL)
		if !ok {
			return indexersvc.Input{}, errors.New(s.translate(r, "indexerForm.error.url"))
		}
		settings["url"] = normalized
	}
	if r.FormValue("allow_private") != "" {
		settings["allow_private"] = "true"
	}
	for _, setting := range parsed.Settings {
		name := setting.Name
		if fixedSettingNames[name] {
			continue
		}
		if indexersvc.IsSecretKey(name) {
			if r.FormValue("clear_"+name) != "" {
				settings[name] = ""
				continue
			}
			if value := strings.TrimSpace(r.FormValue("setting_" + name)); value != "" {
				settings[name] = value
			}
			continue
		}
		switch settingFieldType(setting) {
		case "checkbox":
			settings[name] = "false"
			if r.FormValue("setting_"+name) != "" {
				settings[name] = "true"
			}
		default:
			settings[name] = strings.TrimSpace(r.FormValue("setting_" + name))
		}
	}
	var interval *int
	if values, present := r.Form["interval_seconds"]; present {
		seconds := 0
		if raw := strings.TrimSpace(firstValue(values)); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				return indexersvc.Input{}, errors.New(s.translate(r, "indexerForm.error.intervalNumber"))
			}
			if value < 0 || (value > 0 && value < 60) {
				return indexersvc.Input{}, errors.New(s.translate(r, "indexerForm.error.intervalMin"))
			}
			seconds = value
		}
		interval = &seconds
	}
	return indexersvc.Input{
		DefinitionID:    strings.TrimSpace(r.FormValue("definition_id")),
		Name:            strings.TrimSpace(r.FormValue("name")),
		Slug:            strings.TrimSpace(r.FormValue("slug")),
		Protocol:        strings.TrimSpace(r.FormValue("protocol")),
		Priority:        &priority,
		Tags:            splitTagList(r.FormValue("tags")),
		Enabled:         &enabled,
		IntervalSeconds: interval,
		Settings:        settings,
	}, nil
}

func firstValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// addExtraSecretValues handles vault keys that are not declared by the current
// definition anymore. The edit form still shows them, so they can be replaced
// or removed instead of becoming invisible dead rows.
func (s *Server) addExtraSecretValues(r *http.Request, id int64, parsed *definition.Definition, in *indexersvc.Input) error {
	declared := make(map[string]bool, len(parsed.Settings))
	for _, setting := range parsed.Settings {
		declared[setting.Name] = true
	}
	secrets, err := s.store.ListSecrets(r.Context(), id)
	if err != nil {
		return err
	}
	for _, secret := range secrets {
		if declared[secret.Key] {
			continue
		}
		if r.FormValue("clear_"+secret.Key) != "" {
			in.Settings[secret.Key] = ""
			continue
		}
		if value := strings.TrimSpace(r.FormValue("setting_" + secret.Key)); value != "" {
			in.Settings[secret.Key] = value
		}
	}
	return nil
}

func splitTagList(raw string) []string {
	out := []string{}
	for _, tag := range strings.Split(raw, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}
