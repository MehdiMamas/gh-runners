package localci

import (
	"context"
	"path/filepath"
	"testing"
)

const serverWorkflow = `name: CI
on: [pull_request]
jobs:
  dashboard-e2e:
    runs-on: ubuntu-latest
    steps:
      - run: npx playwright test
  e2e-smoke:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
  lint:
    runs-on: ubuntu-latest
    steps:
      - run: npm run lint
  unit:
    runs-on: ubuntu-latest
    steps:
      - run: npm test
  clash:
    runs-on: ubuntu-latest
    steps:
      - run: |
          echo "Error: http://127.0.0.1:8787/health is already used, make sure nothing runs on the port" >&2
          exit 1
  realfail:
    runs-on: ubuntu-latest
    steps:
      - run: echo "assertion failed"; exit 1
`

func TestStartsServers(t *testing.T) {
	wf, err := ParseWorkflow([]byte(serverWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	for n, want := range map[string]bool{"dashboard-e2e": true, "e2e-smoke": true, "lint": false, "unit": false} {
		if got := wf.Jobs[n].StartsServers(); got != want {
			t.Errorf("%s: StartsServers=%v want %v", n, got, want)
		}
	}
}

func TestPortClashIsBlocked(t *testing.T) {
	wf, err := ParseWorkflow([]byte(serverWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	tb := makeTarball(t, 59999)
	dir := t.TempDir()
	run := func(n string) Result {
		return LocalExecutor{}.RunJob(context.Background(), JobSpec{
			Name: n, Job: wf.Jobs[n], Tarball: tb,
			RunDir: filepath.Join(dir, n), Log: filepath.Join(dir, n+".log"),
		})
	}
	if r := run("clash"); r.Status != Blocked || r.Reason != "port clash with another process" {
		t.Errorf("clash: got %v %q", r.Status, r.Reason)
	}
	if r := run("realfail"); r.Status != Fail {
		t.Errorf("realfail: got %v", r.Status)
	}
}
