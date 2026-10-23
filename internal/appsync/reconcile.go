package appsync

import (
	"sort"
	"strings"
)

// ActionKind is the kind of change reconciliation proposes.
type ActionKind string

const (
	ActionAdd    ActionKind = "add"
	ActionUpdate ActionKind = "update"
	ActionDelete ActionKind = "delete"
)

// Action is one reconciliation step. RemoteID is set for update/delete and is
// zero for add.
type Action struct {
	Kind     ActionKind
	RemoteID int
	Spec     IndexerSpec
	Reason   string
}

// ReconcileOptions controls which actions are emitted.
type ReconcileOptions struct {
	SyncLevel string // disabled => no actions; add-remove => Add/Delete only; full => Add/Update/Delete
}

// ManagedPrefix is the naming convention that marks an indexer as owned by
// indexarr. Reconcile only deletes remote indexers whose name starts with this
// prefix; manually created indexers are left untouched even when they are not
// in the desired set. Indexarr creates its indexers with this prefix.
const ManagedPrefix = "indexarr · "

// Reconcile computes actions to bring remote in line with desired without
// performing any I/O.
//
// Matching is by Name first; when no name matches, an indexer is matched by
// the baseUrl field. API keys are never used for matching. An update is
// emitted only in full mode and only when RemoteIndexer.RawHash differs from
// SpecHash. Deletions are restricted to managed indexers and are sorted by
// name so repeated runs produce identical output.
func Reconcile(desired []IndexerSpec, remote []RemoteIndexer, opts ReconcileOptions) []Action {
	level := strings.ToLower(strings.TrimSpace(opts.SyncLevel))
	if level == "" || level == "disabled" {
		return nil
	}
	if level != "add-remove" && level != "full" {
		return nil
	}

	used := make([]bool, len(remote))
	matches := make([]int, len(desired))
	for i := range matches {
		matches[i] = -1
	}

	// First pass: match every desired indexer by name. This makes names win
	// over base URLs even when two desired specs could claim the same remote.
	for i, spec := range desired {
		for j := range remote {
			if used[j] || remote[j].Name != spec.Name {
				continue
			}
			matches[i] = j
			used[j] = true
			break
		}
	}

	// Second pass: fall back to the remote baseUrl field. API keys are never
	// used for matching.
	for i, spec := range desired {
		if matches[i] >= 0 || strings.TrimSpace(spec.BaseURL) == "" {
			continue
		}
		for j := range remote {
			if used[j] {
				continue
			}
			if !sameBaseURL(asString(remote[j].Fields["baseUrl"]), spec.BaseURL) {
				continue
			}
			matches[i] = j
			used[j] = true
			break
		}
	}

	actions := make([]Action, 0, len(desired))
	for i, spec := range desired {
		if matches[i] < 0 {
			actions = append(actions, Action{
				Kind:   ActionAdd,
				Spec:   spec,
				Reason: "indexer is missing from the application",
			})
			continue
		}
		if level != "full" {
			continue
		}
		matched := remote[matches[i]]
		if matched.RawHash == SpecHash(spec) {
			continue
		}
		actions = append(actions, Action{
			Kind:     ActionUpdate,
			RemoteID: matched.ID,
			Spec:     spec,
			Reason:   "indexer settings differ from the desired state",
		})
	}

	deletions := make([]RemoteIndexer, 0, len(remote))
	for j, item := range remote {
		if used[j] || !strings.HasPrefix(item.Name, ManagedPrefix) {
			continue
		}
		deletions = append(deletions, item)
	}
	sort.SliceStable(deletions, func(i, j int) bool {
		if deletions[i].Name != deletions[j].Name {
			return deletions[i].Name < deletions[j].Name
		}
		return deletions[i].ID < deletions[j].ID
	})
	for _, item := range deletions {
		actions = append(actions, Action{
			Kind:     ActionDelete,
			RemoteID: item.ID,
			Reason:   "managed indexer is no longer desired",
		})
	}
	return actions
}

func sameBaseURL(a, b string) bool {
	a = normalizeURLString(a)
	b = normalizeURLString(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return strings.EqualFold(a, b)
}
