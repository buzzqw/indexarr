package web

import "testing"

func TestVerifiedLoginsCachesOnlySuccess(t *testing.T) {
	encoded, err := hashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	var cache verifiedLogins
	if cache.verify("admin", "wrong", encoded) {
		t.Fatal("wrong password accepted")
	}
	if len(cache.seen) != 0 {
		t.Fatal("a failed login was cached")
	}
	if !cache.verify("admin", "secret", encoded) || !cache.verify("admin", "secret", encoded) {
		t.Fatal("right password rejected")
	}
	if len(cache.seen) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(cache.seen))
	}
	changed, err := hashPassword("other")
	if err != nil {
		t.Fatal(err)
	}
	if cache.verify("admin", "secret", changed) {
		t.Fatal("old password accepted after a password change")
	}
}
