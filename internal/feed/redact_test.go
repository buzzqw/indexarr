package feed

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	input := `Get "http://host:9696/api/v1/search?query=x&apikey=0123456789abcdef&passkey=deadbeef": dial tcp: timeout`
	got := redactSecrets(input)
	for _, secret := range []string{"0123456789abcdef", "deadbeef"} {
		if strings.Contains(got, secret) {
			t.Errorf("secret %q leaked in %q", secret, got)
		}
	}
	if !strings.Contains(got, "apikey=***") || !strings.Contains(got, "passkey=***") {
		t.Errorf("secrets not marked: %q", got)
	}
	// A message without credentials is unchanged.
	plain := "HTTP 500: internal error"
	if got := redactSecrets(plain); got != plain {
		t.Errorf("plain message changed: %q", got)
	}
}
