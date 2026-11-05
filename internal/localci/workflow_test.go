package localci

import (
	"strings"
	"testing"
)

func TestParseNeedTools(t *testing.T) {
	for _, v := range []string{"flutter java", "flutter,java"} {
		wf, err := ParseWorkflow([]byte("jobs:\n  a:\n    runs-on: ubuntu-latest\n    env:\n      GH_RUNNERS_NEEDS: \"" + v + "\"\n    steps:\n      - run: x\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(wf.Jobs["a"].NeedTools, " "); got != "flutter java" {
			t.Fatalf("%q: got %q", v, got)
		}
	}
	_, err := ParseWorkflow([]byte("jobs:\n  a:\n    runs-on: ubuntu-latest\n    env:\n      GH_RUNNERS_NEEDS: gradle\n    steps:\n      - run: x\n"))
	if err == nil || !strings.Contains(err.Error(), "job a") || !strings.Contains(err.Error(), `"gradle"`) {
		t.Fatalf("want an error naming job and tool, got %v", err)
	}
}

func TestRequiresNeedTools(t *testing.T) {
	j := &Job{NeedTools: []string{"java", "flutter", "node"}, Steps: []Step{{Uses: "subosito/flutter-action@v2"}}}
	tools, _ := j.Requires()
	if got := strings.Join(tools, " "); got != "bash flutter java node" {
		t.Fatalf("got %q", got)
	}
	tools, emu := (&Job{}).Requires()
	if strings.Join(tools, " ") != "bash node" || emu {
		t.Fatalf("plain job: %v %v", tools, emu)
	}
}
