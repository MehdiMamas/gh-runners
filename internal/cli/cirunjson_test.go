package cli

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/localci"
)

func testReport(st localci.Status) localci.Report {
	pr := github.PullRequest{Number: 42}
	pr.Head.SHA = "abc123"
	return localci.Report{PR: pr, Results: []localci.Result{
		{Job: "mobile", Machine: "DESKTOP-MEHDI", Status: st, Reason: "step 3", Duration: 1500 * time.Millisecond, Log: "/logs/mobile.log"},
	}}
}

func TestCIRunJSONShape(t *testing.T) {
	var b bytes.Buffer
	if err := writeCIJSON(&b, "o/r", testReport(localci.Pass)); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["repo"] != "o/r" || got["pr"] != float64(42) || got["sha"] != "abc123" {
		t.Fatalf("header %v", got)
	}
	j := got["jobs"].([]any)[0].(map[string]any)
	want := map[string]any{"name": "mobile", "machine": "DESKTOP-MEHDI", "status": "PASS", "reason": "step 3", "duration_ms": float64(1500), "log": "/logs/mobile.log"}
	if len(j) != len(want) {
		t.Fatalf("keys %v", j)
	}
	for k, v := range want {
		if j[k] != v {
			t.Fatalf("%s = %v, want %v", k, j[k], v)
		}
	}
}

func TestCIVerdictExitCode(t *testing.T) {
	if err := ciVerdict(testReport(localci.Pass)); err != nil {
		t.Fatalf("pass: %v", err)
	}
	for _, st := range []localci.Status{localci.Fail, localci.Unsupported, localci.Blocked} {
		if ciVerdict(testReport(st)) == nil {
			t.Fatalf("%s should be an error", st)
		}
	}
}
