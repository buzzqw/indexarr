package web

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/logtext"
)

// logView is one log entry as the user reads it: a translated sentence that
// says what happened, and for problems what went wrong and what to do. The raw
// message and attributes stay available as technical details.
type logView struct {
	logbuffer.Entry
	Area      string
	AreaKey   string
	LevelText string
	Sentence  string
	Problem   string
	Advice    string
	ErrorText string
	Details   []logDetail
}

// logDetail is one attribute with a translated label.
type logDetail struct {
	Key   string
	Label string
	Value string
}

// logSpec maps a raw log message to its catalog entry. The sentence lives at
// logs.msg.<id>[.<variant>] and the area label at logs.area.<area>.
type logSpec struct {
	id      string
	area    string
	variant func(map[string]string) string
}

// logMessages covers every message the process writes at Info or above, plus
// the Debug lines that explain a skipped or queued search. Messages missing
// from the table fall back to logPrefixes and then to the raw text.
var logMessages = map[string]logSpec{
	"starting indexarr":                    {id: "start.begin", area: "start"},
	"memory limit":                         {id: "start.memory", area: "start"},
	"secrets vault ready":                  {id: "start.vault", area: "security"},
	"web interface ready":                  {id: "start.web", area: "start"},
	"shutting down":                        {id: "start.stopping", area: "start"},
	"stopped":                              {id: "start.stopped", area: "start"},
	"HTTP server stopped":                  {id: "start.httpStopped", area: "start"},
	"HTTP shutdown":                        {id: "start.httpShutdown", area: "start"},
	"pprof endpoint enabled":               {id: "start.pprof", area: "start"},
	"pprof server stopped":                 {id: "start.pprofStopped", area: "start"},
	"admin API disabled":                   {id: "start.adminOff", area: "start"},
	"app sync disabled":                    {id: "start.syncOff", area: "start"},
	"download proxy disabled":              {id: "start.proxyOff", area: "start"},
	"health checker disabled":              {id: "start.healthOff", area: "start"},
	"native scheduler disabled":            {id: "start.schedulerOff", area: "start"},
	"native indexer runtime disabled":      {id: "start.nativeOff", area: "start"},
	"native indexer pages disabled":        {id: "start.nativePagesOff", area: "start"},
	"cannot register family connectors":    {id: "start.familiesOff", area: "start"},
	"cannot load settings; using defaults": {id: "start.settingsDefault", area: "start"},
	"generated a Torznab API key":          {id: "start.apikey", area: "security"},
	"public_url is empty; app sync will advertise a local address":                                               {id: "start.publicURL", area: "apps"},
	"secrets vault unavailable; native indexers and sealed source secrets are disabled":                          {id: "start.vaultMissing", area: "security"},
	"vault key is in the data directory: move it outside, for example with 'indexarr vault move-key -to <path>'": {id: "start.vaultInData", area: "security"},

	"built-in catalog synchronized":          {id: "catalog.synced", area: "catalog"},
	"cannot sync the built-in catalog":       {id: "catalog.syncFailed", area: "catalog"},
	"cannot pair known feed/search channels": {id: "catalog.pairFailed", area: "catalog"},
	"paired feed/search channels":            {id: "catalog.paired", area: "catalog"},
	"load catalog for definitions page":      {id: "catalog.pageFailed", area: "catalog"},
	"definition state changed":               {id: "catalog.defState", area: "catalog"},
	"cannot update definition state":         {id: "catalog.defStateFailed", area: "catalog"},

	"database restored from pending backup":                                     {id: "backup.restored", area: "backup"},
	"cannot apply pending restore":                                              {id: "backup.restoreFailed", area: "backup"},
	"cannot restart for restore":                                                {id: "backup.restartFailed", area: "backup"},
	"restored database: no sealed secrets to verify":                            {id: "backup.noSecrets", area: "backup"},
	"restored database: the current vault key does not open the stored secrets": {id: "backup.wrongKey", area: "backup"},
	"restored database: vault key opens the stored secrets":                     {id: "backup.keyOK", area: "backup"},
	"restore staged; restarting to apply it":                                    {id: "backup.staged", area: "backup"},
	"automatic backup created":                                                  {id: "backup.auto", area: "backup"},
	"manual backup created":                                                     {id: "backup.manual", area: "backup"},
	"automatic backup failed":                                                   {id: "backup.autoFailed", area: "backup"},
	"backup rotation failed":                                                    {id: "backup.rotateFailed", area: "backup"},
	"old backups removed":                                                       {id: "backup.rotated", area: "backup"},

	"cannot move the notification secrets into the vault": {id: "security.notifyMoveFailed", area: "security"},
	"notification secrets moved into the vault":           {id: "security.notifyMoved", area: "security"},
	"additional API key created":                          {id: "security.apiKey", area: "security"},

	"log level changed": {id: "web.level", area: "web"},
	"render page":       {id: "web.render", area: "web"},
	"render partial":    {id: "web.renderPartial", area: "web"},

	"cannot import the legacy download client":                            {id: "clients.legacyFailed", area: "clients"},
	"cannot import the legacy download client: secrets vault unavailable": {id: "clients.legacyVault", area: "clients"},
	"legacy download client imported into App":                            {id: "clients.legacyOK", area: "clients"},
	"download client test failed":                                         {id: "clients.testFailed", area: "clients"},
	"release sent to download client":                                     {id: "clients.sent", area: "clients"},
	"send to download client failed":                                      {id: "clients.sendFailed", area: "clients"},

	"FlareSolverr test failed":                 {id: "network.flareFailed", area: "network"},
	"FlareSolverr test ok":                     {id: "network.flareOK", area: "network"},
	"FlareSolverr test returned an HTTP error": {id: "network.flareHTTP", area: "network"},

	"test notification failed":                 {id: "notify.testFailed", area: "notify"},
	"test notification sent":                   {id: "notify.testOK", area: "notify"},
	"notification failed":                      {id: "notify.failed", area: "notify"},
	"cannot record the notification timestamp": {id: "notify.stampFailed", area: "notify"},

	"native: scheduler started":                             {id: "native.started", area: "native"},
	"native: scheduler stopped":                             {id: "native.stopped", area: "native"},
	"native: run complete":                                  {id: "native.run", area: "native", variant: nativeRunVariant},
	"native: indexer run complete":                          {id: "native.indexerDone", area: "native"},
	"native: indexer run failed":                            {id: "native.indexerFailed", area: "native"},
	"native: indexer skipped":                               {id: "native.indexerSkipped", area: "native"},
	"native: indexer auto-disabled after repeated failures": {id: "native.autoDisabled", area: "native"},
	"native: indexer test failed":                           {id: "native.testFailed", area: "native"},
	"native: indexer test ok":                               {id: "native.testOK", area: "native"},
	"native: testing indexers":                              {id: "native.testing", area: "native"},
	"native: test pass finished":                            {id: "native.testDone", area: "native"},
	"native: latest capability":                             {id: "native.capability", area: "native"},
	"native: list indexers":                                 {id: "native.list", area: "native"},
	"native: load retry states":                             {id: "native.retry", area: "native"},
	"native: load settings, using the default interval":     {id: "native.settings", area: "native"},
	"native: read the notified state":                       {id: "native.notifyRead", area: "native"},
	"native: store the notified state":                      {id: "native.notifyStore", area: "native"},
	"seeders refresh":                                       {id: "native.seeders", area: "native"},
	"seeders refresh: list candidates":                      {id: "native.seedersFailed", area: "native"},

	"activity retention cleanup":         {id: "maintenance.activity", area: "maintenance"},
	"activity retention cleanup failed":  {id: "maintenance.activityFailed", area: "maintenance"},
	"retention prune":                    {id: "maintenance.prune", area: "maintenance"},
	"retention prune failed":             {id: "maintenance.pruneFailed", area: "maintenance"},
	"stored retention prune":             {id: "maintenance.storedPrune", area: "maintenance"},
	"stored retention prune failed":      {id: "maintenance.storedPruneFailed", area: "maintenance"},
	"database optimize failed":           {id: "maintenance.optimizeFailed", area: "maintenance"},
	"dedupe: duplicates merged":          {id: "maintenance.dedupe", area: "maintenance"},
	"dedupe: cleanup failed":             {id: "maintenance.dedupeFailed", area: "maintenance"},
	"dedupe: infohash enrichment failed": {id: "maintenance.enrichFailed", area: "maintenance"},
	"indexer: infohash enrichment":       {id: "maintenance.enrich", area: "maintenance"},
	"file-name backfill: read releases":  {id: "maintenance.filesRead", area: "maintenance"},
	"file-name backfill: store files":    {id: "maintenance.filesStore", area: "maintenance"},
	"maintenance: source download stats": {id: "maintenance.downloadStats", area: "maintenance"},

	"health checker started": {id: "health.started", area: "health"},
	"health checker stopped": {id: "health.stopped", area: "health"},
	"health check failed":    {id: "health.checkFailed", area: "health"},
	"health pass failed":     {id: "health.passFailed", area: "health"},

	"syncjob: application synced":        {id: "apps.synced", area: "apps"},
	"syncjob: application sync failed":   {id: "apps.syncFailed", area: "apps"},
	"syncjob: application restored":      {id: "apps.restored", area: "apps"},
	"syncjob: list applications":         {id: "apps.list", area: "apps"},
	"syncjob: record application result": {id: "apps.record", area: "apps"},
	"application restore failed":         {id: "apps.restoreFailed", area: "apps"},
	"manual application snapshot failed": {id: "apps.snapshotFailed", area: "apps"},
	"manual application sync failed":     {id: "apps.manualSyncFailed", area: "apps"},
	"create application":                 {id: "apps.create", area: "apps"},
	"update application":                 {id: "apps.update", area: "apps"},
	"delete application":                 {id: "apps.delete", area: "apps"},
	"seal application api key":           {id: "apps.seal", area: "apps"},

	"indexer import failed":            {id: "sources.importFailed", area: "sources"},
	"indexer import skipped":           {id: "sources.importSkipped", area: "sources"},
	"feed discovery done":              {id: "sources.discovered", area: "sources"},
	"feed discovery failed":            {id: "sources.discoverFailed", area: "sources"},
	"indexer: torrent download failed": {id: "sources.torrentFailed", area: "sources"},
	"indexer: usenet download failed":  {id: "sources.nzbFailed", area: "sources"},

	"torznab: live search":                                             {id: "search.live", area: "search"},
	"torznab: record app search":                                       {id: "search.record", area: "search"},
	"torznab: lookup API key":                                          {id: "search.apikey", area: "search"},
	"torznab: update API key usage":                                    {id: "search.apikeyUsage", area: "search"},
	"torznab: mark downloaded":                                         {id: "download.mark", area: "download"},
	"torznab: record grab":                                             {id: "download.record", area: "download"},
	"torznab: resolve grab indexer":                                    {id: "download.resolve", area: "download"},
	"torznab: resolve release payload":                                 {id: "download.payload", area: "download"},
	"torznab: write payload":                                           {id: "download.write", area: "download"},
	"live search: queue full":                                          {id: "search.queueFull", area: "search"},
	"live search: cannot load blocklist":                               {id: "search.blocklist", area: "search"},
	"live search: provenance":                                          {id: "search.provenance", area: "search"},
	"live search: indexer deleted during search; provenance discarded": {id: "search.deleted", area: "search"},
	"live search: seal download reference":                             {id: "search.seal", area: "search"},
	"live stats: cannot read recent searches":                          {id: "search.stats", area: "search"},
}

