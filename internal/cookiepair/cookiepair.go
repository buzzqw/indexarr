// Package cookiepair hands a one-time token to the browser-cookie import
// command. The token lets the command on the browser machine replace one
// indexer's session cookie without exposing the API key or the UI password.
// Tokens live in memory only: a restart invalidates them, and each token is
// consumed by the first request that presents it.
package cookiepair

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// TTL is how long a generated command stays valid.
const TTL = 10 * time.Minute

type entry struct {
	indexerID int64
	expires   time.Time
}

var (
	mu     sync.Mutex
	tokens = map[string]entry{}
)

// Issue creates a one-time token for one indexer and returns it with its
// expiry. The token is 32 random bytes in URL-safe base64, long enough that a
// guessed token is impossible.
func Issue(indexerID int64) (string, time.Time, error) {
	return issue(indexerID, TTL)
}

func issue(indexerID int64, ttl time.Duration) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(ttl)
	mu.Lock()
	dropExpiredLocked(time.Now())
	tokens[token] = entry{indexerID: indexerID, expires: expires}
	mu.Unlock()
	return token, expires, nil
}

// Consume checks a token and removes it, whether it was valid or expired. It
// returns the indexer the token was issued for.
func Consume(token string) (int64, bool) {
	mu.Lock()
	defer mu.Unlock()
	found, ok := tokens[token]
	delete(tokens, token)
	if !ok || time.Now().After(found.expires) {
		return 0, false
	}
	return found.indexerID, true
}

// Reset drops every token. It is for tests.
func Reset() {
	mu.Lock()
	tokens = map[string]entry{}
	mu.Unlock()
}

func dropExpiredLocked(now time.Time) {
	for token, found := range tokens {
		if now.After(found.expires) {
			delete(tokens, token)
		}
	}
}
