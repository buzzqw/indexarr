package web

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// newOpaqueToken returns an unguessable handle for a short-lived server-side
// form state. The browser receives only the token, never the stored values.
func newOpaqueToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
