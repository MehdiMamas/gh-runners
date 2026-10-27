package feedback

import (
	"os"
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	home, _ := os.UserHomeDir()
	in := "token ghp_abcdefghijklmnopqrstuvwxyz0123456789 and mytoken12345 mail a.b@example.org in " + home + "/proj and /Users/bob/x"
	out := Scrub(in, []string{"mytoken12345"})
	for _, bad := range []string{"ghp_abcdef", "mytoken12345", "a.b@example.org", home, "bob"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q still in %q", bad, out)
		}
	}
}

func TestBuildTrimsTailFirst(t *testing.T) {
	var lines []string
	for i := 0; i < 20000; i++ {
		lines = append(lines, "line number "+strings.Repeat("x", 20))
	}
	log := strings.Join(lines, "\n") + "\nTHE END"
	body := Build("it broke", "v1", []Section{{"Daemon log", log}, {"doctor", "ok all fine"}}, nil)
	if len(body) > MaxBody {
		t.Fatalf("body is %d bytes", len(body))
	}
	if !strings.Contains(body, "THE END") || !strings.Contains(body, "earlier lines cut") {
		t.Error("the end of the log must be kept and the cut marked")
	}
	if !strings.Contains(body, "ok all fine") {
		t.Error("a short section was cut")
	}
	if strings.Count(body, "<details>") != 2 || strings.Count(body, "</details>") != 2 {
		t.Error("details blocks not balanced")
	}
}

func TestBuildFenceInsideLog(t *testing.T) {
	body := Build("x", "v1", []Section{{"log", "a\n```\nb"}}, nil)
	if !strings.Contains(body, "````text") {
		t.Errorf("a log with a code fence needs a longer fence: %s", body)
	}
}

func TestTitle(t *testing.T) {
	if got := Title("  \n", nil); got != "Feedback: Feedback" {
		t.Error(got)
	}
	if got := Title(strings.Repeat("a", 200), nil); len(got) > 100 {
		t.Error(len(got))
	}
}