// logPrefixes give a sentence to the internal-error families. The rest of the
// message after the prefix is shown as the technical step ({op}).
var logPrefixes = []struct {
	prefix string
	id     string
	area   string
}{
	{"adminapi: ", "admin.failed", "admin"},
	{"torznab: ", "search.internal", "search"},
	{"indexer: ", "sources.internal", "sources"},
	{"native: ", "native.internal", "native"},
	{"syncjob: ", "apps.internal", "apps"},
}

// logAreas lists every area, used by the catalog tests.
var logAreas = []string{
	"start", "security", "catalog", "backup", "web", "clients", "network",
	"notify", "native", "maintenance", "health", "apps", "sources", "search",
	"download", "admin", "other",
}

// logDetailHidden are attributes already shown elsewhere in the row.
var logDetailHidden = map[string]bool{"component": true, "subsystem": true}

func nativeRunVariant(attrs map[string]string) string {
	if attrs["indexers"] == "0" {
		return "none"
	}
	if _, ok := attrs["sources"]; !ok {
		return "count"
	}
	failed := attrs["failed"] != "" && attrs["failed"] != "0"
	switch {
	case failed && (attrs["ran"] == "" || attrs["ran"] == "0"):
		return "failedAll"
	case failed:
		return "failed"
	case attrs["new"] == "" || attrs["new"] == "0":
		return "quiet"
	default:
		return "ok"
	}
}

