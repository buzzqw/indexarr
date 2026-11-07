// Package upkeep runs the routine maintenance of the database when indexarr is
// idle: integrity checks, duplicate merge, cleanup of rows no client can
// download, index upkeep and compaction. Each step is idempotent and cheap on
// a clean database, so a daily pass costs a few seconds.
package upkeep

import (
	"net/http"
	"sync/atomic"
	"time"
)

// Activity remembers when indexarr last served someone: an app or a client
// through Torznab, Newznab, the admin API or a download, or a person who
// changed something in the web UI. Pages that are only read do not count, so
// a browser tab left open on the dashboard does not keep indexarr "busy"
// forever.
type Activity struct {
	last atomic.Int64
}

// NewActivity starts as if someone had just been served: a fresh start waits
// one full idle window before any maintenance.
func NewActivity() *Activity {
	a := &Activity{}
	a.Touch()
	return a
}

// Touch records activity now. It is safe on a nil Activity.
func (a *Activity) Touch() {
	if a == nil {
		return
	}
	a.last.Store(time.Now().UnixNano())
}

// LastAt is the time of the last recorded activity.
func (a *Activity) LastAt() time.Time {
	if a == nil {
		return time.Time{}
	}
	return time.Unix(0, a.last.Load())
}

// IdleFor is how long nobody has been served.
func (a *Activity) IdleFor(now time.Time) time.Duration {
	if a == nil {
		return 0
	}
	return now.Sub(a.LastAt())
}

// Wrap records activity for every request served by next. It is meant for the
// machine endpoints (Torznab, API, downloads), where every request is a real
// client.
func (a *Activity) Wrap(next http.Handler) http.Handler {
	if a == nil || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.Touch()
		next.ServeHTTP(w, r)
	})
}

// WrapChanges records activity only for requests that change something
// (anything but GET, HEAD and OPTIONS). It is meant for the web UI, where
// reading a page is not work that maintenance should wait for.
func (a *Activity) WrapChanges(next http.Handler) http.Handler {
	if a == nil || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			a.Touch()
		}
		next.ServeHTTP(w, r)
	})
}
