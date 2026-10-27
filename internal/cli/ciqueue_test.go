package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueueCISendsAuthAndReturnsTask(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie("ghr_ui")
		if r.Method != "POST" || r.URL.Path != "/api/ci/run" || r.Header.Get("X-GHR") != "1" || c == nil || c.Value != "sekret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"task":7}`))
	}))
	defer srv.Close()
	id, err := queueCI(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "sekret", "o/r", 12, true, false)
	if err != nil || id != 7 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if got["repo"] != "o/r" || got["pr"] != float64(12) || got["dry_run"] != true {
		t.Fatalf("body %v", got)
	}
}

func TestQueueCIRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"locked"}`))
	}))
	defer srv.Close()
	_, err := queueCI(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "bad", "o/r", 1, false, false)
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("err=%v", err)
	}
	if _, down := err.(errNoDaemon); down {
		t.Fatal("refusal is not daemon-down")
	}
}

func TestQueueCIDaemonDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	_, err := queueCI(context.Background(), addr, "s", "o/r", 1, false, false)
	if _, down := err.(errNoDaemon); !down {
		t.Fatalf("want errNoDaemon, got %v", err)
	}
}
