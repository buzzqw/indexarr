package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
)

type rulesPage struct {
	base
	Rules []store.BlocklistRule
}

func (s *Server) rulesPage(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListBlocklist(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.render(w, r, "rules", rulesPage{
		base:  s.newBase(r, "Regole", "rules"),
		Rules: rules,
	}, http.StatusOK)
}

func (s *Server) rulesAdd(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.FormValue("kind"))
	value := strings.TrimSpace(r.FormValue("value"))
	note := strings.TrimSpace(r.FormValue("note"))
	if value == "" {
		redirect(w, r, "/rules", "", "The rule value is required")
		return
	}
	if _, err := s.store.AddBlocklistRule(r.Context(), kind, value, note); err != nil {
		redirect(w, r, "/rules", "", err.Error())
		return
	}
	redirect(w, r, "/rules", "Rule added; future runs will skip matching releases", "")
}

func (s *Server) rulesDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/rules", "", "invalid rule id")
		return
	}
	if _, err := s.store.DeleteBlocklistRule(r.Context(), id); err != nil {
		redirect(w, r, "/rules", "", err.Error())
		return
	}
	redirect(w, r, "/rules", "Rule deleted", "")
}

func (s *Server) releaseFavorite(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/releases", "", "invalid release id")
		return
	}
	release, err := s.store.GetRelease(r.Context(), id)
	if err != nil {
		redirect(w, r, "/releases", "", "release not found")
		return
	}
	if _, err := s.store.SetReleaseFlags(r.Context(), id, !release.Favorite, release.Hidden); err != nil {
		redirect(w, r, "/releases/"+fmt.Sprint(id), "", err.Error())
		return
	}
	redirect(w, r, "/releases/"+fmt.Sprint(id), "", "")
}

func (s *Server) releaseHide(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/releases", "", "invalid release id")
		return
	}
	release, err := s.store.GetRelease(r.Context(), id)
	if err != nil {
		redirect(w, r, "/releases", "", "release not found")
		return
	}
	if _, err := s.store.SetReleaseFlags(r.Context(), id, release.Favorite, !release.Hidden); err != nil {
		redirect(w, r, "/releases/"+fmt.Sprint(id), "", err.Error())
		return
	}
	message := "Release hidden"
	if release.Hidden {
		message = "Release visible again"
	}
	redirect(w, r, "/releases/"+fmt.Sprint(id), message, "")
}

func (s *Server) releaseBlock(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/releases", "", "invalid release id")
		return
	}
	release, err := s.store.GetRelease(r.Context(), id)
	if err != nil {
		redirect(w, r, "/releases", "", "release not found")
		return
	}
	kind, value := store.BlockKindTitle, release.Title
	hash := release.InfoHash
	if hash == "" {
		hash = feed.MagnetHash(release.Magnet)
	}
	if hash != "" {
		kind, value = store.BlockKindInfohash, hash
	}
	if _, err := s.store.AddBlocklistRule(r.Context(), kind, value,
		fmt.Sprintf("blocked from release %d", release.ID)); err != nil {
		redirect(w, r, "/releases/"+fmt.Sprint(id), "", err.Error())
		return
	}
	if _, err := s.store.SetReleaseFlags(r.Context(), id, release.Favorite, true); err != nil {
		redirect(w, r, "/releases/"+fmt.Sprint(id), "", err.Error())
		return
	}
	redirect(w, r, "/releases", "Release blocked and hidden; future runs will skip it", "")
}
