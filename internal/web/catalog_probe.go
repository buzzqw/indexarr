package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/catalog"
)

// indexerCatalogProbe is the one-click "Prova" for a lightweight catalog row.
// It only proves reachability: a 200 is not a verified search parser. A
// positive answer with a known family opens the add form; otherwise the user
// gets a sentence explaining what is missing.
func (s *Server) indexerCatalogProbe(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		redirect(w, r, "/indexers/new", "", s.translate(r, "idx.catalog.probeFailed"))
		return
	}
	id := strings.TrimSpace(r.FormValue("definition_id"))
	if id == "" {
		redirect(w, r, "/indexers/new", "", s.translate(r, "idx.catalog.probeFailed"))
		return
	}
	sites, err := catalog.Sites()
	if err != nil {
		redirect(w, r, "/indexers/new", "", s.translate(r, "idx.catalog.probeFailed"))
		return
	}
	var site *catalog.Site
	for i := range sites {
		if sites[i].ID == id {
			site = &sites[i]
			break
		}
	}
	if site == nil {
		redirect(w, r, "/indexers/new", "", s.translate(r, "idx.catalog.probeFailed"))
		return
	}
	if site.State == catalog.SiteStateDead {
		redirect(w, r, "/indexers/new?q="+id, "", s.translate(r, "idx.catalog.probeFailed"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, site.URL, nil)
	if err != nil {
		redirect(w, r, "/indexers/new?q="+id, "", s.translate(r, "idx.catalog.probeFailed"))
		return
	}
	request.Header.Set("User-Agent", "indexarr/catalog-probe")
	client := &http.Client{Timeout: 12 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		redirect(w, r, "/indexers/new?q="+id, "", s.translate(r, "idx.catalog.probeFailed"))
		return
	}
	response.Body.Close()
	if response.StatusCode >= 400 {
		redirect(w, r, "/indexers/new?q="+id, "", s.translate(r, "idx.catalog.probeFailed"))
		return
	}
	if site.Family != "" && site.Family != catalog.FamilyCustom {
		redirect(w, r, "/indexers/new?definition="+site.ID, s.translate(r, "idx.catalog.probeOK"), "")
		return
	}
	redirect(w, r, "/indexers/new?q="+id, "", s.translate(r, "idx.catalog.probeNoFamily"))
}
