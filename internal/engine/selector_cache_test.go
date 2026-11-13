package engine

import "testing"

func TestCompileSelectorCachesAndRejectsInvalid(t *testing.T) {
	for i := 0; i < 2; i++ {
		selector, err := compileSelector("div > a[href]")
		if err != nil {
			t.Fatalf("compileSelector: %v", err)
		}
		if selector == nil {
			t.Fatal("compileSelector returned nil")
		}
	}
	if _, err := compileSelector("div >"); err == nil {
		t.Fatal("an invalid selector was accepted")
	}
}
