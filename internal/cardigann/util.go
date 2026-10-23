package cardigann

import (
	"net/url"
	"sort"
	"strings"
)

// slugify is the same normalisation the rest of indexarr uses for ids and
// setting names: lowercase ASCII letters and digits, single dashes between
// runs of other characters.
func slugify(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func sortStrings(values []string) {
	sort.Strings(values)
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// isSecretName mirrors the native format's definition of a sensitive setting:
// the name or type suggests a password, token, API key, cookie or passkey.
func isSecretName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.NewReplacer("_", "", "-", "", " ", "", ".", "").Replace(normalized)
	switch normalized {
	case "apikey", "password", "passwd", "pass", "passkey", "rsskey",
		"pwd", "passphrase", "cookie", "cookies", "token", "secret",
		"authkey", "credentials", "session", "sessionid", "bearer":
		return true
	}
	for _, marker := range []string{
		"password", "apikey", "passkey", "rsskey", "token", "secret",
		"cookie", "credential", "authkey", "accesskey",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// isCSRFField reports a hidden token that must be extracted from the login
// page. A bare "token" is intentionally not included: it is ambiguous with a
// user-supplied API token, so it stays a setting unless the source names it
// csrf/xsrf/authenticity/nonce, a verification token, or "_token".
func isCSRFField(name string) bool {
	raw := strings.ToLower(strings.TrimSpace(name))
	normalized := strings.NewReplacer("_", "", "-", "", " ", "", ".", "").Replace(raw)
	for _, marker := range []string{
		"csrf", "xsrf", "authenticity", "nonce", "verificationtoken",
		"securitytoken", "formtoken",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	if normalized == "token" && strings.HasPrefix(raw, "_") {
		return true
	}
	return false
}

// isCSRFLiteralField also recognises _token/token when the source definition
// stores a literal value for it. That value is single-use, so extracting it is
// safer than copying it into the converted definition.
func isCSRFLiteralField(name string) bool {
	if isCSRFField(name) {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.NewReplacer("_", "", "-", "", " ", "").Replace(normalized)
	return normalized == "token"
}

// isLoginCredentialName covers literal usernames and account identifiers in
// login.inputs. They are not secrets in the vault sense, but they must not be
// copied from a local Cardigann file into a shared definition.
func isLoginCredentialName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.NewReplacer("_", "", "-", "", " ", "").Replace(normalized)
	switch normalized {
	case "username", "user", "userid", "email", "account":
		return true
	default:
		return false
	}
}

// redactSecrets removes known secret literals (and their URL-escaped forms)
// from text. Longer values go first so a secret containing another one is
// removed whole.
func redactSecrets(text string, secrets ...string) string {
	unique := make(map[string]bool, len(secrets)*3)
	var values []string
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, candidate := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if candidate == "" || unique[candidate] {
				continue
			}
			unique[candidate] = true
			values = append(values, candidate)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	return text
}

// sanitizeURL removes userinfo, query and fragment so a URL is safe in a
// warning. Tracker base URLs are not secret, but embedded credentials are.
func sanitizeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "[invalid URL]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func escapeCSSAttr(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
