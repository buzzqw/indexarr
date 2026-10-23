package web

import (
	"bytes"
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
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

// fixedSettingNames are rendered as explicit fields of the indexer form, so a
// definition setting with the same name is not repeated.
var fixedSettingNames = map[string]bool{
	"url":              true,
	"categories":       true,
	"query":            true,
	"user_agent":       true,
	"allow_private":    true,
	"default_category": true,
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
	defs, err := s.store.ListDefinitions(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	indexers, err := s.store.ListIndexers(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	counts := make(map[string]int, len(indexers))
	for _, in := range indexers {
		counts[in.DefinitionID]++
	}

	data := definitionsPage{base: s.newBaseT(r, "definitions.title", "definitions")}
	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		seen[def.ID] = true
		row := definitionRowFromStore(def, counts[def.ID])
		if def.Origin == catalog.OriginBuiltin {
			row.Builtin = true
			data.CatalogRows = append(data.CatalogRows, row)
			continue
		}
		data.LocalRows = append(data.LocalRows, row)
	}

	entries, catalogErr := catalog.Builtins()
	if catalogErr != nil {
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
		data.CatalogRows = append(data.CatalogRows, row)
	}

	s.render(w, r, "definitions", data, http.StatusOK)
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

// ------------------------------------------------------------------- indexers

type indexerRow struct {
	Indexer       store.Indexer
	Definition    string
	HasSecrets    bool
	LastError     string
	LastRun       *store.IndexerHistory
	Tags          string
	SecretCount   int
	DefinitionRef string
}

type indexersPage struct {
	base
	Rows        []indexerRow
	Unavailable bool
	Count       int
}

func (s *Server) indexersPage(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("tab")), "legacy") {
		// The old legacy tab is a bookmark from the pre-WP16 UI.
		http.Redirect(w, r, "/indexers", http.StatusFound)
		return
	}
	data := indexersPage{
		base: s.newBaseT(r, "indexers.title", "indexers"),
	}
	if s.svc != nil {
		indexers, err := s.svc.Indexers(r.Context())
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		defs, _ := s.store.ListDefinitions(r.Context())
		names := make(map[string]string, len(defs))
		for _, def := range defs {
			names[def.ID] = definitionName(def)
		}

		for _, in := range indexers {
			row := indexerRow{
				Indexer:    in,
				Definition: names[in.DefinitionID],
				Tags:       in.Tags,
			}
			if row.Definition == "" {
				row.Definition = in.DefinitionID
			}
			keys, err := s.svc.SecretKeys(r.Context(), in.ID)
			if err == nil {
				row.SecretCount = len(keys)
				row.HasSecrets = len(keys) > 0
			}
			if state, err := s.store.GetIndexerState(r.Context(), in.ID); err == nil {
				row.LastError = state.LastError
			}
			if history, err := s.store.ListIndexerHistory(r.Context(), in.ID, 1); err == nil && len(history) > 0 {
				row.LastRun = &history[0]
			}
			data.Rows = append(data.Rows, row)
		}
		data.Count = len(data.Rows)
	} else {
		data.Unavailable = true
	}
	s.render(w, r, "indexers", data, http.StatusOK)
}

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

type indexerPage struct {
	base
	Indexer          store.Indexer
	Definition       store.Definition
	DefinitionName   string
	Public           []indexerConfigEntry
	SecretKeys       []string
	State            store.IndexerState
	HasState         bool
	LastRun          *store.IndexerHistory
	Test             *engine.TestResult
	Run              *indexer.RunStats
	RunItems         []engine.Item
	ApplicationCount int
}

func (s *Server) indexerPage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "indexer.error.invalidID"))
		return
	}
	data, err := s.buildIndexerPage(r, id)
	if err != nil {
		s.renderError(w, r, statusForIndexerError(err), s.indexerErrorMessage(r, err))
		return
	}
	s.render(w, r, "indexer", data, http.StatusOK)
}

func (s *Server) buildIndexerPage(r *http.Request, id int64) (indexerPage, error) {
	in, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		return indexerPage{}, err
	}
	data := indexerPage{
		base:       s.newBase(r, in.Name, "indexers"),
		Indexer:    in,
		Definition: store.Definition{ID: in.DefinitionID},
		Public:     publicConfig(in.ConfigJSON),
	}
	if applications, err := s.store.ListApplications(r.Context()); err == nil {
		data.ApplicationCount = len(applications)
	}
	if def, err := s.store.GetDefinition(r.Context(), in.DefinitionID); err == nil {
		data.Definition = def
		data.DefinitionName = definitionName(def)
	} else {
		data.DefinitionName = in.DefinitionID
	}
	secrets, err := s.store.ListSecrets(r.Context(), in.ID)
	if err == nil {
		for _, secret := range secrets {
			data.SecretKeys = append(data.SecretKeys, secret.Key)
		}
	}
	if state, err := s.store.GetIndexerState(r.Context(), in.ID); err == nil {
		data.State = state
		data.HasState = true
	}
	if history, err := s.store.ListIndexerHistory(r.Context(), in.ID, 1); err == nil && len(history) > 0 {
		data.LastRun = &history[0]
	}
	return data, nil
}

