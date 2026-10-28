package logtext

import (
	"errors"
	"testing"
)

func TestProblem(t *testing.T) {
	cases := map[string]string{
		"": "",
		"Get \"https://x\": context deadline exceeded":                                Timeout,
		"net/http: request canceled (Client.Timeout exceeded while awaiting headers)": Timeout,
		"context canceled": Canceled,
		`indexer ddunlimited: search: definition "ddunlimited": search deferred by the flood interval: the site allows one search every 1m5s; queued, the next identical request reads it from the cache`: Flood,
		`definition "ddunlimited": search deferred by the flood interval: the site allows one search every 1m5s and the queue is full`:                                                                    Flood,
		`definition "ddunlimited": search refused by the site: Al momento non ti è permesso fare ricerche`:                                                                                                Flood,
		"dial tcp 1.2.3.4:443: connect: connection refused":               Refused,
		"dial tcp: lookup example.invalid: no such host":                  DNS,
		"x509: certificate has expired":                                   TLS,
		"HTTP 403: Just a moment...":                                      Blocked,
		"engine: challenge page detected":                                 Blocked,
		"HTTP 429 Too Many Requests":                                      RateLimit,
		"login failed: invalid password":                                  Auth,
		"HTTP 502 Bad Gateway":                                            Server,
		"HTTP 404":                                                        NotFound,
		"read: connection reset by peer":                                  Reset,
		"database is locked":                                              DBBusy,
		"live queue full":                                                 Queue,
		`indexer tg: fetch: request failed: engine: circuit breaker open`: Breaker,
		`engine: breaker "tg": engine: circuit breaker open`:              Breaker,
		"something odd":                                                   "",
	}
	for input, want := range cases {
		if got := Problem(input); got != want {
			t.Errorf("Problem(%q) = %q, want %q", input, got, want)
		}
	}
	if ProblemOf(nil) != "" || ProblemOf(errors.New("i/o timeout")) != Timeout {
		t.Error("ProblemOf does not follow Problem")
	}
}

func TestCodesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, code := range Codes() {
		if seen[code] {
			t.Errorf("duplicate code %q", code)
		}
		seen[code] = true
	}
}
