package auth

import (
	"sync"
	"time"
)

type Limits struct {
	Burst      int
	Window     time.Duration
	Lockout    time.Duration
	MaxTracked int
}

func DefaultLimits() Limits {
	return Limits{
		Burst:      8,
		Window:     5 * time.Minute,
		Lockout:    15 * time.Minute,
		MaxTracked: 10000,
	}
}

type attempt struct {
	failures  int
	firstSeen time.Time
	lockedTil time.Time
}

type Limiter struct {
	mu       sync.Mutex
	limits   Limits
	attempts map[string]*attempt
	now      func() time.Time
}

func NewLimiter(limits Limits) *Limiter {
	if limits.Burst <= 0 {
		limits = DefaultLimits()
	}
	return &Limiter{
		limits:   limits,
		attempts: make(map[string]*attempt),
		now:      time.Now,
	}
}

func (l *Limiter) Allow(username, remoteAddr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	for _, key := range keysFor(username, remoteAddr) {
		entry, present := l.attempts[key]
		if !present {
			continue
		}
		if now.Before(entry.lockedTil) {
			return false
		}
		if now.Sub(entry.firstSeen) > l.limits.Window {
			delete(l.attempts, key)
		}
	}
	return true
}

func (l *Limiter) Fail(username, remoteAddr string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepLocked(now)

	for _, key := range keysFor(username, remoteAddr) {
		entry, present := l.attempts[key]
		if !present || now.Sub(entry.firstSeen) > l.limits.Window {
			entry = &attempt{firstSeen: now}
			l.attempts[key] = entry
		}
		entry.failures++
		if entry.failures >= l.limits.Burst {
			entry.lockedTil = now.Add(l.limits.Lockout)
		}
	}
}

func (l *Limiter) Succeed(username, remoteAddr string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, key := range keysFor(username, remoteAddr) {
		delete(l.attempts, key)
	}
}

func (l *Limiter) Tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.attempts)
}

func (l *Limiter) sweepLocked(now time.Time) {
	if len(l.attempts) < l.limits.MaxTracked {
		return
	}
	for key, entry := range l.attempts {
		if now.After(entry.lockedTil) && now.Sub(entry.firstSeen) > l.limits.Window {
			delete(l.attempts, key)
		}
	}
}

func keysFor(username, remoteAddr string) []string {
	keys := make([]string, 0, 2)
	if username != "" {
		keys = append(keys, "user:"+username)
	}
	if remoteAddr != "" {
		keys = append(keys, "addr:"+remoteAddr)
	}
	return keys
}
