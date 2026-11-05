package web

import (
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func TestStateShowsGitHubHealth(t *testing.T) {
	s, h := setup(t)
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	s.h.Status = func() control.Status {
		return control.Status{
			PID: 42,
			Runners: []control.ProcState{
				{ID: "a", State: "running", GitHubOffline: true, GitHubOfflineSince: at.Add(-6 * time.Minute)},
				{ID: "b", State: "running"},
			},
			GitHubCheck: control.GitHubCheck{At: at, Err: "boom"},
		}
	}
	for _, id := range []string{"a", "b"} {
		if err := store.Save(store.Runner{ID: id, Kind: store.KindRepo, Owner: "o", Repo: "r", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	var v struct {
		GitHubOffline    *int   `json:"github_offline"`
		GitHubCheckAt    string `json:"github_check_at"`
		GitHubCheckError string `json:"github_check_error"`
		Runners          []struct {
			ID                 string `json:"id"`
			GitHubOffline      bool   `json:"github_offline"`
			GitHubOfflineSince string `json:"github_offline_since"`
		} `json:"runners"`
	}
	if code := api(t, h, "GET", "/api/state", "", &v); code != 200 {
		t.Fatalf("state: %d", code)
	}
	if v.GitHubOffline == nil || *v.GitHubOffline != 1 {
		t.Fatalf("github_offline = %v, want 1", v.GitHubOffline)
	}
	if v.GitHubCheckAt != "2026-11-04T10:00:00Z" || v.GitHubCheckError != "boom" {
		t.Fatalf("check = %q %q", v.GitHubCheckAt, v.GitHubCheckError)
	}
	got := map[string]bool{}
	for _, r := range v.Runners {
		got[r.ID] = r.GitHubOffline
		if r.ID == "a" && r.GitHubOfflineSince != "2026-11-04T09:54:00Z" {
			t.Fatalf("since = %q", r.GitHubOfflineSince)
		}
	}
	if !got["a"] || got["b"] {
		t.Fatalf("runner flags = %v", got)
	}
}

func TestConfigMaxJobs(t *testing.T) {
	_, h := setup(t)
	var put store.Config
	if code := api(t, h, "PUT", "/api/config", `{"max_jobs":1}`, &put); code != 200 || put.MaxJobs != 1 {
		t.Fatalf("put: %d, max_jobs %d", code, put.MaxJobs)
	}
	got, err := store.LoadConfig()
	if err != nil || got.MaxJobs != 1 {
		t.Fatalf("saved: %v, max_jobs %d", err, got.MaxJobs)
	}
	for _, bad := range []string{`{"max_jobs":-1}`, `{"max_jobs":65}`} {
		if code := api(t, h, "PUT", "/api/config", bad, nil); code != 400 {
			t.Errorf("%s: got %d, want 400", bad, code)
		}
	}
}
