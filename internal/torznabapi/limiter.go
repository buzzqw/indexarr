package torznabapi

import (
	"sync"
	"time"
)

// minuteLimiter is a small per-key fixed-window limiter. The window is one
// minute; limit == 0 disables it. It is deliberately in memory: a restart
// resets the counters, which is acceptable for a personal indexer.
type minuteLimiter struct {
	mu      sync.Mutex
	windows map[string]limitWindow
}

type limitWindow struct {
	minute int64
	count  int
}

func newMinuteLimiter() *minuteLimiter {
	return &minuteLimiter{windows: make(map[string]limitWindow)}
}

func (l *minuteLimiter) allow(key string, limit int, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	minute := now.Unix() / 60
	l.mu.Lock()
	defer l.mu.Unlock()
	window := l.windows[key]
	if window.minute != minute {
		window = limitWindow{minute: minute}
	}
	if window.count >= limit {
		l.windows[key] = window
		return false
	}
	window.count++
	l.windows[key] = window
	if len(l.windows) > 4096 {
		for storedKey, stored := range l.windows {
			if stored.minute < minute-1 {
				delete(l.windows, storedKey)
			}
		}
	}
	return true
}
