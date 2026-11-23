package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

// A download is a bounded read, not a new way to dump the whole cache: the UI
// says that the first five thousand rows are exported.
const releasesExportMax = 5000

// releasesExport downloads the rows of the current catalogue search in CSV or
// JSON. It reuses releasesData so the file contains exactly what the page
// shows, with the same filters and the same duplicate fusion.
func (s *Server) releasesExport(w http.ResponseWriter, r *http.Request) {
	format := strings.ToLower(strings.TrimSpace(r.FormValue("format")))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		redirect(w, r, "/releases", "", s.translate(r, "export.invalidFormat"))
		return
	}

	data, err := s.releasesData(r)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	filter := data.Filter
	filter.Offset = 0
	filter.Limit = releasesExportMax + 1
	releases, _, err := s.store.SearchReleases(r.Context(), filter)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !data.ShowDuplicates {
		releases = store.FuseReleases(releases)
	}
	truncated := len(releases) > releasesExportMax
	if truncated {
		releases = releases[:releasesExportMax]
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	base := scheme + "://" + r.Host
	filename := "indexarr-releases-" + time.Now().Format("2006-01-02") + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Indexarr-Export-Truncated", strconv.FormatBool(truncated))
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		payload := struct {
			Count     int                `json:"count"`
			Truncated bool               `json:"truncated"`
			Releases  []releaseExportRow `json:"releases"`
		}{Count: len(releases), Truncated: truncated, Releases: make([]releaseExportRow, 0, len(releases))}
		for _, release := range releases {
			payload.Releases = append(payload.Releases, newReleaseExportRow(release, base))
		}
		_ = json.NewEncoder(w).Encode(payload)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	writer := csv.NewWriter(w)
	defer writer.Flush()
	header := []string{"id", "title", "size_bytes", "seeders", "leechers", "category",
		"protocol", "published", "first_seen", "url", "magnet", "ed2k", "infohash"}
	if err := writer.Write(header); err != nil {
		return
	}
	for _, release := range releases {
		row := newReleaseExportRow(release, base)
		if err := writer.Write([]string{
			csvSafe(strconv.FormatInt(release.ID, 10)),
			csvSafe(row.Title),
			strconv.FormatInt(release.Size, 10),
			strconv.FormatInt(release.Seeders, 10),
			strconv.FormatInt(release.Leechers, 10),
			csvSafe(row.Category),
			csvSafe(row.Protocol),
			csvSafe(row.Published),
			csvSafe(row.FirstSeen),
			csvSafe(row.URL),
			csvSafe(row.Magnet),
			csvSafe(row.ED2K),
			csvSafe(row.InfoHash),
		}); err != nil {
			return
		}
	}
}

type releaseExportRow struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Size      int64  `json:"size_bytes"`
	Seeders   int64  `json:"seeders"`
	Leechers  int64  `json:"leechers"`
	Category  string `json:"category"`
	Protocol  string `json:"protocol"`
	Published string `json:"published"`
	FirstSeen string `json:"first_seen"`
	URL       string `json:"url"`
	Magnet    string `json:"magnet"`
	ED2K      string `json:"ed2k,omitempty"`
	InfoHash  string `json:"infohash,omitempty"`
}

func newReleaseExportRow(release store.Release, base string) releaseExportRow {
	published, firstSeen := "", ""
	if !release.PublishedAt.IsZero() {
		published = release.PublishedAt.UTC().Format(time.RFC3339)
	}
	if !release.FirstSeenAt.IsZero() {
		firstSeen = release.FirstSeenAt.UTC().Format(time.RFC3339)
	}
	url := fmt.Sprintf("/releases/%d", release.ID)
	if base != "" {
		url = base + url
	}
	return releaseExportRow{
		ID: release.ID, Title: release.Title, Size: release.Size,
		Seeders: release.Seeders, Leechers: release.Leechers,
		Category: release.Category, Protocol: release.Protocol,
		Published: published, FirstSeen: firstSeen, URL: url,
		Magnet: release.Magnet, ED2K: release.Ed2k, InfoHash: release.InfoHash,
	}
}

// csvSafe stops a title or link beginning with a spreadsheet formula marker
// from being executed when the file is opened in a spreadsheet program.
func csvSafe(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" && strings.ContainsRune("=+-@\t\r", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}
