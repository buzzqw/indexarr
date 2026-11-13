package web

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// loginFailureThreshold is the number of consecutive failed attempts
	// before the address is paused. The first four cost one PBKDF2 pass each,
	// which is what a mistyped password should cost.
	loginFailureThreshold = 5
	// loginFirstPause is the pause after the fifth failure. Each further
	// failure doubles it, up to loginMaxPause.
	loginFirstPause = 30 * time.Second
	loginMaxPause   = 15 * time.Minute
	// loginEntryTTL forgets an address that stopped trying, so a shared
	// address is not remembered forever and the map stays small.
	loginEntryTTL = 24 * time.Hour
	// loginMaxEntries bounds the map if many addresses fail.
	loginMaxEntries = 4096
)

// loginLimiter throttles failed Basic authentication attempts per client
// address. A success forgets the failures, so a valid password is never
// affected by an earlier typo on the same address.
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]loginAttempt
	now     func() time.Time
}

type loginAttempt struct {
	failures     int
	blockedUntil time.Time
	seen         time.Time
}

// retryAfter returns how long the address must wait before another password
// will be checked. Zero means it may try now.
func (l *loginLimiter) retryAfter(address string) time.Duration {
	if address == "" {
		return 0
	}
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[address]
	if !ok {
		return 0
	}
	if entry.blockedUntil.After(now) {
		return entry.blockedUntil.Sub(now)
	}
	return 0
}

// fail records one failed attempt and returns the pause it triggered, if any.
func (l *loginLimiter) fail(address string) time.Duration {
	if address == "" {
		return 0
	}
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]loginAttempt)
	}
	entry := l.entries[address]
	entry.failures++
	entry.seen = now
	pause := time.Duration(0)
	if entry.failures >= loginFailureThreshold {
		pause = loginFirstPause
		for i := loginFailureThreshold; i < entry.failures && pause < loginMaxPause; i++ {
			pause *= 2
		}
		if pause > loginMaxPause {
			pause = loginMaxPause
		}
		entry.blockedUntil = now.Add(pause)
	}
	l.entries[address] = entry
	l.pruneLocked(now)
	return pause
}

// success clears the failures of an address that authenticated correctly.
func (l *loginLimiter) success(address string) {
	if address == "" {
		return
	}
	l.mu.Lock()
	delete(l.entries, address)
	l.mu.Unlock()
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	if len(l.entries) < loginMaxEntries {
		return
	}
	for address, entry := range l.entries {
		if now.Sub(entry.seen) > loginEntryTTL && !entry.blockedUntil.After(now) {
			delete(l.entries, address)
		}
	}
	// A flood of unique addresses can still fill the map: drop arbitrary
	// expired-looking entries until it is back under the bound.
	for address := range l.entries {
		if len(l.entries) < loginMaxEntries {
			break
		}
		delete(l.entries, address)
	}
}

func (l *loginLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// writeLoginRetry answers a paused address with 429 and Retry-After, so a
// scripted client slows down instead of mining passwords through PBKDF2.
func writeLoginRetry(w http.ResponseWriter, wait time.Duration) {
	seconds := int((wait + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	w.Header().Set("WWW-Authenticate", `Basic realm="indexarr"`)
	http.Error(w, "too many failed authentication attempts", http.StatusTooManyRequests)
}

// clientAddress returns the address used as the limiter key. Only the
// connection address is trusted: X-Forwarded-For is client-controlled unless
// the deployment adds a trusted proxy, which indexarr does not configure.
func clientAddress(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}
