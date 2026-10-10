package app

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// rateLimitRequests is the number of POST requests one client IP may send
	// to each OAuth endpoint per rateLimitWindow. A normal connection flow
	// sends one request to each endpoint, so this leaves plenty of room while
	// stopping scripted abuse of the unauthenticated endpoints.
	rateLimitRequests = 30
	rateLimitWindow   = time.Minute

	// maxLimiterKeys bounds the limiter's memory. When full, the window that
	// ends first is dropped to make room.
	maxLimiterKeys = 10000
)

// rateWindow counts the requests of one key inside the current fixed window.
type rateWindow struct {
	start time.Time
	count int
}

// rateLimiter is a small in-process fixed-window limiter keyed by string.
// Requests over the limit do not extend the window.
type rateLimiter struct {
	limit  int
	window time.Duration

	mu        sync.Mutex
	windows   map[string]rateWindow
	lastSweep time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, windows: make(map[string]rateWindow)}
}

// allow records a request for key at now. When it is over the limit, ok is
// false and retryAfter is the time left until the window resets.
func (l *rateLimiter) allow(key string, now time.Time) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) >= sweepInterval {
		l.lastSweep = now
		for k, w := range l.windows {
			if !now.Before(w.start.Add(l.window)) {
				delete(l.windows, k)
			}
		}
	}

	w, found := l.windows[key]
	if !found || !now.Before(w.start.Add(l.window)) {
		if !found && len(l.windows) >= maxLimiterKeys {
			evictEarliest(l.windows, func(w rateWindow) time.Time { return w.start.Add(l.window) })
		}
		l.windows[key] = rateWindow{start: now, count: 1}
		return true, 0
	}
	if w.count >= l.limit {
		return false, w.start.Add(l.window).Sub(now)
	}
	w.count++
	l.windows[key] = w
	return true, 0
}

// clientIP identifies the caller for rate limiting. The service runs behind
// Cloudflare and Traefik, so the forwarding headers are trusted in this order:
// CF-Connecting-IP, the first X-Forwarded-For entry, then the socket address.
// Values that are not IP addresses are ignored so they cannot be used to
// create arbitrary keys.
func clientIP(r *http.Request) string {
	if ip := normalizeIP(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	if first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); first != "" {
		if ip := normalizeIP(first); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := normalizeIP(host); ip != "" {
		return ip
	}
	return host
}

func normalizeIP(s string) string {
	if ip := net.ParseIP(strings.TrimSpace(s)); ip != nil {
		return ip.String()
	}
	return ""
}

// rateLimited applies the per-IP limit to POST requests of an endpoint; other
// methods pass through untouched. Each endpoint has its own budget, so a burst
// of registrations cannot starve token exchanges from the same address. The
// refusal is an HTML page for the browser-facing authorize endpoint and an
// OAuth JSON error elsewhere.
func (s *oauthServer) rateLimited(endpoint string, html bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next(w, r)
			return
		}
		ok, retryAfter := s.limiter.allow(endpoint+"|"+clientIP(r), s.now())
		if ok {
			next(w, r)
			return
		}

		seconds := max(int(math.Ceil(retryAfter.Seconds())), 1)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		if html {
			renderPage(w, http.StatusTooManyRequests, authorizePage{
				FatalTitle:   "Too many attempts",
				FatalMessage: fmt.Sprintf("Please wait %d seconds and try again.", seconds),
			})
			return
		}
		writeOAuthError(w, http.StatusTooManyRequests, "temporarily_unavailable",
			fmt.Sprintf("too many requests; retry after %d seconds", seconds))
	}
}
