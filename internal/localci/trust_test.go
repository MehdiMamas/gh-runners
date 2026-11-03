package localci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/github"
)

func permServer(t *testing.T, perms map[string]string) (*github.Client, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		p := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/o/r/collaborators/"), "/permission")
		perm, ok := perms[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"permission":"` + perm + `"}`))
	}))
	t.Cleanup(srv.Close)
	return &github.Client{Token: "t", BaseURL: srv.URL, HTTP: srv.Client()}, &calls
}

func TestTrustPRCollaborators(t *testing.T) {
	c, calls := permServer(t, map[string]string{"writer": "write", "reader": "read", "maint": "maintain"})
	ctx := context.Background()
	allow := []string{"collaborators"}
	for _, u := range []string{"writer", "maint"} {
		if err := TrustPR(ctx, c, pr(u, "o/r", "o/r"), allow, false); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	for _, u := range []string{"reader", "nobody"} {
		if TrustPR(ctx, c, pr(u, "o/r", "o/r"), allow, false) == nil {
			t.Errorf("%s allowed", u)
		}
	}
	if TrustPR(ctx, c, pr("writer", "fork/r", "o/r"), allow, false) == nil {
		t.Error("fork allowed with collaborators")
	}
	// Without the word the permission is never asked.
	before := *calls
	if TrustPR(ctx, c, pr("writer", "o/r", "o/r"), []string{"me"}, false) == nil || *calls != before {
		t.Error("collaborator trusted without the word")
	}
	// Cached: a second check of the same author makes no request.
	before = *calls
	_ = TrustPR(ctx, c, pr("writer", "o/r", "o/r"), allow, false)
	if *calls != before {
		t.Error("permission not cached")
	}
}
