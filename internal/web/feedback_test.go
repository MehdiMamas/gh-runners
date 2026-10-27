package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func TestFeedbackNeedsSignIn(t *testing.T) {
	_, h := setup(t)
	lastIssue.Store(nil)
	var v feedbackView
	if code := api(t, h, "POST", "/api/feedback/preview", `{"text":"broken"}`, &v); code != http.StatusOK || v.SignedIn || !strings.Contains(v.Message, "Settings") {
		t.Fatalf("preview signed out: %d %+v", code, v)
	}
	var e apiError
	if code := api(t, h, "POST", "/api/feedback", `{"text":"broken","body":"x"}`, &e); code != http.StatusUnauthorized {
		t.Fatalf("send signed out: %d %+v", code, e)
	}
	if lastIssue.Load() != nil {
		t.Fatal("an issue was filed without a sign-in")
	}
}

func TestFeedbackPreviewAndSend(t *testing.T) {
	_, h := setup(t)
	lastIssue.Store(nil)
	if err := os.MkdirAll(store.Home(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.TokenPath(), []byte("good-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A daemon log that holds the token, an email and a home path.
	home, _ := os.UserHomeDir()
	log := "start good-token\nmail me at jane.doe@example.com\nopen " + filepath.Join(home, "secret", "x") + "\n"
	if err := os.WriteFile(store.DaemonLog(), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	var pv feedbackView
	body := `{"text":"it broke\nsecond line","daemon_log":true}`
	if code := api(t, h, "POST", "/api/feedback/preview", body, &pv); code != http.StatusOK || !pv.SignedIn || pv.Login != "octo" {
		t.Fatalf("preview: %d %+v", code, pv)
	}
	for _, bad := range []string{"good-token", "jane.doe@example.com", home} {
		if strings.Contains(pv.Body, bad) {
			t.Errorf("preview leaks %q:\n%s", bad, pv.Body)
		}
	}
	if !strings.Contains(pv.Body, "<details>") || !strings.Contains(pv.Body, "it broke") {
		t.Fatalf("preview body: %s", pv.Body)
	}
	// Send what was previewed, plus a token someone slipped in.
	send, _ := json.Marshal(map[string]string{"text": "it broke\nsecond line", "body": pv.Body + "\nextra good-token"})
	var out feedbackView
	if code := api(t, h, "POST", "/api/feedback", string(send), &out); code != http.StatusOK || out.Number != 99 || !strings.HasSuffix(out.URL, "/issues/99") {
		t.Fatalf("send: %d %+v", code, out)
	}
	c := lastIssue.Load()
	if c == nil || c.auth != "Bearer good-token" {
		t.Fatalf("not filed with the user's own token: %+v", c)
	}
	var sent struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}
	_ = json.Unmarshal([]byte(c.body), &sent)
	if strings.Contains(sent.Body, "good-token") || sent.Title != "Feedback: it broke" || len(sent.Labels) != 1 || sent.Labels[0] != "feedback" {
		t.Fatalf("sent: %+v", sent)
	}
}

func TestFeedbackSendNeedsPreview(t *testing.T) {
	_, h := setup(t)
	var e apiError
	if code := api(t, h, "POST", "/api/feedback", `{"text":"x"}`, &e); code != http.StatusBadRequest {
		t.Fatalf("got %d", code)
	}
}
