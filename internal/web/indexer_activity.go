package web

import (
	"fmt"
	"html"
	htmltemplate "html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/torznabapi"
)

// dataChartDays is the window of the per-run chart in the Data tab.
const dataChartDays = 7

// completenessView is one field of the last run: how many normalized items
// carried it out of the total, and therefore how much Sonarr can rely on it.
type completenessView struct {
	Key     string
	Label   string
	Found   int64
	Total   int64
	Percent int64
	Missing bool
}

// sampleItemView is one normalized item of the last sample, with the fields
// the site supplied and the fields it omitted already resolved into labels.
type sampleItemView struct {
	Item       store.SampleItem
	Found      string
	Missing    string
	Fields     []sampleFieldView
	Categories []string
}

// sampleFieldView is one "found/missing" cell of a normalized sample item.
type sampleFieldView struct {
	Label string
	Found bool
}

// indexerSampleView is the last sample with the request, the raw response and
// the first normalized items. RequestURL and Body are redacted again on the
// way out, so a stored sample cannot expose a credential even if it was
// written by an older caller.
type indexerSampleView struct {
	RequestURL string
	Method     string
	Status     int
	LatencyMS  int64
	Body       string
	BodyBytes  int64
	Truncated  bool
	FetchedAt  time.Time
	ItemsCount int
	Items      []sampleItemView
}

// indexerDataView backs the Data tab of one indexer.
type indexerDataView struct {
	Runs          []store.RunMetricRow
	Chart         htmltemplate.HTML
	HasChart      bool
	Completeness  []completenessView
	Problems      []uiProblem
	HasMetrics    bool
	Sample        *indexerSampleView
	HasSample     bool
	ChartNewLabel string
	ChartOldLabel string
}

// indexerTryView is the result of one "Try as Sonarr/Radarr" run.
type indexerTryView struct {
	Mode     string
	Query    string
	Season   int
	Episode  int
	IMDbID   string
	TVDbID   string
	Ran      bool
	Status   int
	Duration string
	Total    int64
	Releases []store.Release
	XML      string
	Err      string
}

// buildIndexerData loads the run metrics, the 7-day series and the last sample
// of one indexer. Every query is bounded server-side.
func (s *Server) buildIndexerData(r *http.Request, indexerID int64) (*indexerDataView, error) {
	data := &indexerDataView{
		ChartNewLabel: s.translate(r, "idx.data.chartNew"),
		ChartOldLabel: s.translate(r, "idx.data.chartDiscarded"),
	}
	rows, err := s.store.ListRunMetrics(r.Context(), indexerID, 20)
	if err != nil {
		return nil, err
	}
	data.Runs = rows

	now := time.Now()
	since := now.Add(-dataChartDays * 24 * time.Hour)
	series, err := s.store.RunSeries(r.Context(), indexerID, since)
	if err != nil {
		return nil, err
	}
	if len(series) > 0 {
		data.Chart = renderRunChartWindow(series, since, now)
		data.HasChart = true
	}

	var metrics *store.RunMetrics
	for i := range rows {
		if rows[i].Metrics.FieldTotal > 0 {
			metrics = &rows[i].Metrics
			break
		}
	}
	if metrics != nil {
		data.HasMetrics = true
		data.Completeness = s.completeness(r, *metrics)
		for _, field := range data.Completeness {
			if !field.Missing {
				continue
			}
			data.Problems = append(data.Problems, uiProblem{
				Text:        s.missingFieldPhrase(r, field.Key),
				ActionLabel: s.translate(r, "common.edit"),
				ActionURL:   "/indexers/" + strconv.FormatInt(indexerID, 10) + "/edit",
			})
		}
	}

	sample, found, err := s.store.GetIndexerSample(r.Context(), indexerID)
	if err != nil {
		return nil, err
	}
	if found {
		data.HasSample = true
		data.Sample = s.sampleView(r, sample)
	}
	return data, nil
}

