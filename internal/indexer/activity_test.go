package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
)

func TestRunMetricsOneElementPerDiscardReason(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	registry := engine.NewRegistry()
	now := time.Now()
	items := []engine.Item{
		{Title: "Valid", InfoHash: hashA, Magnet: feed.MagnetFromHash(hashA, "Valid"),
			Size: 100, Seeders: 5, Categories: []int{5040}, Published: now},
		{Title: "Duplicate", InfoHash: hashA, Magnet: feed.MagnetFromHash(hashA, "Duplicate"),
			Size: 100, Seeders: 5, Categories: []int{5040}, Published: now},
		{Title: "Blocked", InfoHash: strings.Repeat("b", 40),
			Magnet: feed.MagnetFromHash(strings.Repeat("b", 40), "Blocked"),
			Size:   100, Seeders: 3, Categories: []int{5040}, Published: now},
		{Title: "Too Old", InfoHash: strings.Repeat("c", 40),
			Magnet: feed.MagnetFromHash(strings.Repeat("c", 40), "Too Old"),
			Size:   100, Seeders: 2, Categories: []int{5040},
			Published: now.AddDate(0, 0, -10)},
		{Title: "Wrong Category", InfoHash: strings.Repeat("d", 40),
			Magnet: feed.MagnetFromHash(strings.Repeat("d", 40), "Wrong Category"),
			Size:   100, Seeders: 2, Categories: []int{2040}, Published: now},
		{Title: "No Link", Size: 100, Seeders: 1, Categories: []int{5040}, Published: now},
		{Title: "Download Fails", DownloadURL: "https://fake.example/dl",
			Size: 100, Seeders: 1, Categories: []int{5040}, Published: now},
	}
	fake := &fakeConnector{items: items, downloadErr: errors.New("download failed")}
	if err := registry.RegisterConnector("fake", func(engine.Config) (engine.Connector, error) {
		return fake, nil
	}); err != nil {
		t.Fatalf("RegisterConnector: %v", err)
	}
	manager, err := New(Config{
		Store:     st,
		Vault:     v,
		Registry:  registry,
		Transport: newTestTransport(),
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := `schema: 1
meta: {id: fake-metrics, name: Fake Metrics, type: public, protocol: torrent, urls: ["https://fake.example/"]}
connector: fake
`
	seedDefinition(t, st, "fake-metrics", "fake", body)
	indexerID := createIndexer(t, st, "fake-metrics", "fake-metrics", "Fake Metrics",
		"torrent", `{"query":"anything","categories":[5040],"max_age_days":1}`)

	if _, err := st.AddBlocklistRule(context.Background(), store.BlockKindInfohash,
		strings.Repeat("b", 40), "metric test"); err != nil {
		t.Fatalf("AddBlocklistRule: %v", err)
	}

	stats, err := manager.Run(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Seen != len(items) {
		t.Errorf("Seen = %d, want %d", stats.Seen, len(items))
	}
	if stats.New != 1 || stats.Duplicates != 1 {
		t.Errorf("New = %d, Duplicates = %d", stats.New, stats.Duplicates)
	}
	for name, got := range map[string]int{
		"no link":  stats.DiscardNoLink,
		"blocked":  stats.Blocked,
		"too old":  stats.DiscardTooOld,
		"category": stats.DiscardCategory,
		"other":    stats.DiscardOther,
	} {
		if got != 1 {
			t.Errorf("%s = %d, want 1", name, got)
		}
	}
	if stats.Discarded != 5 {
		t.Errorf("Discarded = %d, want 5", stats.Discarded)
	}
	if stats.Skipped != 5 {
		t.Errorf("Skipped = %d, want 5 (legacy counter includes duplicates)", stats.Skipped)
	}
	if stats.FieldTotal != len(items) {
		t.Errorf("FieldTotal = %d, want %d", stats.FieldTotal, len(items))
	}

	rows, err := st.ListRunMetrics(context.Background(), indexerID, 10)
	if err != nil {
		t.Fatalf("ListRunMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("run rows = %d, want 1", len(rows))
	}
	metrics := rows[0].Metrics
	if metrics.Seen != int64(len(items)) || metrics.DiscardNoLink != 1 ||
		metrics.DiscardBlocked != 1 || metrics.DiscardTooOld != 1 ||
		metrics.DiscardCategory != 1 || metrics.DiscardOther != 1 ||
		metrics.Discarded != 5 || metrics.Duplicates != 1 {
		t.Fatalf("stored metrics = %+v", metrics)
	}
}

func activityDefinition(serverURL string) string {
	return fmt.Sprintf(`schema: 1
meta:
  id: activity-site
  name: Activity Site
  type: public
  protocol: torrent
  urls: [%q]
settings:
  - { name: apikey, type: password, required: true }

capabilities:
  search: [q]

auth:
  strategy: none
search:
  request:
    get: /search
    params:
      q: "{{ query.keywords }}"
      apikey: "{{ settings.apikey }}"
  response:
    type: html
    rows: "table.results tr"
    fields:
      title: { selector: "td.name a", filters: [trim] }
      download: { selector: "a.download", attr: href, type: url }
      infohash: { selector: "td.hash" }
      category: { selector: "a.cat", attr: href, filters: [{ query_param: cat }], type: category }
      size: { selector: "td.size", type: size }
      seeders: { selector: "td.seeders", type: int }
      date: { selector: "td.date", type: date, layouts: ["02-01-2006 15:04"] }
    required: [title, download, size]
`, serverURL)
}

func TestRunSampleIsTruncatedAndRedacted(t *testing.T) {
	const apiKey = "SYNTHETICKEY"
	const rssKey = "SYNTHETICRSSKEY"
	hash := strings.Repeat("e", 40)
	filler := strings.Repeat("filler ", 50<<10) // about 350 KiB
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			if r.URL.Query().Get("apikey") != apiKey {
				t.Errorf("apikey = %q, want the synthetic key", r.URL.Query().Get("apikey"))
			}
			_, _ = fmt.Fprintf(w, `<html><body><table class="results"><tbody>
<tr>
  <td class="name"><a href="/details/1?rsskey=%s">Redacted Release</a></td>
  <td><a class="download" href="/download/1.torrent?rsskey=%s&apikey=%s">DL</a></td>
  <td class="hash">%s</td>
  <td><a class="cat" href="/browse?cat=5">TV</a></td>
  <td class="size">1 GB</td>
  <td class="seeders">5</td>
  <td class="date">02-01-2026 15:04</td>
</tr>
</tbody></table><p>%s</p><p>%s</p><p>%s</p>%s</body></html>`,
				rssKey, rssKey, apiKey, hash, apiKey, rssKey, server.URL, filler)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	st := newTestStore(t)
	v := newTestVault(t, 1)
	manager := newTestManager(t, st, v)
	seedDefinition(t, st, "activity-site", "", activityDefinition(server.URL))
	config := fmt.Sprintf(`{"url":%q,"query":"release","categories":[5040],"rsskey":%q}`, server.URL, rssKey)
	indexerID := createIndexer(t, st, "activity-site", "activity-site", "Activity Site", "torrent", config)
	putSecret(t, st, v, indexerID, "api_key", apiKey)

	if _, err := manager.Run(context.Background(), indexerID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sample, found, err := st.GetIndexerSample(context.Background(), indexerID)
	if err != nil || !found {
		t.Fatalf("GetIndexerSample: found=%t err=%v", found, err)
	}
	if !sample.Truncated {
		t.Error("sample was not marked truncated")
	}
	if len(sample.Body) > engine.ObservationBodyLimit {
		t.Errorf("sample body = %d bytes, limit %d", len(sample.Body), engine.ObservationBodyLimit)
	}
	for name, value := range map[string]string{
		"request URL": sample.RequestURL,
		"body":        sample.Body,
		"item URL":    sample.Items[0].DownloadURL,
		"item detail": sample.Items[0].DetailsURL,
	} {
		if strings.Contains(value, apiKey) || strings.Contains(value, rssKey) {
			t.Errorf("%s leaked a synthetic secret: %q", name, value)
		}
	}
	if len(sample.Items) != 1 {
		t.Fatalf("sample items = %d, want 1", len(sample.Items))
	}
	if sample.Items[0].Fields["infohash"] != true || sample.Items[0].Fields["seeders"] != true {
		t.Errorf("sample fields = %+v", sample.Items[0].Fields)
	}
	if sample.BodyBytes < int64(engine.ObservationBodyLimit) {
		t.Errorf("body bytes = %d, want the observed prefix", sample.BodyBytes)
	}

	// The raw response is truncated before it is stored: no second huge row.
	var stored int
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT LENGTH(body) FROM indexer_samples WHERE indexer_id = ?`,
		indexerID).Scan(&stored); err != nil {
		t.Fatalf("read stored length: %v", err)
	}
	if stored > engine.ObservationBodyLimit {
		t.Errorf("stored body = %d bytes, limit %d", stored, engine.ObservationBodyLimit)
	}
}

func TestRunCountsParseFailures(t *testing.T) {
	hash := strings.Repeat("f", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<html><body><table class="results"><tbody>
<tr>
  <td class="name"><a href="/details/1">Good Release</a></td>
  <td><a class="download" href="/download/1.torrent">DL</a></td>
  <td class="hash">` + hash + `</td>
  <td><a class="cat" href="/browse?cat=5">TV</a></td>
  <td class="size">1 GB</td>
  <td class="seeders">5</td>
  <td class="date">02-01-2026 15:04</td>
</tr>
<tr>
  <td class="name"></td>
  <td><a class="download" href="/download/2.torrent">DL</a></td>
  <td class="hash"></td>
  <td><a class="cat" href="/browse?cat=5">TV</a></td>
  <td class="size">1 GB</td>
  <td class="seeders">5</td>
  <td class="date">02-01-2026 15:04</td>
</tr>
</tbody></table></body></html>`))
	}))
	t.Cleanup(server.Close)

	st := newTestStore(t)
	v := newTestVault(t, 1)
	manager := newTestManager(t, st, v)
	seedDefinition(t, st, "activity-site", "", activityDefinition(server.URL))
	config := fmt.Sprintf(`{"url":%q,"query":"release"}`, server.URL)
	indexerID := createIndexer(t, st, "activity-site", "activity-site", "Activity Site", "torrent", config)
	putSecret(t, st, v, indexerID, "api_key", "key")

	stats, err := manager.Run(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Seen != 1 || stats.New != 1 {
		t.Errorf("Seen = %d, New = %d", stats.Seen, stats.New)
	}
	if stats.DiscardParse != 1 {
		t.Errorf("DiscardParse = %d, want 1", stats.DiscardParse)
	}
}
