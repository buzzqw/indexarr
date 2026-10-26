package definition

import (
	"bytes"
	"os"
	"testing"
)

func TestParseSharedReusesAndRefreshes(t *testing.T) {
	body, err := os.ReadFile("../../examples/definitions/newznab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	first, err := ParseShared(body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseShared(body)
	if err != nil || second != first {
		t.Fatalf("second parse = %p, %v; want the cached %p", second, err, first)
	}
	edited := append(bytes.Clone(body), []byte("\n# edited\n")...)
	third, err := ParseShared(edited)
	if err != nil || third == first {
		t.Fatalf("edited body returned the stale parse (%v)", err)
	}
	if _, err := ParseShared([]byte("  ")); err == nil {
		t.Fatal("empty document accepted")
	}
}
