package engine

import "testing"

// TestNormalizeInfoHashDecodesTheDoubleHexForm covers the infohash some
// indexers hex-encode twice (Jackett issue #17045): without decoding it the
// hash is discarded and the same release appears again with a new identity.
func TestNormalizeInfoHashDecodesTheDoubleHexForm(t *testing.T) {
	const inner = "234493f324ff4fcbf35c7dfea71f3b962cf5e016"
	const doubled = "32333434393366333234666634666362663335633764666561373166336239363263663565303136"
	if got := normalizeInfoHash(doubled); got != inner {
		t.Fatalf("normalizeInfoHash(double hex) = %q, want %q", got, inner)
	}
	if got := normalizeInfoHash("not-a-hash"); got != "" {
		t.Fatalf("normalizeInfoHash(not-a-hash) = %q, want empty", got)
	}
}
