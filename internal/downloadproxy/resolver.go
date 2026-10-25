// Package downloadproxy resolves opaque release download references. The
// tracker URL, which usually carries a passkey, never leaves the vault: the
// API hands clients a /dl/{id}.torrent or /dl/{id}.nzb link and this package
// rebuilds the real request only when the bytes are needed.
package downloadproxy

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/notify"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

var (
	// ErrStoreRequired is returned by New without a store.
	ErrStoreRequired = errors.New("downloadproxy: store is required")
	// ErrManagerRequired is returned by New without an indexer manager.
	ErrManagerRequired = errors.New("downloadproxy: indexer manager is required")
	// ErrNotFound reports an unknown release id.
	ErrNotFound = errors.New("downloadproxy: release not found")
	// ErrUnavailable reports that no stored payload or usable reference
	// exists. Its text never includes a tracker URL or a secret.
	ErrUnavailable = errors.New("downloadproxy: download unavailable")
	ErrLogin       = errors.New("downloadproxy: login required")
	ErrBlocked     = errors.New("downloadproxy: download blocked")
	ErrTemporary   = errors.New("downloadproxy: download temporarily unavailable")
	ErrQuarantined = errors.New("downloadproxy: download quarantined")
)

// Error is a classified download failure. Its message is safe to show in the
// release card: it never contains a tracker URL or a credential.
type Error struct {
	Kind       error
	Message    string
	RetryAfter time.Time
	status     int
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		return e.Kind.Error()
	}
	return e.Kind.Error() + ": " + message
}

func (e *Error) Unwrap() error { return e.Kind }

// HTTPStatus maps a classified error to the status the /dl endpoint returns.
func (e *Error) HTTPStatus() int {
	if e == nil {
		return http.StatusBadGateway
	}
	if e.status > 0 {
		return e.status
	}
	switch {
	case errors.Is(e.Kind, ErrLogin):
		return http.StatusUnauthorized
	case errors.Is(e.Kind, ErrBlocked):
		return http.StatusForbidden
	case errors.Is(e.Kind, ErrQuarantined):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func classifyDownloadError(err error) *Error {
	if err == nil {
		return nil
	}
	var classified *Error
	if errors.As(err, &classified) {
		return classified
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "challenge"), strings.Contains(lower, "captcha"),
		strings.Contains(lower, "cloudflare"), strings.Contains(lower, "blocked"):
		return &Error{Kind: ErrBlocked, Message: "site blocked the download"}
	case strings.Contains(lower, "login"), strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "forbidden"), strings.Contains(lower, "http 401"),
		strings.Contains(lower, "http 403"):
		return &Error{Kind: ErrLogin, Message: "site requires a login"}
	case strings.Contains(lower, "404"), strings.Contains(lower, "410"),
		strings.Contains(lower, "not found"), strings.Contains(lower, "no magnet"):
		return &Error{Kind: ErrTemporary, Message: "no magnet or .torrent available"}
	default:
		return &Error{Kind: ErrTemporary, Message: "temporary download error"}
	}
}

// ValidateHTMLBody rejects a login/challenge/error page even when it was
// already stored by an older version.
func ValidateHTMLBody(body []byte) error {
	if looksLikeHTML(body) {
		return htmlError(body)
	}
	return nil
}

func looksLikeHTML(body []byte) bool {
	if len(body) > 512 {
		body = body[:512]
	}
	trimmed := strings.TrimSpace(strings.ToLower(string(body)))
	return strings.HasPrefix(trimmed, "<!doctype html") || strings.HasPrefix(trimmed, "<html") ||
		strings.HasPrefix(trimmed, "<head") || strings.HasPrefix(trimmed, "<body")
}

func htmlError(body []byte) *Error {
	if len(body) > 4096 {
		body = body[:4096]
	}
	lower := strings.ToLower(string(body))
	switch {
	case strings.Contains(lower, "login"), strings.Contains(lower, "sign in"),
		strings.Contains(lower, "password"), strings.Contains(lower, "unauthorized"):
		return &Error{Kind: ErrLogin, Message: "site returned a login page"}
	default:
		return &Error{Kind: ErrBlocked, Message: "site returned an HTML page instead of a torrent"}
	}
}

// Config configures a Resolver.
type Config struct {
	Store   *store.Store
	Vault   *vault.Vault
	Manager *indexer.Manager
	// Notifier reports a grab that failed on every copy. It is optional.
	Notifier *notify.Dispatcher
}

// Resolver serves release payloads, fetching through the indexer manager when
// only an opaque reference is stored.
type Resolver struct {
	store    *store.Store
	vault    *vault.Vault
	manager  *indexer.Manager
	notifier *notify.Dispatcher
}