// logViews converts buffer entries for one request. names maps indexer slugs
// to display names; it may be nil.
func logViews(lang i18n.Language, entries []logbuffer.Entry, names map[string]string) []logView {
	out := make([]logView, 0, len(entries))
	for _, entry := range entries {
		out = append(out, newLogView(lang, entry, names))
	}
	return out
}

func newLogView(lang i18n.Language, entry logbuffer.Entry, names map[string]string) logView {
	attrs := make(map[string]string, len(entry.Attrs))
	for _, attr := range entry.Attrs {
		attrs[attr.Key] = attr.Value
	}
	view := logView{Entry: entry, LevelText: i18n.T(lang, "logs.levels."+levelKey(entry.Level))}

	spec, op, known := lookupLogSpec(entry.Message)
	area := spec.area
	if area == "" {
		area = "other"
	}
	view.AreaKey = area
	view.Area = i18n.T(lang, "logs.area."+area)

	formatted := make(map[string]string, len(attrs)+1)
	for key, value := range attrs {
		formatted[key] = formatLogValue(lang, key, value, names)
	}
	formatted["op"] = op
	if known {
		key := "logs.msg." + spec.id
		if spec.variant != nil {
			key += "." + spec.variant(attrs)
		}
		view.Sentence = fillLogTemplate(i18n.T(lang, key), formatted)
	} else {
		view.Sentence = capitalize(entry.Message)
	}

	errText := strings.TrimSpace(attrs["error"])
	problem := strings.TrimSpace(attrs["problem"])
	if problem == "" && errText != "" {
		problem = logtext.Problem(errText)
	}
	if problem == "" && attrs["status"] != "" && entry.Level >= slog.LevelWarn {
		problem = logtext.Problem("HTTP " + attrs["status"])
	}
	switch {
	case problem != "":
		view.Problem = i18n.T(lang, "logs.problem."+problem)
		view.Advice = i18n.T(lang, "logs.advice."+problem)
	case errText != "":
		view.Problem = i18n.T(lang, "logs.problem.unknown")
		view.Advice = i18n.T(lang, "logs.advice.unknown")
	case entry.Level >= slog.LevelWarn:
		view.Advice = i18n.T(lang, "logs.advice.repeat")
	}
	view.ErrorText = errText

	for _, attr := range entry.Attrs {
		if logDetailHidden[attr.Key] || attr.Key == "error" {
			continue
		}
		label := i18n.T(lang, "logs.attr."+attr.Key)
		if label == "logs.attr."+attr.Key {
			label = attr.Key
		}
		view.Details = append(view.Details, logDetail{Key: attr.Key, Label: label, Value: formatted[attr.Key]})
	}
	return view
}

