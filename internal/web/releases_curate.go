package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
)

// releasesCurate applies one curation action to every selected release. The
// single-release actions on the detail page toggle a flag; here the action is
// explicit, so a mixed selection always ends in the state the operator chose.
func (s *Server) releasesCurate(w http.ResponseWriter, r *http.Request) {
	op := strings.ToLower(strings.TrimSpace(r.FormValue("op")))
	ids := releaseIDsFromForm(r)
	if len(ids) == 0 {
		redirect(w, r, bulkReturnURL(r), "", s.translate(r, "releases.noneSelected"))
		return
	}
	changed, failed := 0, 0
	for _, id := range ids {
		release, err := s.store.GetRelease(r.Context(), id)
		if err != nil {
			failed++
			s.log.Warn("bulk curation: release not found", "component", "indexarr",
				"subsystem", "web", "release_id", id, "error", err)
			continue
		}
		if err := s.applyBulkCuration(r, op, release); err != nil {
			failed++
			s.log.Warn("bulk curation: release not updated", "component", "indexarr",
				"subsystem", "web", "release_id", id, "action", op, "error", err)
			continue
		}
		changed++
	}
	message := s.translate(r, "releases.bulkCurateResult", changed, failed)
	if failed > 0 {
		redirect(w, r, bulkReturnURL(r), "", message)
		return
	}
	redirect(w, r, bulkReturnURL(r), message, "")
}

// applyBulkCuration sets the flags of one release, or adds the blocklist rule
// that the detail page's Block button would add, and hides the release too.
func (s *Server) applyBulkCuration(r *http.Request, op string, release store.Release) error {
	favorite, hidden := release.Favorite, release.Hidden
	switch op {
	case "favorite":
		favorite = true
	case "unfavorite":
		favorite = false
	case "hide":
		hidden = true
	case "unhide":
		hidden = false
	case "block":
		hash := release.InfoHash
		if hash == "" {
			hash = feed.MagnetHash(release.Magnet)
		}
		kind, value := store.BlockKindTitle, release.Title
		if hash != "" {
			kind, value = store.BlockKindInfohash, hash
		}
		if _, err := s.store.AddBlocklistRule(r.Context(), kind, value,
			fmt.Sprintf("blocked in bulk from release %d", release.ID)); err != nil {
			return err
		}
		hidden = true
	default:
		return fmt.Errorf("unknown bulk curation action %q", op)
	}
	_, err := s.store.SetReleaseFlags(r.Context(), release.ID, favorite, hidden)
	return err
}

// bulkReturnURL keeps the operator on the filtered results page after a bulk
// action. Only a local /releases path is accepted, so the hidden field cannot
// turn the form into an open redirect.
func bulkReturnURL(r *http.Request) string {
	raw := strings.TrimSpace(r.FormValue("return"))
	if strings.HasPrefix(raw, "/releases") && !strings.HasPrefix(raw, "//") {
		return raw
	}
	return "/releases"
}