// New builds a Resolver. The vault may be nil, but then opaque references
// cannot be opened; stored payloads and magnets still resolve.
func New(cfg Config) (*Resolver, error) {
	if cfg.Store == nil {
		return nil, ErrStoreRequired
	}
	if cfg.Manager == nil {
		return nil, ErrManagerRequired
	}
	return &Resolver{store: cfg.Store, vault: cfg.Vault, manager: cfg.Manager, notifier: cfg.Notifier}, nil
}

// Payload is the downloadable form of a release: bytes plus how to serve them,
// or a magnet when the tracker answered with a redirect.
type Payload struct {
	Body        []byte
	ContentType string
	Filename    string
	Magnet      string
}

// Resolve returns the payload of one release. Stored bytes are served locally;
// otherwise each opaque reference is opened and downloaded through the
// indexer, and the fetched body is persisted for the next request.
func (r *Resolver) Resolve(ctx context.Context, releaseID int64) (Payload, error) {
	release, err := r.store.GetRelease(ctx, releaseID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Payload{}, ErrNotFound
		}
		return Payload{}, ErrUnavailable
	}

	if body, contentType, protocol, payloadErr := r.store.PayloadByReleaseID(ctx, releaseID); payloadErr == nil && len(body) > 0 {
		if err := ValidateHTMLBody(body); err != nil {
			return Payload{}, err
		}
		return Payload{
			Body:        body,
			ContentType: contentType,
			Filename:    Filename(release.Title, protocol),
		}, nil
	} else if payloadErr != nil && !errors.Is(payloadErr, store.ErrNoPayload) {
		return Payload{}, ErrUnavailable
	}

	if strings.TrimSpace(release.Magnet) != "" {
		return Payload{Magnet: release.Magnet}, nil
	}

	sources, err := r.store.ReleaseSourcesFor(ctx, releaseID)
	if err != nil {
		return Payload{}, ErrUnavailable
	}
	// Try the healthiest copy first. When the chosen copy fails (404, HTML,
	// login) the loop falls through to the next source of the same merged
	// release, in the same grab.
	sort.SliceStable(sources, func(i, j int) bool {
		return sources[i].BetterThan(sources[j])
	})
	var lastErr error
	var earliestQuarantine time.Time
	for _, source := range sources {
		if source.Quarantined {
			if earliestQuarantine.IsZero() || source.QuarantinedUntil.Before(earliestQuarantine) {
				earliestQuarantine = source.QuarantinedUntil
			}
			continue
		}
		ref := strings.TrimSpace(source.DownloadRef)
		if ref == "" || r.vault == nil {
			continue
		}
		parsed, openErr := indexer.OpenDownloadRef(r.vault, ref)
		if openErr != nil {
			continue
		}
		indexerID := parsed.IndexerID
		if indexerID <= 0 {
			indexerID = source.IndexerID
		}
		fetched, downloadErr := r.manager.Download(ctx, indexerID, parsed.Item)
		if downloadErr != nil {
			if ctx.Err() != nil {
				return Payload{}, ctx.Err()
			}
			classified := classifyDownloadError(downloadErr)
			lastErr = classified
			r.recordSourceFailure(ctx, releaseID, source.IndexerID, classified)
			continue
		}
		if len(fetched.Body) == 0 {
			if magnet := strings.TrimSpace(fetched.Magnet); magnet != "" {
				r.clearSourceFailure(ctx, releaseID, source.IndexerID)
				return Payload{Magnet: magnet}, nil
			}
			continue
		}

		body := fetched.Body
		trimmedBody := strings.TrimSpace(string(body))
		if strings.HasPrefix(strings.ToLower(trimmedBody), "magnet:") {
			r.clearSourceFailure(ctx, releaseID, source.IndexerID)
			return Payload{Magnet: trimmedBody}, nil
		}
		if looksLikeHTML(body) {
			classified := htmlError(body)
			lastErr = classified
			r.recordSourceFailure(ctx, releaseID, source.IndexerID, classified)
			continue
		}

		protocol := protocolFor(parsed.Item, fetched.ContentType)
		if protocol == "torrent" {
			meta, metaErr := feed.ParseTorrent(body)
			if metaErr != nil {
				classified := &Error{Kind: ErrTemporary, Message: "downloaded file is not a torrent"}
				lastErr = classified
				r.recordSourceFailure(ctx, releaseID, source.IndexerID, classified)
				continue
			}
			if strings.TrimSpace(meta.InfoHash) != "" && strings.TrimSpace(release.InfoHash) == "" {
				if keeperID, _, mergeErr := r.store.SetReleaseInfoHash(ctx, releaseID, meta.InfoHash); mergeErr == nil && keeperID > 0 {
					releaseID = keeperID
				}
			}
			if strings.TrimSpace(fetched.Filename) == "" && strings.TrimSpace(meta.Name) != "" {
				fetched.Filename = SafeFilename(meta.Name, ".torrent")
			}
		}
		if protocol == "torrent" {
			if sourceIndexer, indexerErr := r.store.GetIndexer(ctx, indexerID); indexerErr == nil &&
				strings.EqualFold(strings.TrimSpace(sourceIndexer.Protocol), "usenet") {
				protocol = "usenet"
			}
		}
		contentType := strings.TrimSpace(fetched.ContentType)
		if contentType == "" {
			contentType = defaultContentType(protocol)
		}
		// Cache the bytes so the next request is local. A cache write failure
		// must not discard a payload the client can still use.
		_ = r.store.SavePayload(ctx, releaseID, body, contentType, protocol)
		if protocol == "torrent" {
			if meta, metaErr := feed.ParseTorrent(body); metaErr == nil && len(meta.Files) > 0 {
				files := make([]store.ReleaseFile, 0, len(meta.Files))
				for _, file := range meta.Files {
					files = append(files, store.ReleaseFile{Path: file.Path, Size: file.Size})
				}
				_ = r.store.ReplaceReleaseFiles(ctx, releaseID, files)
			}
		}
		// Record which copy actually worked, so the merged release keeps a
		// history of successful fallbacks.
		_ = r.store.RecordSourceGrab(ctx, releaseID, indexerID)
		r.clearSourceFailure(ctx, releaseID, source.IndexerID)

		filename := strings.TrimSpace(fetched.Filename)
		if filename == "" {
			filename = Filename(release.Title, protocol)
		}
		return Payload{Body: body, ContentType: contentType, Filename: filename}, nil
	}

	if lastErr != nil {
		classified := classifyDownloadError(lastErr)
		r.notifyGrabFailed(ctx, release, classified)
		return Payload{}, classified
	}
	if !earliestQuarantine.IsZero() {
		quarantined := &Error{Kind: ErrQuarantined, Message: "download link temporarily quarantined", RetryAfter: earliestQuarantine}
		r.notifyGrabFailed(ctx, release, quarantined)
		return Payload{}, quarantined
	}
	unavailable := &Error{Kind: ErrTemporary, Message: "download unavailable"}
	r.notifyGrabFailed(ctx, release, unavailable)
	return Payload{}, unavailable
}

