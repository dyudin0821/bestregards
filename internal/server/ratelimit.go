package server

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipLimiter is a coarse per-process flood guard. Exact quotas live in PostgreSQL.
type ipLimiter struct {
	mu        sync.Mutex
	clients   map[string]*client
	limit     rate.Limit
	burst     int
	ttl       time.Duration
	lastSweep time.Time
}

type client struct {
	limiter *rate.Limiter
	seen    time.Time
}

const (
	maxLimiterKeys = 50_000
	sweepInterval  = time.Minute
	evictSample    = 8 // entries looked at when the table is full; the least recently seen goes
)

func newIPLimiter(perMinute, burst int) *ipLimiter {
	return &ipLimiter{
		clients: map[string]*client{},
		limit:   rate.Limit(float64(perMinute) / 60),
		burst:   burst,
		ttl:     10 * time.Minute,
	}
}

// allow reports whether the caller may proceed. Idle keys are dropped at most once a minute, so a
// request does not pay for the size of the table. The table never grows past maxLimiterKeys: when
// it is full, a newcomer replaces the least recently seen of a small sample. A flood from many
// addresses can therefore wear out its own buckets, but cannot push everyone else onto one shared
// bucket; the real barriers against it are the challenge and the spending ceilings.
func (l *ipLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= sweepInterval {
		for k, c := range l.clients {
			if now.Sub(c.seen) > l.ttl {
				delete(l.clients, k)
			}
		}
		l.lastSweep = now
	}
	c, ok := l.clients[ip]
	if !ok {
		if len(l.clients) >= maxLimiterKeys {
			l.evictOne()
		}
		c = &client{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.clients[ip] = c
	}
	c.seen = now
	return c.limiter.AllowN(now, 1)
}

// evictOne removes the least recently seen of the first few entries in map order, which is random.
func (l *ipLimiter) evictOne() {
	var oldest string
	var oldestSeen time.Time
	n := 0
	for k, c := range l.clients {
		if n == 0 || c.seen.Before(oldestSeen) {
			oldest, oldestSeen = k, c.seen
		}
		if n++; n == evictSample {
			break
		}
	}
	delete(l.clients, oldest)
}

// clientAddr returns the caller's address. Behind exactly one trusted proxy the proxy appends the
// real address as the last X-Forwarded-For entry of the last header line; earlier entries are
// client-controlled, and an entry that is not an IP address is ignored.
func clientAddr(r *http.Request, trustProxy bool) (netip.Addr, bool) {
	if trustProxy {
		if lines := r.Header.Values("X-Forwarded-For"); len(lines) > 0 {
			parts := strings.Split(lines[len(lines)-1], ",")
			if addr, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
				return addr.Unmap().WithZone(""), true
			}
		}
	}
	addr, err := netip.ParseAddr(remoteHost(r))
	return addr.Unmap().WithZone(""), err == nil
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientIP returns the key a caller is rate limited by. IPv6 callers are keyed by their /64, the
// smallest block one subscriber normally controls, so rotating addresses inside it buys nothing.
func clientIP(r *http.Request, trustProxy bool) string {
	addr, ok := clientAddr(r, trustProxy)
	if !ok {
		return remoteHost(r)
	}
	if addr.Is6() {
		p, _ := addr.Prefix(64)
		return p.String()
	}
	return addr.String()
}
