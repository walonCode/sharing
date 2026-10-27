package server

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	failLimit  = 10
	failWindow = time.Minute
)

// failLimiter counts failed authentications per client address. After
// failLimit failures inside failWindow the address is refused until the
// window passes. Honest clients never fail, so the cost of this is nil for
// them while a token guesser is cut off (and visible in the log).
type failLimiter struct {
	mu    sync.Mutex
	fails map[string]*failRec
	now   func() time.Time
}

type failRec struct {
	n     int
	start time.Time
}

func newFailLimiter() *failLimiter { return &failLimiter{fails: map[string]*failRec{}, now: time.Now} }

// blocked returns how long the address must wait, or 0.
func (l *failLimiter) blocked(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.fails[ip]
	if r == nil {
		return 0
	}
	if wait := failWindow - l.now().Sub(r.start); wait > 0 && r.n >= failLimit {
		return wait
	}
	return 0
}

func (l *failLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.fails) > 1024 { // bound memory under a spray of addresses
		for k, r := range l.fails {
			if now.Sub(r.start) >= failWindow {
				delete(l.fails, k)
			}
		}
	}
	r := l.fails[ip]
	if r == nil || now.Sub(r.start) >= failWindow {
		r = &failRec{start: now}
		l.fails[ip] = r
	}
	r.n++
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
}
