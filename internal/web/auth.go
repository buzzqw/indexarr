package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"crypto/pbkdf2"
)

const (
	passwordIterations = 210000
	passwordScheme     = "pbkdf2-sha256"
)

// hashPassword derives a salted PBKDF2-SHA256 hash in the
// pbkdf2-sha256$iterations$salt$hash format. The standard library package keeps
// indexarr free of external authentication dependencies.
func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", fmt.Errorf("derive password: %w", err)
	}
	return strings.Join([]string{
		passwordScheme,
		strconv.Itoa(passwordIterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

// verifyPassword checks a password against the stored hash in constant time.
func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordScheme {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 10000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// verifiedLogins remembers Basic credentials that already passed PBKDF2. The
// browser resends them on every request, page asset and poll; deriving 210k
// iterations each time cost tens of milliseconds of CPU per request. Only
// successes are cached, so a wrong password still pays the full derivation.
// The key includes the stored hash: changing the password invalidates it.
type verifiedLogins struct {
	mu   sync.Mutex
	seen map[[32]byte]struct{}
}

const verifiedLoginsMax = 32

func (v *verifiedLogins) verify(user, password, encoded string) bool {
	h := sha256.New()
	for _, part := range []string{user, password, encoded} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	var key [32]byte
	copy(key[:], h.Sum(nil))
	v.mu.Lock()
	_, ok := v.seen[key]
	v.mu.Unlock()
	if ok {
		return true
	}
	if !verifyPassword(encoded, password) {
		return false
	}
	v.mu.Lock()
	if v.seen == nil || len(v.seen) >= verifiedLoginsMax {
		v.seen = make(map[[32]byte]struct{}, verifiedLoginsMax)
	}
	v.seen[key] = struct{}{}
	v.mu.Unlock()
	return true
}

type contextKey string

const csrfContextKey contextKey = "csrf"

// csrfToken returns the per-browser token installed by the middleware.
func csrfToken(ctx context.Context) string {
	token, _ := ctx.Value(csrfContextKey).(string)
	return token
}

func newCSRFToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
