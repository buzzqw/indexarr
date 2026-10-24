package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/torznabapi"
)

// previewTag marks the temporary disabled indexer used by "Try before adding".
// It is removed before every new try and when the user says "Do not add".
const previewTag = "__preview"

// sitePreviewPage shows up to five real results from a site the user has not
// added yet. The indexer behind it is stored disabled: Sync ignores disabled
// entries, so nothing reaches Sonarr or Radarr until the user presses Add.
type sitePreviewPage struct {
	base
	EntryID   string
	EntryName string
	Name      string
	Query     string
	IndexerID int64
	Releases  []store.Release
	Total     int64
	Err       string
	Status    int
	SearchURL string
	AddURL    string
	CancelURL string
}

// indexerCatalogTry runs one real search with a temporary disabled indexer.
func (s *Server) indexerCatalogTry(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil || s.torznab == nil {
		redirect(w, r, "/indexers/new", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	if err := r.ParseForm(); err != nil {
		redirect(w, r, "/indexers/new", "", s.translate(r, "idx.catalog.probeFailed"))
		return
	}
	entryID := strings.TrimSpace(r.FormValue("definition_id"))
	query := strings.TrimSpace(r.FormValue("q"))
	if query == "" {
		// One click must show results: "1080p" is a broad query that most
		// sites answer without the user having to type a title first.
		query = "1080p"
	}
	entry, ok := s.catalogEntry(entryID)
	if !ok || !catalogQuickAdd(entry) {
		redirect(w, r, "/indexers/new", "", s.translate(r, "idx.catalog.probeFailed"))
		return
	}
	if err := s.ensureStoredDefinition(r, entry.ID); err != nil {
		redirect(w, r, "/indexers/new", "", err.Error())
		return
	}
	s.removePreviewIndexers(r)
	enabled := false
	priority := 25
	preview, err := s.svc.Create(r.Context(), indexersvc.Input{
		DefinitionID: entry.ID,
		Name:         entry.Name + " (preview)",
		Slug:         previewSlug(entry.ID),
		Protocol:     entry.Protocol,
		Priority:     &priority,
		Enabled:      &enabled,
		Tags:         []string{previewTag},
	})
	if err != nil {
		redirect(w, r, "/indexers/new", "", s.indexerFriendlyError(r, err))
		return
	}
	data := sitePreviewPage{
		base:      s.newBaseT(r, "idx.guide.previewTitle", "indexers", entry.Name),
		EntryID:   entry.ID,
		EntryName: entry.Name,
		Name:      entry.Name,
		Query:     query,
		IndexerID: preview.ID,
		Status:    http.StatusOK,
	}
	settings, settingsErr := s.store.LoadSettings(r.Context())
	if settingsErr != nil {
		settings = store.DefaultSettings()
	}
	result, tryErr := s.torznab.Try(r.Context(), torznabapi.BaseURL(r, settings), torznabapi.TryRequest{
		IndexerSlug: preview.Slug,
		Mode:        "search",
		Query:       query,
		Limit:       5,
	})
	if tryErr != nil {
		data.Err = tryErr.Error()
	} else {
		data.Status = result.Status
		data.Total = result.Total
		data.Err = result.Err
		for _, release := range result.Releases {
			if len(data.Releases) >= 5 {
				break
			}
			data.Releases = append(data.Releases, release)
		}
	}
	s.render(w, r, "site_preview", data, http.StatusOK)
}

// indexerCatalogPreviewAdd keeps the preview indexer and enables it.
func (s *Server) indexerCatalogPreviewAdd(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || id <= 0 {
		redirect(w, r, "/indexers/new", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	current, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		redirect(w, r, "/indexers/new", "", s.indexerErrorMessage(r, err))
		return
	}
	// The preview name is an internal label: drop the suffix on the real add.
	name := strings.TrimSuffix(current.Name, " (preview)")
	enabled := true
	if _, err := s.svc.Update(r.Context(), id, indexersvc.Input{
		Name: name, Enabled: &enabled, Tags: withoutTag(current.Tags, previewTag),
	}); err != nil {
		redirect(w, r, "/indexers/new", "", s.indexerFriendlyError(r, err))
		return
	}
	redirect(w, r, "/indexers/"+strconv.FormatInt(id, 10), s.translate(r, "idx.guide.addedNext", name), "")
}

// indexerCatalogPreviewCancel deletes the temporary disabled indexer.
func (s *Server) indexerCatalogPreviewCancel(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		redirect(w, r, "/indexers/new", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err == nil && id > 0 {
		_ = s.svc.Delete(r.Context(), id)
	}
	redirect(w, r, "/indexers/new", "", "")
}

func (s *Server) catalogEntry(id string) (catalog.IndexEntry, bool) {
	index, err := s.siteCatalog()
	if err != nil {
		return catalog.IndexEntry{}, false
	}
	for _, entry := range index {
		if entry.ID == id {
			return entry, true
		}
	}
	return catalog.IndexEntry{}, false
}

func previewSlug(id string) string {
	slug := indexersvc.NormalizeSlug("preview-" + id)
	if slug == "" {
		return "preview"
	}
	return slug
}

func (s *Server) removePreviewIndexers(r *http.Request) {
	indexers, err := s.store.ListIndexers(r.Context())
	if err != nil {
		return
	}
	for _, in := range indexers {
		if hasTag(in.Tags, previewTag) {
			_ = s.svc.Delete(r.Context(), in.ID)
		}
	}
}

func hasTag(raw, want string) bool {
	for _, tag := range splitTagList(raw) {
		if strings.EqualFold(strings.TrimSpace(tag), want) {
			return true
		}
	}
	return false
}

func withoutTag(raw, drop string) []string {
	out := []string{}
	for _, tag := range splitTagList(raw) {
		if !strings.EqualFold(strings.TrimSpace(tag), drop) {
			out = append(out, tag)
		}
	}
	return out
}