func (s *Server) completeness(r *http.Request, metrics store.RunMetrics) []completenessView {
	fields := []struct {
		key   string
		label string
		field int64
	}{
		{"size", s.translate(r, "idx.data.fieldSize"), metrics.FieldSize},
		{"seeders", s.translate(r, "idx.data.fieldSeeders"), metrics.FieldSeeders},
		{"category", s.translate(r, "idx.data.fieldCategory"), metrics.FieldCategory},
		{"date", s.translate(r, "idx.data.fieldDate"), metrics.FieldDate},
		{"infohash", s.translate(r, "idx.data.fieldHash"), metrics.FieldHash},
	}
	out := make([]completenessView, 0, len(fields))
	for _, field := range fields {
		percent := metrics.Percent(field.field)
		out = append(out, completenessView{
			Key: field.key, Label: field.label, Found: field.field,
			Total: metrics.FieldTotal, Percent: percent, Missing: percent < 50,
		})
	}
	return out
}

func (s *Server) missingFieldPhrase(r *http.Request, key string) string {
	switch key {
	case "size":
		return s.translate(r, "idx.data.problemSize")
	case "seeders":
		return s.translate(r, "idx.data.problemSeeders")
	case "category":
		return s.translate(r, "idx.data.problemCategory")
	case "date":
		return s.translate(r, "idx.data.problemDate")
	default:
		return s.translate(r, "idx.data.problemHash")
	}
}

func (s *Server) sampleView(r *http.Request, sample store.IndexerSample) *indexerSampleView {
	view := &indexerSampleView{
		RequestURL: feed.RedactURL(sample.RequestURL),
		Method:     sample.Method,
		Status:     sample.Status,
		LatencyMS:  sample.LatencyMS,
		Body:       feed.RedactText(sample.Body, nil),
		BodyBytes:  sample.BodyBytes,
		Truncated:  sample.Truncated,
		FetchedAt:  sample.FetchedAt,
		ItemsCount: sample.ItemsCount,
	}
	fieldLabels := []struct {
		key   string
		label string
	}{
		{"size", s.translate(r, "idx.data.fieldSize")},
		{"seeders", s.translate(r, "idx.data.fieldSeeders")},
		{"category", s.translate(r, "idx.data.fieldCategory")},
		{"date", s.translate(r, "idx.data.fieldDate")},
		{"infohash", s.translate(r, "idx.data.fieldHash")},
	}
	for _, raw := range sample.Items {
		item := raw
		item.Title = feed.RedactText(item.Title, nil)
		item.GUID = feed.RedactText(item.GUID, nil)
		item.DetailsURL = feed.RedactText(item.DetailsURL, nil)
		item.DownloadURL = feed.RedactText(item.DownloadURL, nil)
		item.Magnet = feed.RedactText(item.Magnet, nil)
		item.InfoHash = strings.TrimSpace(item.InfoHash)
		viewItem := sampleItemView{Item: item}
		var found, missing []string
		for _, field := range fieldLabels {
			ok := item.Fields[field.key]
			viewItem.Fields = append(viewItem.Fields, sampleFieldView{Label: field.label, Found: ok})
			if ok {
				found = append(found, field.label)
			} else {
				missing = append(missing, field.label)
			}
		}
		viewItem.Found = strings.Join(found, ", ")
		viewItem.Missing = strings.Join(missing, ", ")
		for _, id := range item.Categories {
			viewItem.Categories = append(viewItem.Categories, s.categoryLabel(r, id))
		}
		view.Items = append(view.Items, viewItem)
	}
	return view
}

// buildIndexerTry runs the exact /api search path through torznabapi.Try.
func (s *Server) buildIndexerTry(r *http.Request, in store.Indexer) (*indexerTryView, error) {
	q := r.URL.Query()
	view := &indexerTryView{
		Mode:    strings.ToLower(strings.TrimSpace(q.Get("mode"))),
		Query:   strings.TrimSpace(q.Get("q")),
		Season:  intParam(q.Get("season"), 0),
		Episode: intParam(q.Get("ep"), 0),
		IMDbID:  strings.TrimSpace(q.Get("imdbid")),
		TVDbID:  strings.TrimSpace(q.Get("tvdbid")),
		Ran:     true,
	}
	if view.Mode == "" {
		view.Mode = "search"
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		return nil, err
	}
	result, err := s.torznab.Try(r.Context(), torznabapi.BaseURL(r, settings), torznabapi.TryRequest{
		IndexerSlug: in.Slug,
		Mode:        view.Mode,
		Query:       view.Query,
		Season:      view.Season,
		Episode:     view.Episode,
		IMDbID:      view.IMDbID,
		TVDbID:      view.TVDbID,
	})
	if err != nil {
		view.Err = err.Error()
		return view, nil
	}
	view.Status = result.Status
	view.Duration = durationLabel(result.Duration)
	view.Total = result.Total
	view.Releases = result.Releases
	view.XML = string(result.XML)
	view.Err = result.Err
	return view, nil
}