// notifyGrabFailed reports a grab that failed on every copy of a merged
// release, after the fallback loop is exhausted. The event carries the title,
// the downstream application and the readable reason; the anti-spam window is
// keyed on the release, so a client retrying the same grab is not spammed.
func (r *Resolver) notifyGrabFailed(ctx context.Context, release store.Release, cause error) {
	if r == nil || r.notifier == nil {
		return
	}
	settings, err := r.store.LoadSettings(ctx)
	if err != nil {
		return
	}
	reason := ""
	if cause != nil {
		reason = cause.Error()
	}
	app := notify.AppFrom(ctx)
	if app == "" {
		app = "unknown"
	}
	r.notifier.DispatchAsync(ctx, settings, notify.Event{
		Kind:   notify.KindGrabFailed,
		Source: release.Title,
		Title:  release.Title,
		App:    app,
		Error:  reason,
		Path:   "/releases",
	})
}

func (r *Resolver) recordSourceFailure(ctx context.Context, releaseID, indexerID int64, err *Error) {
	if err == nil {
		return
	}
	if errors.Is(err.Kind, ErrTemporary) && strings.Contains(strings.ToLower(err.Message), "no magnet") {
		_, _, _ = r.store.RecordSourceDownloadFailure(ctx, releaseID, indexerID, err.Error())
		return
	}
	_ = r.store.RecordSourceDownloadNotice(ctx, releaseID, indexerID, err.Error())
}

func (r *Resolver) clearSourceFailure(ctx context.Context, releaseID, indexerID int64) {
	_ = r.store.ClearSourceDownloadFailure(ctx, releaseID, indexerID)
}

func protocolFor(item engine.Item, contentType string) string {
	if item.Protocol == engine.ProtocolUsenet ||
		strings.Contains(strings.ToLower(contentType), "nzb") {
		return "usenet"
	}
	return "torrent"
}

func defaultContentType(protocol string) string {
	if protocol == "usenet" {
		return "application/x-nzb"
	}
	return "application/x-bittorrent"
}

// Filename builds a safe local filename for a release payload.
func Filename(title, protocol string) string {
	extension := ".torrent"
	if strings.EqualFold(strings.TrimSpace(protocol), "usenet") {
		extension = ".nzb"
	}
	return SafeFilename(title, extension)
}

// SafeFilename replaces path separators and control characters in title and
// caps the result. It never returns an empty name.
func SafeFilename(title, extension string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_' || r == ' ':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" {
		name = "release"
	}
	runes := []rune(name)
	if len(runes) > 100 {
		name = string(runes[:100])
	}
	return name + extension
}
