package engine

import "testing"

func TestCompileRegexCachesAndRejectsInvalid(t *testing.T) {
	first, err := compileRegex(`(?i)^(.+?)\s+S(\d+)$`)
	if err != nil {
		t.Fatalf("compileRegex: %v", err)
	}
	second, err := compileRegex(`(?i)^(.+?)\s+S(\d+)$`)
	if err != nil {
		t.Fatalf("compileRegex again: %v", err)
	}
	if first != second {
		t.Fatal("the same pattern was compiled twice")
	}
	if _, err := compileRegex(`(`); err == nil {
		t.Fatal("an invalid pattern was accepted")
	}
}
