package cardigann

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var corpusLinePattern = regexp.MustCompile(`line \d+: `)

// TestCorpus is an optional measurement helper. It runs the converter over a
// directory of Cardigann definitions only when CARDIGANN_CORPUS is set, so
// normal `go test ./internal/cardigann` stays corpus-free. The corpus is a
// read-only measurement input: it is never committed, copied or asserted on.
func TestCorpus(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("CARDIGANN_CORPUS"))
	if dir == "" {
		t.Skip("CARDIGANN_CORPUS is not set")
	}

	results, errs := ConvertDirectory(context.Background(), dir, Options{})
	t.Logf("converted=%d failed=%d", len(results), len(errs))

	if os.Getenv("CARDIGANN_CORPUS_VERBOSE") != "" {
		for _, err := range errs {
			t.Logf("FAIL %v", err)
		}
	}

	reasons := make(map[string]int)
	for _, err := range errs {
		message := err.Error()
		if index := strings.Index(message, ": "); index >= 0 {
			message = message[index+2:]
		}
		// Collapse line numbers and quoted values so the histogram is useful.
		message = collapseCorpusReason(message)
		reasons[message]++
	}
	type entry struct {
		reason string
		count  int
	}
	entries := make([]entry, 0, len(reasons))
	for reason, count := range reasons {
		entries = append(entries, entry{reason, count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].reason < entries[j].reason
	})
	for _, item := range entries {
		t.Logf("%4d  %s", item.count, item.reason)
	}

	if len(results)+len(errs) == 0 {
		t.Fatalf("no definitions found under %s", dir)
	}
}

func collapseCorpusReason(message string) string {
	// Keep the distinguishing wording, drop volatile details.
	if index := strings.Index(message, "unmarshal errors:"); index >= 0 {
		message = message[index:]
	}
	message = corpusLinePattern.ReplaceAllString(message, "line N: ")
	message = strings.Join(strings.Fields(message), " ")
	if index := strings.Index(message, "request template:"); index >= 0 {
		return message[index:]
	}
	if index := strings.Index(message, "optional template:"); index >= 0 {
		return message[index:]
	}
	return message
}
