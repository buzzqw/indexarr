package web

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/buzzqw/indexarr/internal/store"
)

type bulkEntry struct {
	Name string
	URL  string
	Line int
}

type bulkReport struct {
	Created    int
	Duplicates int
	Queued     int
	Problems   []string
}

// Summary is the one-line message shown in the flash.
func (r bulkReport) Summary() string {
	parts := []string{fmt.Sprintf("%d feed(s) added", r.Created)}
	if r.Duplicates > 0 {
		parts = append(parts, fmt.Sprintf("%d already present", r.Duplicates))
	}
	if len(r.Problems) > 0 {
		parts = append(parts, fmt.Sprintf("%d not added", len(r.Problems)))
	}
	if r.Queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", r.Queued))
	}
	return strings.Join(parts, ", ")
}

type sourceBulkPage struct {
	base
	Feeds           string
	IntervalSeconds int
	Enabled         bool
	RunNow          bool
	DefaultInterval int
	Created         int
	Duplicates      int
	Queued          int
	Problems        []string
}

func (s *Server) sourceBulkNew(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.store.LoadSettings(r.Context())
	s.render(w, r, "source_bulk", sourceBulkPage{
		base:            s.newBase(r, "Aggiunta RSS in blocco", "sources"),
		Enabled:         true,
		DefaultInterval: settings.DefaultIntervalSeconds,
	}, http.StatusOK)
}

