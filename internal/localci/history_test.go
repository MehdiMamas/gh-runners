package localci

import (
	"errors"
	"testing"
	"time"
)

func TestHistoryKeepsNewestFirstAndCaps(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if len(History()) != 0 {
		t.Fatal("history should start empty")
	}
	for i := 0; i < MaxHistory+3; i++ {
		if err := AddHistory(HistoryRun{ID: string(rune('a' + i%26)), PR: i}); err != nil {
			t.Fatal(err)
		}
	}
	runs := History()
	if len(runs) != MaxHistory || runs[0].PR != MaxHistory+2 {
		t.Fatalf("got %d runs, first PR %d", len(runs), runs[0].PR)
	}
}

func TestNewHistoryRun(t *testing.T) {
	o := Options{Owner: "o", Repo: "r", PR: 7, DryRun: true}
	var rep Report
	rep.PR.Title, rep.PR.Head.SHA = "Fix", "abc"
	rep.Results = []Result{{Job: "test", Status: Pass}}
	r := NewHistoryRun(o, true, time.Now(), rep, nil)
	if r.Repo != "o/r" || r.PR != 7 || !r.DryRun || !r.Auto || r.Title != "Fix" || r.SHA != "abc" || !r.Passed {
		t.Fatalf("unexpected run %+v", r)
	}
	if r := NewHistoryRun(o, false, time.Now(), rep, errors.New("boom")); r.Passed || r.Error != "boom" {
		t.Fatalf("a run that errored should not pass: %+v", r)
	}
}
