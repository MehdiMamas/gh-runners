package localci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
)

func lines(s string) []string { return strings.Split(s, "\n") }

func TestParseCounts(t *testing.T) {
	cases := []struct {
		name, log, want string
	}{
		{"playwright", "Running 15 tests\n  1 failed\n    [chromium] › a.spec.ts:1:1 › x\n  2 skipped\n  1 flaky\n  12 passed (30s)\n", "12 passed, 1 failed, 2 skipped, 1 flaky"},
		{"vitest", "\x1b[2m Test Files \x1b[22m 1 failed | 3 passed (4)\n\x1b[2m      Tests \x1b[22m \x1b[31m1 failed\x1b[39m | 12 passed (13)\n", "12 passed, 1 failed"},
		{"jest", "Tests:       1 failed, 2 skipped, 12 passed, 15 total\n", "12 passed, 1 failed, 2 skipped"},
		{"go", "--- FAIL: TestA (0.00s)\n    --- FAIL: TestA/sub (0.00s)\n--- PASS: TestB (0.00s)\n--- PASS: TestC (0.00s)\n--- SKIP: TestD (0.00s)\n", "2 passed, 1 failed, 1 skipped"},
		{"none", "building...\ndone\n", ""},
	}
	for _, c := range cases {
		got := ParseCounts(lines(c.log))
		if c.want == "" {
			if got.Known {
				t.Errorf("%s: want unknown, got %v", c.name, got)
			}
			continue
		}
		if got.String() != c.want {
			t.Errorf("%s: got %q want %q", c.name, got.String(), c.want)
		}
	}
}

