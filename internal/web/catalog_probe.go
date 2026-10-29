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
	// A site counts as reachable when any of its addresses answers: a dead
	// first domain with a live mirror is not a failed probe.
	if !probeAnyAddress(r.Context(), site) {
		redirect(w, r, "/indexers/new?q="+id, "", s.translate(r, "idx.catalog.probeFailed"))
		return
	}
	if site.Family != "" && site.Family != catalog.FamilyCustom {
		redirect(w, r, "/indexers/new?definition="+site.ID, s.translate(r, "idx.catalog.probeOK"), "")
		return
	}
	redirect(w, r, "/indexers/new?q="+id, "", s.translate(r, "idx.catalog.probeNoFamily"))
}

// probeAnyAddress tries the site's main address and then its mirrors, and
// reports whether one of them answers below 400.
func probeAnyAddress(ctx context.Context, site *catalog.Site) bool {
	addresses := append([]string{site.URL}, site.URLs...)
	seen := make(map[string]bool, len(addresses))
	client := &http.Client{Timeout: 12 * time.Second}
	for _, address := range addresses {
		address = strings.TrimSpace(address)
		if address == "" || seen[address] {
			continue
		}
		seen[address] = true
		probeCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, address, nil)
		if err != nil {
			cancel()
			continue
		}
		request.Header.Set("User-Agent", "indexarr/catalog-probe")
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
		}
		cancel()
		if err == nil && response.StatusCode < 400 {
			return true
		}
	}
	return false
}
