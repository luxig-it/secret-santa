package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Failed organizer logins allowed per client address within the window.
const (
	loginFailures = 10
	loginWindow   = 15 * time.Minute
)

// limiter counts failures per key in a fixed window. It is deliberately
// tiny: one instance, one map, swept lazily, which is plenty for a login
// form that only a handful of organizers ever use.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	hits   map[string]*window
}

type window struct {
	start time.Time
	count int
}

func newLimiter(max int, d time.Duration) *limiter {
	return &limiter{max: max, window: d, now: time.Now, hits: map[string]*window{}}
}

// allow reports whether key may try again.
func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.current(key)
	return w == nil || w.count < l.max
}

// fail records a failed attempt.
// take checks and records one use in a single step, so parallel requests
// cannot all pass before any of them counts.
func (l *limiter) take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.current(key)
	if w == nil {
		l.hits[key] = &window{start: l.now(), count: 1}
		return true
	}
	if w.count >= l.max {
		return false
	}
	w.count++
	return true
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := l.current(key); w != nil {
		w.count++
		return
	}
	l.hits[key] = &window{start: l.now(), count: 1}
}

func (l *limiter) current(key string) *window {
	now := l.now()
	if len(l.hits) > 10_000 {
		for k, w := range l.hits {
			if now.Sub(w.start) > l.window {
				delete(l.hits, k)
			}
		}
	}
	w := l.hits[key]
	if w == nil || now.Sub(w.start) > l.window {
		delete(l.hits, key)
		return nil
	}
	return w
}

// clientIP is the address the request came from. Forwarding headers are
// ignored on purpose: without a trusted proxy configuration they are set
// by the client and would let anybody pick a fresh identity per request.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
