package web

import (
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

func choiceValue(raw string, allowed ...string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	for _, value := range allowed {
		if raw == value {
			return value
		}
	}
	return ""
}

// languageFilterValue keeps the free-text language the user typed: the filter
// is no longer a list of two codes, so anything a source may declare must
// survive. Only the characters a language name can contain are kept, and the
// value is capped so a pasted paragraph cannot end up in the query.
func languageFilterValue(raw string) string {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	space := false
	runes := 0
	for _, r := range raw {
		if runes >= 40 {
			break
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '\'' || r == '_' || r == '/':
			b.WriteRune(r)
			space = false
			runes++
		case unicode.IsSpace(r):
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
				space = true
				runes++
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func (s *Server) savedSearchAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 60 {
		redirect(w, r, "/releases", "", s.translate(r, "saved.error.name"))
		return
	}
	raw := strings.TrimSpace(r.FormValue("query"))
	if len(raw) > 2000 {
		redirect(w, r, "/releases", "", s.translate(r, "saved.error.tooLong"))
		return
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		redirect(w, r, "/releases", "", s.translate(r, "saved.error.invalid", err.Error()))
		return
	}
	values.Del("page")
	query := values.Encode()
	if _, err := s.store.AddSavedSearch(r.Context(), name, query); err != nil {
		redirect(w, r, "/releases", "", err.Error())
		return
	}
	target := "/releases"
	if query != "" {
		target += "?" + query
	}
	redirect(w, r, target, s.translate(r, "saved.saved"), "")
}

func (s *Server) savedSearchDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/releases", "", s.translate(r, "saved.error.invalidID"))
		return
	}
	if _, err := s.store.DeleteSavedSearch(r.Context(), id); err != nil {
		redirect(w, r, "/releases", "", err.Error())
		return
	}
	redirect(w, r, "/releases", s.translate(r, "saved.deleted"), "")
}
