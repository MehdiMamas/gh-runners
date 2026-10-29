package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetStatusTargetURLOnlyWhenSet(t *testing.T) {
	var bodies []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := map[string]string{}
		_ = json.NewDecoder(r.Body).Decode(&m)
		bodies = append(bodies, m)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := New("tok")
	c.BaseURL = srv.URL
	if err := c.SetStatus(context.Background(), "o", "r", "sha", "success", "x", "d", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.SetStatus(context.Background(), "o", "r", "sha", "success", "x", "d", "http://l"); err != nil {
		t.Fatal(err)
	}
	if _, ok := bodies[0]["target_url"]; ok {
		t.Errorf("empty link was sent: %v", bodies[0])
	}
	if bodies[1]["target_url"] != "http://l" {
		t.Errorf("link not sent: %v", bodies[1])
	}
}