func (s *Server) indexerTest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers?tab=native", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	if _, err := s.store.GetIndexer(r.Context(), id); err != nil {
		redirect(w, r, "/indexers?tab=native", "", s.translate(r, "indexer.error.notFound"))
		return
	}
	if s.native == nil {
		data, buildErr := s.buildIndexerPage(r, id)
		if buildErr != nil {
			redirect(w, r, "/indexers?tab=native", "", s.indexerErrorMessage(r, buildErr))
			return
		}
		data.Error = s.translate(r, "indexer.error.nativeEngineTest")
		s.render(w, r, "indexer", data, http.StatusOK)
		return
	}
	result, testErr := s.native.Test(r.Context(), id)
	data, buildErr := s.buildIndexerPage(r, id)
	if buildErr != nil {
		redirect(w, r, "/indexers?tab=native", "", s.indexerErrorMessage(r, buildErr))
		return
	}
	data.Test = &result
	if testErr != nil {
		data.Error = s.translate(r, "indexer.testFailedDetail", testErr.Error())
	}
	s.render(w, r, "indexer", data, http.StatusOK)
}

func (s *Server) indexerRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers?tab=native", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	if _, err := s.store.GetIndexer(r.Context(), id); err != nil {
		redirect(w, r, "/indexers?tab=native", "", s.translate(r, "indexer.error.notFound"))
		return
	}
	if s.native == nil {
		data, buildErr := s.buildIndexerPage(r, id)
		if buildErr != nil {
			redirect(w, r, "/indexers?tab=native", "", s.indexerErrorMessage(r, buildErr))
			return
		}
		data.Error = s.translate(r, "indexer.error.nativeEngineRun")
		s.render(w, r, "indexer", data, http.StatusOK)
		return
	}
	stats, runErr := s.native.Run(r.Context(), id)
	data, buildErr := s.buildIndexerPage(r, id)
	if buildErr != nil {
		redirect(w, r, "/indexers?tab=native", "", s.indexerErrorMessage(r, buildErr))
		return
	}
	if stats != nil {
		data.Run = stats
		if len(stats.Items) > 20 {
			data.RunItems = stats.Items[:20]
		} else {
			data.RunItems = stats.Items
		}
	}
	if runErr != nil {
		data.Error = s.translate(r, "indexer.runFailed", runErr.Error())
	}
	s.render(w, r, "indexer", data, http.StatusOK)
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

type definitionOption struct {
	ID        string
	Name      string
	Protocol  string
	Family    string
	Installed bool
	Derived   bool
}

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

type indexerFormPage struct {
	base
	IsNew           bool
	Indexer         store.Indexer
	DefinitionID    string
	DefinitionName  string
	Definitions     []definitionOption
	Derived         bool
	Name            string
	Slug            string
	Protocol        string
	Priority        int
	Tags            string
	Enabled         bool
	URL             string
	Categories      string
	Query           string
	UserAgent       string
	DefaultCategory string
	AllowPrivate    bool
	Fields          []indexerSettingField
	Problems        []string
}

func (s *Server) indexerNew(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	defID := strings.TrimSpace(r.URL.Query().Get("definition"))
	data, err := s.buildIndexerForm(r, defID, nil, nil, nil)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.render(w, r, "indexer_form", data, http.StatusOK)
}

func (s *Server) indexerEdit(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "indexer.error.invalidID"))
		return
	}
	current, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		s.renderError(w, r, statusForIndexerError(err), s.indexerErrorMessage(r, err))
		return
	}
	data, err := s.buildIndexerForm(r, current.DefinitionID, &current, nil, nil)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.render(w, r, "indexer_form", data, http.StatusOK)
}

func (s *Server) indexerCreate(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	defID := strings.TrimSpace(r.FormValue("definition_id"))
	parsed, _, err := s.loadDefinitionForForm(r, defID)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "indexerForm.invalidDefinition", err.Error()))
		return
	}
	in, err := indexerInputFromForm(r, parsed)
	if err != nil {
		s.renderIndexerFormError(w, r, defID, nil, nil, []string{err.Error()})
		return
	}
	if err := s.ensureStoredDefinition(r, defID); err != nil {
		s.renderIndexerFormError(w, r, defID, nil, &in, []string{err.Error()})
		return
	}
	created, err := s.svc.Create(r.Context(), in)
	if err != nil {
		s.renderIndexerFormError(w, r, defID, nil, &in, []string{s.indexerFriendlyError(r, err)})
		return
	}
	redirect(w, r, "/indexers/"+strconv.FormatInt(created.ID, 10), s.translate(r, "indexer.created"), "")
}

