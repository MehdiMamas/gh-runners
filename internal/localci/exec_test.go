package localci

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runSteps runs the given job YAML (a job body under "jobs: j:") and returns
// the result and the log text.
func runSteps(t *testing.T, body string) (Result, string) {
	t.Helper()
	wf, err := ParseWorkflow([]byte("jobs:\n  j:\n    runs-on: ubuntu-latest\n" + body))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	res := LocalExecutor{}.RunJob(context.Background(), JobSpec{
		Name: "j", Job: wf.Jobs["j"], Tarball: makeTarball(t, 9000),
		RunDir: filepath.Join(dir, "j"), Log: filepath.Join(dir, "j.log"),
	})
	b, _ := os.ReadFile(res.Log)
	return res, string(b)
}

func TestRunJobStepTimeout(t *testing.T) {
	old := minuteUnit
	minuteUnit = 500 * time.Millisecond
	t.Cleanup(func() { minuteUnit = old })
	start := time.Now()
	res, log := runSteps(t, "    steps:\n      - timeout-minutes: 1\n        run: sleep 30\n")
	if res.Status != Fail || !strings.Contains(res.Reason, "timed out") {
		t.Fatalf("got %v %q\n%s", res.Status, res.Reason, log)
	}
	if time.Since(start) > 20*time.Second {
		t.Errorf("step was not cut short: %v", time.Since(start))
	}
}

func TestRunJobFailMarkerExit0(t *testing.T) {
	res, log := runSteps(t, "    steps:\n      - run: |\n          echo \"Some tests failed.\"\n          exit 0\n")
	if res.Status != Fail || res.Infra {
		t.Fatalf("got %v %q\n%s", res.Status, res.Reason, log)
	}
	if !strings.Contains(log, "test output reports failures (exit code was 0)") {
		t.Errorf("log lacks the reason: %s", log)
	}
}

func TestRunJobFlutterSummaryFail(t *testing.T) {
	res, log := runSteps(t, "    steps:\n      - run: |\n          echo \"00:01 +3 -1: x\"\n")
	if res.Status != Fail {
		t.Fatalf("got %v %q\n%s", res.Status, res.Reason, log)
	}
	res, log = runSteps(t, "    steps:\n      - run: |\n          echo \"00:01 +3 -0: all passed\"\n")
	if res.Status != Pass {
		t.Fatalf("clean summary: got %v %q\n%s", res.Status, res.Reason, log)
	}
}

const dartCrash = "The Dart compiler exited unexpectedly"

func TestRunJobInfraRetryPasses(t *testing.T) {
	res, log := runSteps(t, `    steps:
      - run: |
          f="$GITHUB_WORKSPACE/seen"
          if [ ! -f "$f" ]; then
            touch "$f"
            echo "`+dartCrash+`"
            exit 1
          fi
          echo ok
`)
	if res.Status != Pass {
		t.Fatalf("got %v %q\n%s", res.Status, res.Reason, log)
	}
	if !strings.Contains(log, "::retry step 1: "+dartCrash) {
		t.Errorf("log lacks the retry line: %s", log)
	}
}

func TestRunJobInfraRetryFails(t *testing.T) {
	res, log := runSteps(t, "    steps:\n      - run: |\n          echo \""+dartCrash+"\"\n          exit 1\n")
	if res.Status != Fail || !res.Infra || !strings.Contains(res.Reason, "retried once") {
		t.Fatalf("got %v infra=%v %q\n%s", res.Status, res.Infra, res.Reason, log)
	}
	if n := strings.Count(log, "::retry"); n != 1 {
		t.Errorf("want one retry, got %d", n)
	}
}

func TestInfraCommentPrefix(t *testing.T) {
	r := Result{Job: "j", Status: Fail, Reason: "boom", Infra: true}
	if got := CommentBody(CommentInput{Results: []Result{r}}); !strings.Contains(got, "Infrastructure: boom") {
		t.Errorf("comment lacks the prefix: %s", got)
	}
}
