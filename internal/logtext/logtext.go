// Package logtext turns raw error strings into a short problem code. The code
// is safe to log (it never carries hostnames or credentials) and the web
// interface turns it into a translated explanation with a suggested action.
package logtext

import "strings"

// Problem codes. The web catalog has one "logs.problem.<code>" and one
// "logs.advice.<code>" entry for each of them.
const (
	Timeout   = "timeout"
	Canceled  = "canceled"
	Refused   = "refused"
	DNS       = "dns"
	TLS       = "tls"
	Blocked   = "blocked"
	Auth      = "auth"
	RateLimit = "rate"
	Server    = "server"
	NotFound  = "notfound"
	Reset     = "reset"
	Parse     = "parse"
	DBBusy    = "dbbusy"
	Database  = "database"
	Vault     = "vault"
	Queue     = "queue"
	Disk      = "disk"
)

// rules are checked in order: the first match wins, so the more specific
// patterns come before the generic ones ("context deadline" before "context").
var rules = []struct {
	code     string
	patterns []string
}{
	{Queue, []string{"queue full", "waiting for a slot"}},
	{Blocked, []string{"cloudflare", "challenge", "just a moment", "ddos-guard", "captcha", "403", "forbidden", "flaresolverr"}},
	{RateLimit, []string{"429", "too many requests", "rate limit", "grab limit", "query limit"}},
	{Auth, []string{"401", "unauthorized", "login", "credential", "password", "not logged", "session expired", "authentication"}},
	{Timeout, []string{"deadline exceeded", "timeout", "timed out"}},
	{Canceled, []string{"context canceled", "operation was canceled"}},
	{Refused, []string{"connection refused"}},
	{DNS, []string{"no such host", "server misbehaving", "name resolution", "lookup "}},
	{TLS, []string{"x509", "certificate", "tls:", "tls handshake"}},
	{Reset, []string{"connection reset", "broken pipe", "unexpected eof", "eof", "connection closed", "network is unreachable", "no route to host"}},
	{Server, []string{"500", "502", "503", "504", "520", "521", "522", "523", "524", "bad gateway", "service unavailable", "internal server error", "gateway"}},
	{NotFound, []string{"404", "not found"}},
	{DBBusy, []string{"database is locked", "sqlite_busy", "database table is locked"}},
	{Disk, []string{"no space left", "disk full", "read-only file system", "permission denied"}},
	{Vault, []string{"vault", "decrypt", "cipher", "sealed", "master key"}},
	{Database, []string{"sqlite", "no such table", "no such column", "constraint failed", "sql:"}},
	{Parse, []string{"parse", "unmarshal", "invalid character", "xml syntax", "unexpected end", "selector", "no rows matched"}},
}

// Problem returns the problem code for an error text, or "" when the text
// does not match a known pattern.
func Problem(text string) string {
	raw := strings.ToLower(strings.TrimSpace(text))
	if raw == "" {
		return ""
	}
	for _, rule := range rules {
		for _, pattern := range rule.patterns {
			if strings.Contains(raw, pattern) {
				return rule.code
			}
		}
	}
	return ""
}

// ProblemOf is Problem for an error value.
func ProblemOf(err error) string {
	if err == nil {
		return ""
	}
	return Problem(err.Error())
}

// Codes lists every code, used by the catalog tests.
func Codes() []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, rule.code)
	}
	return out
}
