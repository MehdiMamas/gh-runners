package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestETagReturnsCachedBodyOn304(t *testing.T) {
	resetLimiter()
	var hits, cond int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Limit", "5000")
		if r.Header.Get("If-None-Match") == `"v1"` {
			atomic.AddInt32(&cond, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(`{"login":"me"}`))
	}))
	defer srv.Close()
	c := New("tok")
	c.BaseURL = srv.URL
	for i := 0; i < 2; i++ {
		var u User
		if _, err := c.do(context.Background(), http.MethodGet, "/user", nil, &u); err != nil || u.Login != "me" {
			t.Fatalf("call %d: %+v %v", i, u, err)
		}
	}
	if hits != 2 || cond != 1 {
		t.Fatalf("hits=%d conditional=%d", hits, cond)
	}
	if s := Stats(); s.Remaining != 4999 || s.Limit != 5000 || s.CallsLastMinute != 2 {
		t.Fatalf("stats %+v", s)
	}
}

func TestRateLimitBlocksUntilReset(t *testing.T) {
	resetLimiter()
	reset := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"API rate limit exceeded for user ID 1."}`))
	}))
	defer srv.Close()
	c := New("tok")
	c.BaseURL = srv.URL
	for i := 0; i < 3; i++ {
		_, err := c.do(context.Background(), http.MethodGet, "/user", nil, nil)
		if !IsRateLimited(err) {
			t.Fatalf("call %d: %v", i, err)
		}
		if want := "resumes at " + reset.Local().Format("15:04"); !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q lacks %q", err, want)
		}
	}
	if hits != 1 {
		t.Fatalf("GitHub was called %d times while blocked", hits)
	}
	// After the reset, calls go through again.
	defer func() { now = time.Now }()
	now = func() time.Time { return reset.Add(time.Second) }
	c.do(context.Background(), http.MethodGet, "/user", nil, nil)
	if hits != 2 {
		t.Fatalf("not called after reset: %d", hits)
	}
}

func TestPlain403IsNotRateLimit(t *testing.T) {
	resetLimiter()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4000")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	defer srv.Close()
	c := New("tok")
	c.BaseURL = srv.URL
	_, err := c.do(context.Background(), http.MethodGet, "/user", nil, nil)
	if IsRateLimited(err) || err == nil {
		t.Fatalf("got %v", err)
	}
	if _, err := c.do(context.Background(), http.MethodGet, "/user", nil, nil); IsRateLimited(err) {
		t.Fatal("blocked after a plain 403")
	}
}
