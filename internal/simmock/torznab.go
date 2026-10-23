package simmock

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleTorznab(w http.ResponseWriter, r *http.Request) {
	if !s.requireTorznabKey(w, r) {
		return
	}

	s.mu.Lock()
	s.state.TorznabRequests++
	s.mu.Unlock()

	query := r.URL.Query()
	function := strings.ToLower(strings.TrimSpace(query.Get("t")))
	if function == "" {
		function = "search"
	}
	switch function {
	case "caps":
		s.writeCaps(w)
	case "search", "tvsearch", "movie", "music", "book":
		s.mu.Lock()
		s.state.TorznabSearches++
		s.mu.Unlock()
		s.writeSearch(w, r)
	case "get":
		s.serveTorznabGet(w, r)
	default:
		writeTorznabError(w, http.StatusBadRequest, 300, "unsupported function: "+function)
	}
}

func (s *Server) writeCaps(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server version="1.0" title="simmock Torznab"/>
  <limits max="100" default="50"/>
  <registration available="no" open="no"/>
  <searching>
    <search available="yes" supportedParams="q"/>
    <tv-search available="yes" supportedParams="q,season,ep,imdbid,tvdbid"/>
    <movie-search available="yes" supportedParams="q,imdbid,tmdbid"/>
    <music-search available="yes" supportedParams="q,artist,album"/>
    <book-search available="yes" supportedParams="q,author,title"/>
  </searching>
  <categories>
    <category id="2000" name="Movies"><subcat id="2040" name="Movies HD"/></category>
    <category id="3000" name="Audio"><subcat id="3040" name="Audio Lossless"/></category>
    <category id="5000" name="TV"><subcat id="5040" name="TV HD"/></category>
    <category id="7000" name="Books"><subcat id="7020" name="Books Ebook"/></category>
  </categories>
</caps>
`))
}

func (s *Server) writeSearch(w http.ResponseWriter, r *http.Request) {
	base := requestBaseURL(r)
	key := url.QueryEscape(strings.TrimSpace(r.URL.Query().Get("apikey")))
	if key == "" {
		key = url.QueryEscape(s.apiKey)
	}

	limit := len(s.releases)
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed < limit {
			limit = parsed
		}
	}

	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">`)
	b.WriteString(`<channel><title>simmock</title>`)
	fmt.Fprintf(&b, `<link>%s</link>`, xmlEscape(base))
	b.WriteString(`<description>simmock deterministic indexer</description>`)
	fmt.Fprintf(&b, `<newznab:response offset="0" total="%d"/>`, len(s.releases))

	for i := 0; i < limit; i++ {
		rel := s.releases[i]
		link := ""
		enclosure := ""
		switch {
		case rel.Usenet:
			link = base + "/newznab/api?t=get&id=" + url.QueryEscape(rel.GUID) + "&apikey=" + key
			enclosure = base + "/newznab/download/" + rel.ID + ".nzb"
		case rel.Magnet != "":
			link = rel.Magnet
			enclosure = base + "/torznab/download/" + rel.ID + ".torrent"
		default:
			link = base + "/torznab/api?t=get&id=" + url.QueryEscape(rel.GUID) + "&apikey=" + key
			enclosure = base + "/torznab/download/" + rel.ID + ".torrent"
		}

		b.WriteString(`<item>`)
		fmt.Fprintf(&b, `<title>%s</title>`, xmlEscape(rel.Title))
		fmt.Fprintf(&b, `<guid isPermaLink="false">%s</guid>`, xmlEscape(rel.GUID))
		fmt.Fprintf(&b, `<link>%s</link>`, xmlEscape(link))
		fmt.Fprintf(&b, `<comments>%s/tracker/details/%s</comments>`, xmlEscape(base), rel.ID)
		fmt.Fprintf(&b, `<pubDate>%s</pubDate>`, rel.Published.Format(time.RFC1123Z))
		fmt.Fprintf(&b, `<size>%d</size>`, rel.Size)
		fmt.Fprintf(&b, `<category>%s</category>`, rel.Category)
		fmt.Fprintf(&b, `<enclosure url="%s" length="%d" type="%s"/>`,
			xmlEscape(enclosure), rel.Size, rel.enclosureType())

		attrs := [][2]string{
			{"category", rel.Category},
			{"size", strconv.FormatInt(rel.Size, 10)},
			{"seeders", strconv.FormatInt(rel.Seeders, 10)},
			{"leechers", strconv.FormatInt(rel.Leechers, 10)},
			{"peers", strconv.FormatInt(rel.Seeders+rel.Leechers, 10)},
			{"publishdate", rel.Published.Format(time.RFC3339)},
		}
		if rel.InfoHash != "" {
			attrs = append(attrs, [2]string{"infohash", rel.InfoHash})
		}
		if rel.Magnet != "" {
			attrs = append(attrs, [2]string{"magneturl", rel.Magnet})
		}
		if rel.Usenet {
			attrs = append(attrs, [2]string{"protocol", "usenet"})
		}
		for _, attr := range attrs {
			fmt.Fprintf(&b, `<torznab:attr name="%s" value="%s"/>`, attr[0], xmlEscape(attr[1]))
			fmt.Fprintf(&b, `<newznab:attr name="%s" value="%s"/>`, attr[0], xmlEscape(attr[1]))
		}
		b.WriteString(`</item>`)
	}
	b.WriteString(`</channel></rss>`)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b.Bytes())
}

func (s *Server) serveTorznabGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	index, ok := s.byID[id]
	if !ok {
		writeTorznabError(w, http.StatusNotFound, 201, "release not found")
		return
	}
	s.servePayload(w, index)
}

func (s *Server) handleTorznabDownload(w http.ResponseWriter, r *http.Request) {
	if !s.requireTorznabKey(w, r) {
		return
	}
	file := r.PathValue("file")
	id := strings.TrimSuffix(strings.TrimSuffix(file, ".torrent"), ".nzb")
	index, ok := s.byID[id]
	if !ok {
		writeTorznabError(w, http.StatusNotFound, 201, "payload not found")
		return
	}
	s.servePayload(w, index)
}

func (s *Server) servePayload(w http.ResponseWriter, index int) {
	if index < 0 || index >= len(s.releases) {
		writeTorznabError(w, http.StatusNotFound, 201, "payload not found")
		return
	}
	rel := s.releases[index]
	s.mu.Lock()
	s.state.TorznabDownloads++
	s.mu.Unlock()

	if rel.Usenet {
		w.Header().Set("Content-Type", "application/x-nzb")
		w.Header().Set("Content-Disposition", `attachment; filename="`+rel.Title+`.nzb"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(rel.NZB)))
		_, _ = w.Write(rel.NZB)
		return
	}
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", `attachment; filename="`+rel.Title+`.torrent"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(rel.Torrent)))
	_, _ = w.Write(rel.Torrent)
}

func writeTorznabError(w http.ResponseWriter, status, code int, description string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header +
		fmt.Sprintf(`<error code="%d" description="%s"/>`, code, xmlEscape(description)) + "\n"))
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func xmlEscape(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}