func (s *Server) sourceBulkCreate(w http.ResponseWriter, r *http.Request) {
	text := r.FormValue("feeds")
	interval, err := s.formInt(r, "interval_seconds", 0, 0, 30*24*3600)
	if err != nil {
		s.renderBulk(w, r, text, 0, true, false, bulkReport{Problems: []string{err.Error()}}, http.StatusBadRequest)
		return
	}
	if interval != 0 && interval < 60 {
		s.renderBulk(w, r, text, interval, true, false,
			bulkReport{Problems: []string{"interval must be 0 (use default) or at least 60 seconds"}},
			http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("enabled") != ""
	runNow := r.FormValue("run_now") != ""

	entries, problems := parseBulkEntries(text)
	report := bulkReport{Problems: problems}
	if len(entries) == 0 {
		status := http.StatusBadRequest
		s.renderBulk(w, r, text, interval, enabled, runNow, report, status)
		return
	}

	existing, err := s.store.ListSources(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	urls := make(map[string]bool, len(existing)+len(entries))
	names := make(map[string]bool, len(existing)+len(entries))
	for _, source := range existing {
		urls[canonicalFeedURL(source.URL)] = true
		names[strings.ToLower(source.Name)] = true
	}

	var createdIDs []int64
	for _, entry := range entries {
		key := canonicalFeedURL(entry.URL)
		if urls[key] {
			report.Duplicates++
			continue
		}
		name := uniqueSourceName(entry.Name, names)
		id, err := s.store.CreateSource(r.Context(), store.Source{
			Name:            name,
			Kind:            "rss",
			Manager:         "auto",
			URL:             entry.URL,
			Enabled:         enabled,
			IntervalSeconds: interval,
		})
		if err != nil {
			report.Problems = append(report.Problems,
				fmt.Sprintf("%s: %v", bulkEntryLabel(entry), friendlySourceError(err)))
			continue
		}
		urls[key] = true
		names[strings.ToLower(name)] = true
		report.Created++
		createdIDs = append(createdIDs, id)
	}

	if runNow {
		for _, id := range createdIDs {
			if err := s.engine.Trigger(id); err == nil {
				report.Queued++
			}
		}
	}

	if len(report.Problems) > 0 {
		status := http.StatusOK
		if report.Created == 0 {
			status = http.StatusBadRequest
		}
		s.renderBulk(w, r, text, interval, enabled, runNow, report, status)
		return
	}
	redirect(w, r, "/sources", report.Summary(), "")
}

func (s *Server) renderBulk(w http.ResponseWriter, r *http.Request, text string, interval int, enabled, runNow bool, report bulkReport, status int) {
	settings, _ := s.store.LoadSettings(r.Context())
	data := sourceBulkPage{
		base:            s.newBase(r, "Aggiunta RSS in blocco", "sources"),
		Feeds:           text,
		IntervalSeconds: interval,
		Enabled:         enabled,
		RunNow:          runNow,
		DefaultInterval: settings.DefaultIntervalSeconds,
		Created:         report.Created,
		Duplicates:      report.Duplicates,
		Queued:          report.Queued,
		Problems:        report.Problems,
	}
	if len(report.Problems) > 0 {
		data.Error = report.Summary()
	}
	s.render(w, r, "source_bulk", data, status)
}

// parseBulkEntries accepts either an OPML document or a plain list with one
// feed per line. A line can be just the URL or "Name | URL" (a tab works too).
// Lines starting with # are comments.
func parseBulkEntries(text string) ([]bulkEntry, []string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, []string{"no feed provided"}
	}
	if looksLikeOPML(trimmed) {
		return parseOPMLEntries(trimmed)
	}

	var entries []bulkEntry
	var problems []string
	for index, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lineNumber := index + 1
		name, rawURL := splitFeedLine(line)
		rawURL = strings.Trim(rawURL, "\"' ")
		if !validFeedURL(rawURL) {
			problems = append(problems, fmt.Sprintf("line %d: invalid feed URL %q", lineNumber, rawURL))
			continue
		}
		if name == "" {
			name = autoFeedName(rawURL)
		}
		entries = append(entries, bulkEntry{Name: strings.TrimSpace(name), URL: rawURL, Line: lineNumber})
	}
	if len(entries) == 0 && len(problems) == 0 {
		problems = append(problems, "no feed found (only comments or empty lines)")
	}
	return entries, problems
}

func splitFeedLine(line string) (name, rawURL string) {
	if index := strings.Index(line, "|"); index >= 0 {
		left := strings.TrimSpace(line[:index])
		right := strings.TrimSpace(line[index+1:])
		if validFeedURL(left) && !validFeedURL(right) {
			return right, left
		}
		return left, right
	}
	if fields := strings.SplitN(line, "\t", 2); len(fields) == 2 {
		left := strings.TrimSpace(fields[0])
		right := strings.TrimSpace(fields[1])
		if validFeedURL(left) && !validFeedURL(right) {
			return right, left
		}
		return left, right
	}
	return "", line
}

type opmlDocument struct {
	Body struct {
		Outlines []opmlOutline `xml:"outline"`
	} `xml:"body"`
}

type opmlOutline struct {
	Text        string        `xml:"text,attr"`
	Title       string        `xml:"title,attr"`
	XMLURL      string        `xml:"xmlUrl,attr"`
	XMLURLLower string        `xml:"xmlurl,attr"`
	Outlines    []opmlOutline `xml:"outline"`
}

func parseOPMLEntries(text string) ([]bulkEntry, []string) {
	var document opmlDocument
	if err := xml.Unmarshal([]byte(text), &document); err != nil {
		return nil, []string{"invalid OPML: " + err.Error()}
	}
	var entries []bulkEntry
	var problems []string
	seen := map[string]bool{}
	var walk func([]opmlOutline)
	walk = func(list []opmlOutline) {
		for _, outline := range list {
			rawURL := strings.TrimSpace(firstNonEmpty(outline.XMLURL, outline.XMLURLLower))
			if rawURL != "" {
				key := canonicalFeedURL(rawURL)
				switch {
				case !validFeedURL(rawURL):
					problems = append(problems, fmt.Sprintf("OPML: invalid feed URL %q", rawURL))
				case seen[key]:
					// The same feed exported twice is not an error.
				default:
					seen[key] = true
					name := firstNonEmpty(outline.Text, outline.Title)
					if name == "" {
						name = autoFeedName(rawURL)
					}
					entries = append(entries, bulkEntry{
						Name: name,
						URL:  rawURL,
					})
				}
			}
			walk(outline.Outlines)
		}
	}
	walk(document.Body.Outlines)
	if len(entries) == 0 && len(problems) == 0 {
		problems = append(problems, "OPML contains no feed (no xmlUrl attribute)")
	}
	return entries, problems
}

func looksLikeOPML(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(lower, "<") && strings.Contains(lower, "<opml")
}

func validFeedURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == "http" || scheme == "https"
}

func canonicalFeedURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
}

func autoFeedName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return "RSS feed"
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if host == "" {
		return "RSS feed"
	}
	return host
}

func uniqueSourceName(base string, taken map[string]bool) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "RSS feed"
	}
	base = truncateRunes(base, 100)
	if !taken[strings.ToLower(base)] {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := truncateRunes(base, 90) + fmt.Sprintf(" (%d)", suffix)
		if !taken[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

func truncateRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max])
}

func bulkEntryLabel(entry bulkEntry) string {
	if entry.Line > 0 {
		return fmt.Sprintf("line %d", entry.Line)
	}
	return "OPML"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
