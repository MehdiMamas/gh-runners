package github

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimitError means GitHub's rate limit for the token is used up. Calls
// are refused locally until Until, so a poll loop can't keep it exhausted.
type RateLimitError struct {
	Until time.Time
}

func (e *RateLimitError) Error() string {
	return "GitHub rate limit reached, resumes at " + e.Until.Local().Format("15:04")
}

// IsRateLimited reports whether err is a RateLimitError.
func IsRateLimited(err error) bool {
	var rl *RateLimitError
	return errors.As(err, &rl)
}

// RateStats is what the client knows about API use, for `doctor`.
type RateStats struct {
	CallsLastMinute int
	Remaining       int // -1 until GitHub has answered once
	Limit           int
	Reset           time.Time
	BlockedUntil    time.Time
}

// limiter is shared by every Client in the process: they all spend the same
// user's hourly allowance.
var limiter = &rateState{remaining: -1}

type rateState struct {
	mu        sync.Mutex
	until     time.Time
	remaining int
	limit     int
	reset     time.Time
	calls     []time.Time
}

var now = time.Now

// Stats returns the API use seen by this process.
func Stats() RateStats {
	l := limiter
	l.mu.Lock()
	defer l.mu.Unlock()
	l.trim()
	return RateStats{CallsLastMinute: len(l.calls), Remaining: l.remaining, Limit: l.limit, Reset: l.reset, BlockedUntil: l.until}
}

func (l *rateState) trim() {
	cut := now().Add(-time.Minute)
	i := 0
	for i < len(l.calls) && l.calls[i].Before(cut) {
		i++
	}
	l.calls = l.calls[i:]
}

// before refuses a call while blocked and counts it otherwise.
func (l *rateState) before() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now().Before(l.until) {
		return &RateLimitError{Until: l.until}
	}
	l.trim()
	l.calls = append(l.calls, now())
	return nil
}

// after records the rate headers and, when the answer says the limit is hit,
// blocks calls until GitHub resets it. It returns the error to report then.
func (l *rateState) after(resp *http.Response, message string) error {
	h := resp.Header
	l.mu.Lock()
	defer l.mu.Unlock()
	if v, err := strconv.Atoi(h.Get("X-RateLimit-Remaining")); err == nil {
		l.remaining = v
	}
	if v, err := strconv.Atoi(h.Get("X-RateLimit-Limit")); err == nil {
		l.limit = v
	}
	var reset time.Time
	if v, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		reset = time.Unix(v, 0)
		l.reset = reset
	}
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	limited := h.Get("X-RateLimit-Remaining") == "0" || strings.Contains(strings.ToLower(message), "rate limit")
	if !limited {
		return nil
	}
	until := reset
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
		until = now().Add(time.Duration(s) * time.Second)
	}
	if !until.After(now()) {
		until = now().Add(time.Minute) // secondary limit with no hint
	}
	l.until = until
	return &RateLimitError{Until: until}
}

// resetLimiter is for tests.
func resetLimiter() {
	limiter = &rateState{remaining: -1}
}

func (s RateStats) String() string {
	if s.Remaining < 0 {
		return fmt.Sprintf("%d GitHub API calls in the last minute", s.CallsLastMinute)
	}
	return fmt.Sprintf("%d GitHub API calls in the last minute; %d of %d left this hour (resets %s)", s.CallsLastMinute, s.Remaining, s.Limit, s.Reset.Local().Format("15:04"))
}