func lookupLogSpec(message string) (logSpec, string, bool) {
	if spec, ok := logMessages[message]; ok {
		return spec, "", true
	}
	for _, family := range logPrefixes {
		if rest, ok := strings.CutPrefix(message, family.prefix); ok {
			return logSpec{id: family.id, area: family.area}, rest, true
		}
	}
	return logSpec{}, "", false
}

func levelKey(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "error"
	case level >= slog.LevelWarn:
		return "warn"
	case level >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}

// fillLogTemplate replaces {name} placeholders. A missing attribute becomes
// "—" so an old entry written before an attribute existed still reads.
func fillLogTemplate(text string, values map[string]string) string {
	var b strings.Builder
	for {
		start := strings.IndexByte(text, '{')
		if start < 0 {
			b.WriteString(text)
			break
		}
		end := strings.IndexByte(text[start:], '}')
		if end < 0 {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:start])
		name := text[start+1 : start+end]
		if value := strings.TrimSpace(values[name]); value != "" {
			b.WriteString(value)
		} else {
			b.WriteString("—")
		}
		text = text[start+end+1:]
	}
	return b.String()
}

func formatLogValue(lang i18n.Language, key, value string, names map[string]string) string {
	value = strings.TrimSpace(value)
	switch key {
	case "interval", "tick", "ttl":
		if d, err := time.ParseDuration(value); err == nil {
			return humanLogDuration(lang, d)
		}
	case "duration_ms":
		if ms, err := strconv.ParseInt(value, 10, 64); err == nil {
			return formatLatency(ms)
		}
	case "bytes":
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			return humanBytes(n)
		}
	case "indexer", "source", "indexer_slug":
		if name := names[strings.ToLower(value)]; name != "" {
			return name
		}
	case "key_in_data_dir":
		switch value {
		case "true":
			return i18n.T(lang, "logs.value.yes")
		case "false":
			return i18n.T(lang, "logs.value.no")
		}
	case "problem":
		if value != "" {
			return i18n.T(lang, "logs.problemShort."+value)
		}
	}
	return value
}

// humanLogDuration writes 1h0m0s as "1 h" and 90s as "1 min 30 s".
func humanLogDuration(lang i18n.Language, d time.Duration) string {
	if d <= 0 {
		return "0 s"
	}
	if d%(24*time.Hour) == 0 {
		return i18n.T(lang, "logs.unit.days", int(d/(24*time.Hour)))
	}
	var parts []string
	if h := int(d / time.Hour); h > 0 {
		parts = append(parts, fmt.Sprintf("%d h", h))
	}
	if m := int(d % time.Hour / time.Minute); m > 0 {
		parts = append(parts, fmt.Sprintf("%d min", m))
	}
	if s := int(d % time.Minute / time.Second); s > 0 {
		parts = append(parts, fmt.Sprintf("%d s", s))
	}
	if len(parts) == 0 {
		return d.String()
	}
	return strings.Join(parts, " ")
}

func capitalize(text string) string {
	r, size := utf8.DecodeRuneInString(text)
	if r == utf8.RuneError {
		return text
	}
	return string(unicode.ToUpper(r)) + text[size:]
}

// indexerNames maps slug and name (lower case) to the display name, so log
// lines that only carry the slug show the name the user chose.
func (s *Server) indexerNames(ctx context.Context) map[string]string {
	rows, err := s.store.ListIndexers(ctx)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.Slug != "" {
			out[strings.ToLower(row.Slug)] = row.Name
		}
	}
	return out
}
