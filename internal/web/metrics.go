package web

import (
	"fmt"
	"net/http"
	"time"
)

// metrics exposes the index state in Prometheus text format. Like /healthz it
// is unauthenticated so a local scraper can read it.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.LoadStats(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rules, _ := s.store.ListBlocklist(r.Context())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "# HELP indexarr_up 1 when the process is serving requests.\n")
	fmt.Fprintf(w, "# TYPE indexarr_up gauge\nindexarr_up 1\n")
	fmt.Fprintf(w, "# HELP indexarr_uptime_seconds Process uptime.\n")
	fmt.Fprintf(w, "# TYPE indexarr_uptime_seconds gauge\nindexarr_uptime_seconds %d\n", int64(time.Since(s.started).Seconds()))
	fmt.Fprintf(w, "# HELP indexarr_releases Cached releases.\n")
	fmt.Fprintf(w, "# TYPE indexarr_releases gauge\nindexarr_releases %d\n", stats.Releases)
	fmt.Fprintf(w, "# HELP indexarr_torrents Releases with a stored .torrent.\n")
	fmt.Fprintf(w, "# TYPE indexarr_torrents gauge\nindexarr_torrents %d\n", stats.Torrents)
	fmt.Fprintf(w, "# HELP indexarr_torrent_bytes Bytes stored as .torrent payloads.\n")
	fmt.Fprintf(w, "# TYPE indexarr_torrent_bytes gauge\nindexarr_torrent_bytes %d\n", stats.TorrentBytes)
	fmt.Fprintf(w, "# HELP indexarr_sources Configured sources.\n")
	fmt.Fprintf(w, "# TYPE indexarr_sources gauge\nindexarr_sources %d\n", stats.Sources)
	fmt.Fprintf(w, "# HELP indexarr_enabled_sources Enabled sources.\n")
	fmt.Fprintf(w, "# TYPE indexarr_enabled_sources gauge\nindexarr_enabled_sources %d\n", stats.EnabledSources)
	fmt.Fprintf(w, "# HELP indexarr_runs_24h Runs started in the last 24 hours.\n")
	fmt.Fprintf(w, "# TYPE indexarr_runs_24h gauge\nindexarr_runs_24h %d\n", stats.Runs24h)
	fmt.Fprintf(w, "# HELP indexarr_errors_24h Failed runs in the last 24 hours.\n")
	fmt.Fprintf(w, "# TYPE indexarr_errors_24h gauge\nindexarr_errors_24h %d\n", stats.Errors24h)
	fmt.Fprintf(w, "# HELP indexarr_blocklist_rules Active blocklist rules.\n")
	fmt.Fprintf(w, "# TYPE indexarr_blocklist_rules gauge\nindexarr_blocklist_rules %d\n", len(rules))
	fmt.Fprintf(w, "# HELP indexarr_db_bytes Database size including the WAL.\n")
	fmt.Fprintf(w, "# TYPE indexarr_db_bytes gauge\nindexarr_db_bytes %d\n", stats.DBBytes)
}