func TestFailedTests(t *testing.T) {
	log := "  1) [chromium] › tests/rides.spec.ts:42:5 › add a phone ride\n  1) [chromium] › tests/rides.spec.ts:42:5 › add a phone ride\n" +
		"\x1b[31m FAIL \x1b[39m  src/x.test.ts > suite > name\n--- FAIL: TestX (0.01s)\nFAIL\tpkg\t0.1s\nFAIL\n"
	got := FailedTests(lines(log))
	want := []string{"[chromium] › tests/rides.spec.ts:42:5 › add a phone ride", "src/x.test.ts > suite > name", "TestX"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestScrub(t *testing.T) {
	cases := []struct{ in, want string }{
		{"token ghp_abcdefghijklmnopqrstuvwxyz0123456789 end", "token *** end"},
		{"github_pat_11ABCDEFG0abcdefghijklmnop_xyz", "***"},
		{"Authorization: Bearer abc.def-ghi123456", "Authorization: ***"},
		{"id AKIAABCDEFGHIJKLMNOP x", "id *** x"},
		{"api_key=Abcdefghij0123456789xyz+/=", "api_key=***"},
		{"MYVAR is hunter2hunter2 ok", "MYVAR is *** ok"},
		{"nothing here, key=short", "nothing here, key=short"},
	}
	for _, c := range cases {
		if got := Scrub(c.in, []string{"hunter2hunter2"}); got != c.want {
			t.Errorf("Scrub(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestKnownIssues(t *testing.T) {
	is := []github.Issue{
		{Number: 1, Title: `Flaky: "add a phone ride" times out`},
		{Number: 2, Title: "rides.spec.ts keeps failing on CI"},
		{Number: 3, Title: "Unrelated thing"},
		{Number: 4, Title: "short"},
	}
	got := KnownIssues([]string{"[chromium] › tests/rides.spec.ts:42:5 › add a phone ride", "TestX"}, is)
	if len(got) != 2 || got[0].Number != 1 || got[1].Number != 2 {
		t.Fatalf("got %v", got)
	}
}

func writeLog(t *testing.T, s string) string {
	p := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func testPR() github.PullRequest {
	var p github.PullRequest
	p.Head.SHA = "abc1234def5678"
	p.Base.SHA = "def5678abc1234"
	return p
}

func TestCommentBodyFail(t *testing.T) {
	pw := writeLog(t, "Running 13 tests\n\n  1) [chromium] › tests/rides.spec.ts:42:5 › add a phone ride ──\n\n    Error: expect(received).toBe(expected)\n    token=ghp_abcdefghijklmnopqrstuvwxyz0123456789\n    secretvalue1 leaked\n\n  1 failed\n    [chromium] › tests/rides.spec.ts:42:5 › add a phone ride\n  12 passed (30s)\n")
	body := CommentBody(CommentInput{
		PR: testPR(), Secrets: []string{"secretvalue1"},
		Results: []Result{
			{Job: "e2e", Status: Fail, Reason: "step 4", Duration: 95 * time.Second, Log: pw, Machine: "mac-mini"},
			{Job: "lint", Status: Pass, Duration: 12 * time.Second, Machine: "mac-mini"},
			{Job: "deploy", Status: Skipped, Reason: "not needed"},
			{Job: "api", Status: Fail, Duration: time.Second, Machine: "pc2", Log: "/nonexistent/x.log"},
		},
		Known: func() []github.Issue { return []github.Issue{{Number: 7, Title: "Flaky: add a phone ride"}} },
	})
	for _, w := range []string{
		Marker + "\n**FAIL**: 2 of 3 jobs that ran failed",
		"Head `abc1234` on base `def5678`",
		"| Job | Machine | Result | Tests | Time |",
		"| e2e | mac-mini | FAIL - step 4 | 12 passed, 1 failed | 1m35s |",
		"| lint | mac-mini | PASS | - | 12s |",
		"<details><summary>e2e: why it failed</summary>",
		"- `[chromium] › tests/rides.spec.ts:42:5 › add a phone ride`",
		"token=***",
		"*** leaked",
		"Also open as #7: Flaky: add a phone ride",
		"**api**: log is on pc2",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q in:\n%s", w, body)
		}
	}
	for _, bad := range []string{"ghp_", "secretvalue1", "localhost"} {
		if strings.Contains(body, bad) {
			t.Errorf("found %q", bad)
		}
	}
	t.Log("\n" + body)
}

func TestCommentBodyPass(t *testing.T) {
	body := CommentBody(CommentInput{PR: testPR(), Results: []Result{
		{Job: "a", Status: Pass, Duration: time.Second, Machine: "m"},
		{Job: "b", Status: Pass, Duration: time.Second, Machine: "m"},
		{Job: "c", Status: Skipped},
	}})
	if !strings.Contains(body, "**PASS**: all 2 jobs that ran passed") || strings.Contains(body, "<details>") {
		t.Fatalf("bad body:\n%s", body)
	}
}

func TestCommentBodyCapped(t *testing.T) {
	var rs []Result
	big := strings.Repeat("x", 3000)
	for i := 0; i < 40; i++ {
		rs = append(rs, Result{Job: "j" + string(rune('a'+i%26)) + strings.Repeat("z", i), Status: Fail,
			Log: writeLog(t, strings.Repeat("--- FAIL: TestSomething"+big+"\n", 20))})
	}
	if n := len(CommentBody(CommentInput{PR: testPR(), Results: rs})); n > 65536 {
		t.Fatalf("comment is %d chars", n)
	}
}

func TestScrubPaths(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/Users/bob/.gh-runners/ci/app/pr599-332b388-1793094601/src/a.ts:3 boom", "<workdir>/src/a.ts:3 boom"},
		{`C:\Users\bob\.gh-runners\ci\app\pr7-abc-1\src\a.ts:3`, `<workdir>\src\a.ts:3`},
		{"at /Users/bob/Library/x.js and /home/amy/y.js", "at ~/Library/x.js and ~/y.js"},
		{`C:\Users\bob\AppData\z`, `~\AppData\z`},
		{"nothing here /usr/lib/x", "nothing here /usr/lib/x"},
	}
	for _, c := range cases {
		if got := ScrubPaths(c.in); got != c.want {
			t.Errorf("ScrubPaths(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCommentBodyHidesPaths(t *testing.T) {
	dir := t.TempDir()
	log := dir + "/job.log"
	line := "--- FAIL: TestX\n    /Users/bob/.gh-runners/ci/app/pr1-abc-2/x_test.go:9: bad\n"
	if err := os.WriteFile(log, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	body := CommentBody(CommentInput{PR: testPR(), Results: []Result{
		{Job: "t", Status: Fail, Reason: "step in /Users/bob/.gh-runners/ci/app/pr1-abc-2", Log: log, Machine: "m"},
	}})
	if strings.Contains(body, "bob") || strings.Contains(body, "/Users/") {
		t.Errorf("path leaked:\n%s", body)
	}
	if !strings.Contains(body, "<workdir>/x_test.go:9") {
		t.Errorf("run folder not replaced:\n%s", body)
	}
}
