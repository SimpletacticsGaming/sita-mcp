package auth

import (
	"sync"
	"time"
)

// sweepInterval is how often expired limiter entries are dropped.
const sweepInterval = time.Minute

// Limiter blocks a key (client IP or username) after too many failed logins.
// Failures are counted in a fixed window that starts with the first failure;
// once the maximum is reached the key is blocked until the window ends.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu        sync.Mutex
	entries   map[string]*failures
	lastSweep time.Time
}

type failures struct {
	count int
	first time.Time
}

// NewLimiter allows maxFailures failed logins per key within window.
func NewLimiter(maxFailures int, window time.Duration) *Limiter {
	return &Limiter{max: maxFailures, window: window, now: time.Now, entries: map[string]*failures{}}
}

// Blocked reports whether key is blocked and for how long.
func (l *Limiter) Blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	f, ok := l.entries[key]
	if !ok {
		return false, 0
	}
	if now.Sub(f.first) >= l.window {
		delete(l.entries, key)
		return false, 0
	}
	if f.count < l.max {
		return false, 0
	}
	return true, f.first.Add(l.window).Sub(now)
}

// Fail records a failed login for key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	f, ok := l.entries[key]
	if !ok || now.Sub(f.first) >= l.window {
		l.entries[key] = &failures{count: 1, first: now}
		return
	}
	f.count++
}

// Reset forgets the failures of key.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// sweep drops expired entries so the map does not grow without bound.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = now
	for k, f := range l.entries {
		if now.Sub(f.first) >= l.window {
			delete(l.entries, k)
		}
	}
}
