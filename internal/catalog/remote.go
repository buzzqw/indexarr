package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
)

// A definition is data: the catalog is a directory of YAML files plus the
// generated index.json. Shipping a fix for one site used to mean a new binary,
// because the catalog is embedded, so a tracker that changed its HTML waited
// for the next release (or for the operator to paste the YAML by hand).
//
// Import reads the same index.json and the same definitions/<id>.yaml files
// from an address the operator chooses — the repository, a mirror, a release
// asset — validates every document with the same parser the engine uses, and
// stores it with the "remote" origin. The embedded catalog never overwrites a
// remote definition, and a remote one is labelled as such in the interface.

// Import bounds one remote catalog read.
const (
	// ImportMaxDefinitions bounds how many documents one call may write: a
	// hostile or broken index must not turn into an unbounded download.
	ImportMaxDefinitions = 300
	// importMaxIndexBytes bounds the manifest, importMaxDefinitionBytes one
	// document.
	importMaxIndexBytes      = 4 << 20
	importMaxDefinitionBytes = 1 << 20
	// ImportTimeout bounds one document read; the whole call is bounded by the
	// caller's context.
	ImportTimeout = 20 * time.Second
)

// ImportResult reports what a remote catalog read did.
type ImportResult struct {
	// Entries is how many definitions the manifest lists.
	Entries int `json:"entries"`
	// Written is how many documents were stored (new or changed).
	Written int `json:"written"`
	// Unchanged counts the entries whose stored document already matches.
	Unchanged int `json:"unchanged"`
	// Local counts the entries skipped because the operator owns them.
	Local int `json:"local"`
	// Skipped counts the entries the manifest marks as dead ("morta") or that
	// carry no usable state: there is nothing to install for them.
	Skipped int `json:"skipped"`
	// Problems lists the rejected entries (invalid YAML, wrong id, fetch
	// error), shortened and bounded so a response stays readable.
	Problems []string `json:"problems,omitempty"`
}

// Import reads a catalog from rawBase (the directory that holds index.json and
// definitions/) and stores its definitions, skipping the ones the operator
// created or edited. An invalid document is reported and skipped: one broken
// YAML must not stop the rest of the catalog.
func Import(ctx context.Context, st *store.Store, rawBase string) (ImportResult, error) {
	var result ImportResult
	if st == nil {
		return result, errors.New("catalog: nil store")
	}
	base, err := importBaseURL(rawBase)
	if err != nil {
		return result, err
	}
	client := &http.Client{Timeout: ImportTimeout}

	indexBody, err := importGet(ctx, client, base+"/index.json", importMaxIndexBytes)
	if err != nil {
		return result, fmt.Errorf("catalog: read index.json: %w", err)
	}
	var entries []IndexEntry
	if err := json.Unmarshal(indexBody, &entries); err != nil {
		return result, fmt.Errorf("catalog: parse index.json: %w", err)
	}
	if len(entries) > ImportMaxDefinitions {
		entries = entries[:ImportMaxDefinitions]
	}
	result.Entries = len(entries)

	existing, err := st.ListDefinitions(ctx)
	if err != nil {
		return result, fmt.Errorf("catalog: list definitions: %w", err)
	}
	byID := make(map[string]store.Definition, len(existing))
	for _, def := range existing {
		byID[def.ID] = def
	}

	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if !importSafeID(id) {
			result.Problems = appendProblem(result.Problems, id, "invalid id")
			continue
		}
		if current, ok := byID[id]; ok {
			if current.Origin == OriginLocal {
				result.Local++
				continue
			}
		}
		state, usable := importState(entry.State)
		if !usable {
			result.Skipped++
			continue
		}
		body, err := importGet(ctx, client, base+"/definitions/"+id+".yaml", importMaxDefinitionBytes)
		if err != nil {
			result.Problems = appendProblem(result.Problems, id, err.Error())
			continue
		}
		parsed, err := definition.Parse(body)
		if err != nil {
			result.Problems = appendProblem(result.Problems, id, "invalid definition: "+err.Error())
			continue
		}
		if parsed.Meta.ID != id {
			result.Problems = appendProblem(result.Problems, id, "the document declares id "+parsed.Meta.ID)
			continue
		}
		hash := definition.Fingerprint(body)
		if current, ok := byID[id]; ok && current.Hash == hash && current.Origin == OriginRemote {
			result.Unchanged++
			continue
		}
		if err := st.UpsertDefinition(ctx, store.Definition{
			ID:            id,
			SchemaVersion: parsed.Schema,
			Family:        parsed.Connector,
			State:         state,
			Origin:        OriginRemote,
			Author:        parsed.Meta.Author,
			Body:          string(body),
			Hash:          hash,
		}); err != nil {
			result.Problems = appendProblem(result.Problems, id, err.Error())
			continue
		}
		result.Written++
	}
	return result, nil
}

// importState maps the state of the catalog to the state of a definition. The
// two vocabularies differ: the catalog says whether the *site* was verified
// ("da-provvi", "bloccata", "morta"), the definition says whether the document
// may be offered ("bozza", "verificata", "stabile", "degradata"). A site that
// was not verified here becomes a draft, a blocked one becomes degraded, and a
// dead one is not installed at all.
func importState(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "verificata", "stabile", "degradata", "bozza":
		return strings.ToLower(strings.TrimSpace(raw)), true
	case "da-provvi":
		return "bozza", true
	case "bloccata":
		return "degradata", true
	case "morta":
		return "", false
	default:
		return "bozza", true
	}
}

// importBaseURL validates the address of a catalog directory.
func importBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("catalog: url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("catalog: url must be an absolute http or https address")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("catalog: url must be an absolute http or https address")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// importSafeID keeps an id from the manifest inside definitions/: it becomes a
// path segment, so a parent reference or a slash must never reach the request.
func importSafeID(id string) bool {
	if id == "" || len(id) > 100 || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// importGet reads one bounded document.
func importGet(ctx context.Context, client *http.Client, target string, limit int64) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, ImportTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json, application/yaml, text/yaml, text/plain")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("the document is too large")
	}
	return body, nil
}

// appendProblem keeps the report readable: the first ten problems are enough
// to see what is wrong with a catalog, and a bounded list cannot flood a
// response.
func appendProblem(problems []string, id, reason string) []string {
	if len(problems) >= 10 {
		return problems
	}
	reason = strings.Join(strings.Fields(reason), " ")
	if len(reason) > 200 {
		reason = reason[:200] + "…"
	}
	return append(problems, id+": "+reason)
}