func durationLabel(value time.Duration) string {
	if value < time.Millisecond {
		return value.String()
	}
	return value.Round(time.Millisecond).String()
}

// renderRunChart draws the 7-day new/discarded series as inline SVG. It uses
// no library and no client-side code; the server writes every rectangle.
func renderRunChart(points []store.RunSeriesPoint) htmltemplate.HTML {
	now := time.Now()
	return renderRunChartWindow(points, now.Add(-dataChartDays*24*time.Hour), now)
}

// renderRunChartWindow positions every run on a real time axis: two runs an
// hour apart stay close together even when the series also contains a run six
// days later. Uniform spacing (one slot per run) hid long gaps and made a
// single burst look like a steady schedule.
func renderRunChartWindow(points []store.RunSeriesPoint, start, end time.Time) htmltemplate.HTML {
	if len(points) == 0 {
		return ""
	}
	const width = 720
	const height = 180
	const left = 8
	const right = 8
	const top = 12
	const bottom = 22
	plotWidth := width - left - right
	plotHeight := height - top - bottom
	const barWidth = 2.5

	max := int64(1)
	for _, point := range points {
		if point.New > max {
			max = point.New
		}
		if point.Discarded > max {
			max = point.Discarded
		}
	}
	span := end.Sub(start)
	if span <= 0 {
		span = time.Second
	}
	// Leave room for the pair of bars at both ends.
	usable := float64(plotWidth) - barWidth*2
	if usable < 1 {
		usable = 1
	}
	var b strings.Builder
	b.WriteString(`<svg class="run-chart" viewBox="0 0 `)
	b.WriteString(strconv.Itoa(width))
	b.WriteString(` `)
	b.WriteString(strconv.Itoa(height))
	b.WriteString(`" role="img" preserveAspectRatio="none">`)
	fmt.Fprintf(&b, `<line class="chart-axis" x1="%d" y1="%d" x2="%d" y2="%d"/>`,
		left, top+plotHeight, width-right, top+plotHeight)
	for _, point := range points {
		ratio := float64(point.StartedAt.Sub(start)) / float64(span)
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		x := float64(left) + ratio*usable
		newHeight := float64(point.New) / float64(max) * float64(plotHeight)
		oldHeight := float64(point.Discarded) / float64(max) * float64(plotHeight)
		// A tick marks every run, including one that produced no new or
		// discarded item, so the chart never looks like the run is missing.
		fmt.Fprintf(&b, `<rect class="chart-tick" x="%.2f" y="%d" width="%.2f" height="1"/>`,
			x, top+plotHeight, barWidth)
		title := html.EscapeString(fmt.Sprintf("%s · %d/%d",
			point.StartedAt.Local().Format("2006-01-02 15:04"), point.New, point.Discarded))
		fmt.Fprintf(&b, `<rect class="chart-new" x="%.2f" y="%.2f" width="%.2f" height="%.2f"><title>%s</title></rect>`,
			x, float64(top+plotHeight)-newHeight, barWidth, newHeight, title)
		fmt.Fprintf(&b, `<rect class="chart-old" x="%.2f" y="%.2f" width="%.2f" height="%.2f"><title>%s</title></rect>`,
			x+barWidth, float64(top+plotHeight)-oldHeight, barWidth, oldHeight, title)
	}
	b.WriteString(`</svg>`)
	return htmltemplate.HTML(b.String())
}
