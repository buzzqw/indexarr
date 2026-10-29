package cookiepair

import (
	"testing"
	"time"
)

func TestIssueConsumeOnce(t *testing.T) {
	Reset()
	token, expires, err := Issue(7)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || time.Until(expires) <= 0 {
		t.Fatalf("token = %q, expires = %v", token, expires)
	}
	if id, ok := Consume(token); !ok || id != 7 {
		t.Fatalf("Consume = %d, %v", id, ok)
	}
	if _, ok := Consume(token); ok {
		t.Error("a token was consumed twice")
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	Reset()
	token, _, err := issue(3, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Consume(token); ok {
		t.Error("an expired token was accepted")
	}
	if _, ok := Consume("never-issued"); ok {
		t.Error("an unknown token was accepted")
	}
}

func TestIssueDropsExpiredTokens(t *testing.T) {
	Reset()
	if _, _, err := issue(1, -time.Second); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Issue(2); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	count := len(tokens)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("tokens = %d, want only the fresh one", count)
	}
}