func (s *Server) indexerUpdate(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers?tab=native", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	current, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		redirect(w, r, "/indexers?tab=native", "", s.indexerErrorMessage(r, err))
		return
	}
	defID := strings.TrimSpace(r.FormValue("definition_id"))
	if defID == "" {
		defID = current.DefinitionID
	}
	parsed, _, err := s.loadDefinitionForForm(r, defID)
	if err != nil {
		s.renderIndexerFormError(w, r, defID, &current, nil, []string{s.translate(r, "indexerForm.definitionInvalid", err.Error())})
		return
	}
	in, err := indexerInputFromForm(r, parsed)
	if err != nil {
		s.renderIndexerFormError(w, r, defID, &current, nil, []string{err.Error()})
		return
	}
	if err := s.addExtraSecretValues(r, current.ID, parsed, &in); err != nil {
		s.renderIndexerFormError(w, r, defID, &current, &in, []string{err.Error()})
		return
	}
	if err := s.ensureStoredDefinition(r, defID); err != nil {
		s.renderIndexerFormError(w, r, defID, &current, &in, []string{err.Error()})
		return
	}
	if _, err := s.svc.Update(r.Context(), id, in); err != nil {
		s.renderIndexerFormError(w, r, defID, &current, &in, []string{s.indexerFriendlyError(r, err)})
		return
	}
	redirect(w, r, "/indexers/"+strconv.FormatInt(id, 10), s.translate(r, "indexer.saved"), "")
}

func (s *Server) indexerDelete(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers?tab=native", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	if err := s.svc.Delete(r.Context(), id); err != nil {
		redirect(w, r, "/indexers?tab=native", "", s.indexerFriendlyError(r, err))
		return
	}
	redirect(w, r, "/indexers?tab=native", s.translate(r, "indexer.deleted"), "")
}

