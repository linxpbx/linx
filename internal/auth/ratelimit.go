package auth

import (
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Rate limits (docs/API.md §3). In memory, so per instance until a
// Valkey-backed limiter is added for multi-node (ADR-027).
const (
	CallsPerMinute       = 600
	CallBurst            = 100
	FailedAuthPerMinute  = 20
	limiterIdleExpiry    = 10 * time.Minute
	limiterSweepInterval = time.Minute
	// maxLimiters bounds memory under attack from many addresses; beyond it,
	// new keys share one overflow bucket.
	maxLimiters = 50_000
)

// Limiters is a set of token buckets, one per key (a principal id or a
// client address), created on first use and forgotten when idle.
type Limiters struct {
	perMinute int
	limit     rate.Limit
	burst     int

	mu       sync.Mutex
	buckets  map[string]*bucket
	overflow *rate.Limiter
	swept    time.Time
}

type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// NewLimiters allows perMinute events per minute per key, up to burst at once.
func NewLimiters(perMinute, burst int) *Limiters {
	l := rate.Limit(float64(perMinute) / 60)
	return &Limiters{
		perMinute: perMinute,
		limit:     l,
		burst:     burst,
		buckets:   map[string]*bucket{},
		overflow:  rate.NewLimiter(l, burst),
	}
}

// Allow takes one token for key, reporting whether one was available.
func (l *Limiters) Allow(key string, now time.Time) bool {
	return l.get(key, now).AllowN(now, 1)
}

// Exhausted reports whether key has no token left, without taking one.
func (l *Limiters) Exhausted(key string, now time.Time) bool {
	return l.get(key, now).TokensAt(now) < 1
}

// Status is a bucket's state for RateLimit-* headers.
type Status struct {
	Limit, Remaining int
	// Reset is how long until the bucket is full again.
	Reset time.Duration
	// RetryAfter is how long until the next token, if none is left.
	RetryAfter time.Duration
}

// Status reports key's bucket without changing it.
func (l *Limiters) Status(key string, now time.Time) Status {
	lim := l.get(key, now)
	tokens := lim.TokensAt(now)
	s := Status{Limit: l.perMinute, Remaining: max(0, int(math.Floor(tokens)))}
	if l.limit > 0 {
		s.Reset = time.Duration((float64(l.burst) - tokens) / float64(l.limit) * float64(time.Second))
		if tokens < 1 {
			s.RetryAfter = time.Duration((1 - tokens) / float64(l.limit) * float64(time.Second))
		}
	}
	return s
}

func (l *Limiters) get(key string, now time.Time) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.swept) > limiterSweepInterval {
		for k, b := range l.buckets {
			if now.Sub(b.lastSeen) > limiterIdleExpiry {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxLimiters {
			return l.overflow
		}
		b = &bucket{lim: rate.NewLimiter(l.limit, l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = now
	return b.lim
}

// IPKey groups addresses for failed-auth limiting: one bucket per IPv4
// address, one per IPv6 /64 (what a single host usually controls).
func IPKey(ip netip.Addr) string {
	if !ip.IsValid() {
		return "unknown"
	}
	if ip.Is6() {
		p, _ := ip.Prefix(64)
		return p.String()
	}
	return ip.String()
}

// SetRateLimitHeaders writes RateLimit-Limit/-Remaining/-Reset and, when
// the bucket is empty, Retry-After.
func SetRateLimitHeaders(h http.Header, s Status) {
	h.Set("RateLimit-Limit", strconv.Itoa(s.Limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(s.Remaining))
	h.Set("RateLimit-Reset", strconv.Itoa(ceilSeconds(s.Reset)))
	if s.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(max(1, ceilSeconds(s.RetryAfter))))
	}
}

func ceilSeconds(d time.Duration) int {
	return int(math.Ceil(d.Seconds()))
}
