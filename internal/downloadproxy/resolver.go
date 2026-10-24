// Package downloadproxy resolves opaque release download references. The
// tracker URL, which usually carries a passkey, never leaves the vault: the
// API hands clients a /dl/{id}.torrent or /dl/{id}.nzb link and this package
// rebuilds the real request only when the bytes are needed.
package downloadproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexer"
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
)

// Config configures a Resolver.
type Config struct {
	Store   *store.Store
	Vault   *vault.Vault
	Manager *indexer.Manager
}

// Resolver serves release payloads, fetching through the indexer manager when
// only an opaque reference is stored.
type Resolver struct {
	store   *store.Store
	vault   *vault.Vault
	manager *indexer.Manager
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
	return &Resolver{store: cfg.Store, vault: cfg.Vault, manager: cfg.Manager}, nil
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
	for _, source := range sources {
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
			lastErr = downloadErr
			continue
		}
		if len(fetched.Body) == 0 {
			if magnet := strings.TrimSpace(fetched.Magnet); magnet != "" {
				return Payload{Magnet: magnet}, nil
			}
			continue
		}

		protocol := protocolFor(parsed.Item, fetched.ContentType)
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
		_ = r.store.SavePayload(ctx, releaseID, fetched.Body, contentType, protocol)
		// Record which copy actually worked, so the merged release keeps a
		// history of successful fallbacks.
		_ = r.store.RecordSourceGrab(ctx, releaseID, indexerID)

		filename := strings.TrimSpace(fetched.Filename)
		if filename == "" {
			filename = Filename(release.Title, protocol)
		}
		return Payload{Body: fetched.Body, ContentType: contentType, Filename: filename}, nil
	}

	if lastErr != nil {
		return Payload{}, fmt.Errorf("release %d: %w: %v", releaseID, ErrUnavailable, lastErr)
	}
	return Payload{}, fmt.Errorf("release %d: %w", releaseID, ErrUnavailable)
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