func (s *Server) renderIndexerFormError(w http.ResponseWriter, r *http.Request, defID string, current *store.Indexer, in *indexersvc.Input, problems []string) {
	data, err := s.buildIndexerForm(r, defID, current, in, problems)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	data.Error = s.translate(r, "indexerForm.checkFields")
	s.render(w, r, "indexer_form", data, http.StatusBadRequest)
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

// buildIndexerForm prepares the form from a definition, an existing indexer
// and an optional submitted input. Secret values are never copied into the
// page: only the names and whether a value is configured.
func (s *Server) buildIndexerForm(r *http.Request, defID string, current *store.Indexer, in *indexersvc.Input, problems []string) (indexerFormPage, error) {
	options, err := s.definitionOptions(r)
	if err != nil {
		return indexerFormPage{}, err
	}
	data := indexerFormPage{
		base:        s.newBaseT(r, "indexerForm.new", "indexers"),
		IsNew:       current == nil,
		Definitions: options,
		Priority:    25,
		Enabled:     true,
		Problems:    problems,
	}
	if current != nil {
		data.Title = s.translate(r, "indexerForm.edit", current.Name)
		data.Indexer = *current
		data.Name = current.Name
		data.Slug = current.Slug
		data.Protocol = current.Protocol
		data.Priority = current.Priority
		data.Tags = current.Tags
		data.Enabled = current.Enabled
	}
	if in != nil {
		data.Name = in.Name
		data.Slug = in.Slug
		data.Protocol = in.Protocol
		if in.Priority != nil {
			data.Priority = *in.Priority
		}
		data.Tags = strings.Join(in.Tags, ", ")
		if in.Enabled != nil {
			data.Enabled = *in.Enabled
		}
	}
	if defID == "" {
		if current != nil {
			defID = current.DefinitionID
		} else if len(options) > 0 {
			defID = options[0].ID
		}
	}
	data.DefinitionID = defID
	if defID == "" {
		data.Problems = append(data.Problems, s.translate(r, "indexerForm.noDefinitions"))
		return data, nil
	}

	parsed, _, err := s.loadDefinitionForForm(r, defID)
	if err != nil {
		return indexerFormPage{}, err
	}
	data.DefinitionName = parsed.Meta.Name
	data.Derived = parsed.IsDerived()
	if data.Protocol == "" {
		data.Protocol = parsed.Meta.Protocol
	}

	config := map[string]string{}
	secretKeys := map[string]bool{}
	if current != nil {
		config = publicConfigMap(current.ConfigJSON)
		secrets, err := s.store.ListSecrets(r.Context(), current.ID)
		if err != nil {
			return indexerFormPage{}, err
		}
		for _, secret := range secrets {
			secretKeys[secret.Key] = true
		}
	}
	value := func(key string) string {
		if in != nil {
			if submitted, ok := in.Settings[key]; ok {
				return submitted
			}
		}
		return config[key]
	}

	data.URL = value("url")
	data.Categories = value("categories")
	data.Query = value("query")
	data.UserAgent = value("user_agent")
	data.DefaultCategory = value("default_category")
	data.AllowPrivate = truthy(value("allow_private"))

	seenSecret := map[string]bool{}
	for _, setting := range parsed.Settings {
		name := setting.Name
		if fixedSettingNames[name] {
			continue
		}
		label := strings.TrimSpace(setting.Label)
		if label == "" {
			label = name
		}
		if indexersvc.IsSecretKey(name) {
			seenSecret[name] = true
			data.Fields = append(data.Fields, indexerSettingField{
				Name:      name,
				Label:     label,
				Help:      setting.Help,
				Type:      "password",
				Required:  setting.Required,
				Secret:    true,
				HasSecret: secretKeys[name],
			})
			continue
		}
		fieldType := settingFieldType(setting)
		raw := value(name)
		if current == nil && in == nil && raw == "" {
			raw = setting.Default
		}
		field := indexerSettingField{
			Name:     name,
			Label:    label,
			Help:     setting.Help,
			Type:     fieldType,
			Value:    raw,
			Required: setting.Required,
		}
		if fieldType == "checkbox" {
			field.Checked = truthy(raw)
		}
		if fieldType == "select" {
			for _, option := range setting.Options {
				field.Options = append(field.Options, indexerSettingOption{
					Value:    option,
					Label:    option,
					Selected: option == raw,
				})
			}
		}
		data.Fields = append(data.Fields, field)
	}

	// A secret present in the vault but missing from the definition is still
	// shown by name, so it can be replaced or removed.
	extra := make([]string, 0)
	for key := range secretKeys {
		if !seenSecret[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	for _, key := range extra {
		data.Fields = append(data.Fields, indexerSettingField{
			Name:      key,
			Label:     key,
			Type:      "password",
			Secret:    true,
			HasSecret: true,
		})
	}
	return data, nil
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

func (s *Server) definitionOptions(r *http.Request) ([]definitionOption, error) {
	defs, err := s.store.ListDefinitions(r.Context())
	if err != nil {
		return nil, err
	}
	options := make([]definitionOption, 0, len(defs))
	installed := make(map[string]bool, len(defs))
	for _, def := range defs {
		installed[def.ID] = true
		option := definitionOption{ID: def.ID, Name: definitionName(def), Installed: true}
		if parsed, err := definition.Parse([]byte(def.Body)); err == nil {
			option.Protocol = parsed.Meta.Protocol
			option.Family = parsed.Connector
			option.Derived = parsed.IsDerived()
			if option.Family == "" {
				option.Family = def.Family
			}
		}
		options = append(options, option)
	}
	entries, err := catalog.Builtins()
	if err != nil {
		return options, nil
	}
	for _, entry := range entries {
		if installed[entry.ID] {
			continue
		}
		options = append(options, definitionOption{
			ID:       entry.ID,
			Name:     entry.Definition.Meta.Name,
			Protocol: entry.Definition.Meta.Protocol,
			Family:   entry.Definition.Connector,
			Derived:  entry.Definition.IsDerived(),
		})
	}
	return options, nil
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
	if !ok {
		return nil, false, indexersvc.ErrDefinitionNotFound
	}
	return entry.Definition, false, nil
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
	if !ok {
		return indexersvc.ErrDefinitionNotFound
	}
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

// indexerInputFromForm parses the generated fields. Empty password fields are
// omitted (keep the stored secret); the clear checkbox sends an empty value,
// which the service interprets as a deletion.
func indexerInputFromForm(r *http.Request, parsed *definition.Definition) (indexersvc.Input, error) {
	priority := 25
	if raw := strings.TrimSpace(r.FormValue("priority")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return indexersvc.Input{}, errors.New("La priorità deve essere un numero")
		}
		priority = value
	}
	enabled := r.FormValue("enabled") != ""
	settings := map[string]string{
		"url":              strings.TrimSpace(r.FormValue("url")),
		"categories":       strings.TrimSpace(r.FormValue("categories")),
		"query":            strings.TrimSpace(r.FormValue("query")),
		"user_agent":       strings.TrimSpace(r.FormValue("user_agent")),
		"default_category": strings.TrimSpace(r.FormValue("default_category")),
		"allow_private":    "false",
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
	return indexersvc.Input{
		DefinitionID: strings.TrimSpace(r.FormValue("definition_id")),
		Name:         strings.TrimSpace(r.FormValue("name")),
		Slug:         strings.TrimSpace(r.FormValue("slug")),
		Protocol:     strings.TrimSpace(r.FormValue("protocol")),
		Priority:     &priority,
		Tags:         splitTagList(r.FormValue("tags")),
		Enabled:      &enabled,
		Settings:     settings,
	}, nil
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
