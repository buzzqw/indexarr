package indexer

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/vault"
)

// ErrDownloadRefUnavailable is returned when an opaque download reference
// cannot be sealed or opened because the vault is missing.
var ErrDownloadRefUnavailable = errors.New("indexer: vault required for download reference")

// DownloadRef is the sealed contents of release_sources.download_ref. It keeps
// the tracker download URL out of the database: only the indexer id and the
// item needed to rebuild the request are stored, encrypted by the vault.
type DownloadRef struct {
	IndexerID int64       `json:"indexer_id"`
	Item      engine.Item `json:"item"`
}

// SealDownloadRef serializes and encrypts one opaque download reference.
func SealDownloadRef(v *vault.Vault, indexerID int64, item engine.Item) (string, error) {
	if v == nil {
		return "", ErrDownloadRefUnavailable
	}
	if indexerID <= 0 {
		return "", errors.New("indexer: download reference without indexer")
	}
	encoded, err := json.Marshal(DownloadRef{IndexerID: indexerID, Item: item})
	if err != nil {
		return "", errors.New("indexer: encode download reference")
	}
	sealed, err := v.SealString(string(encoded))
	if err != nil {
		return "", errors.New("indexer: seal download reference")
	}
	return sealed, nil
}

// OpenDownloadRef decrypts and parses one opaque download reference.
func OpenDownloadRef(v *vault.Vault, ref string) (DownloadRef, error) {
	var out DownloadRef
	if v == nil {
		return out, ErrDownloadRefUnavailable
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return out, errors.New("indexer: empty download reference")
	}
	plain, err := v.OpenString(ref)
	if err != nil {
		return out, errors.New("indexer: open download reference")
	}
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return out, errors.New("indexer: decode download reference")
	}
	if out.IndexerID <= 0 {
		return out, errors.New("indexer: download reference without indexer")
	}
	return out, nil
}
