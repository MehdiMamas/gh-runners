package localci

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func jobWith(runsOn string, uses ...string) *Job {
	j := &Job{RunsOn: runsOn}
	for _, u := range uses {
		j.Steps = append(j.Steps, Step{Uses: u})
	}
	return j
}

var noLoad = func(string) int { return 0 }

func TestRouteJavaAvoidsNonEmulatorMembers(t *testing.T) {
	win := Machine{ID: "w", Name: "win", OS: "windows", Tools: []string{"bash", "node", "java"}}
	mac := Machine{Name: "mac", OS: "darwin", Tools: []string{"bash", "node", "java"}, EmulatorOK: true, Host: true}
	j := jobWith("ubuntu-latest", "actions/setup-java@v4")
	m, why := Route(j, []Machine{win, mac}, noLoad, false)
	if why != "" || m.Name != "mac" {
		t.Fatalf("got %v %q", m.Name, why)
	}
	_, why = Route(j, []Machine{win}, noLoad, false)
	if why == "" || !strings.Contains(why, "emulator") {
		t.Fatalf("want an emulator reason, got %q", why)
	}
	win.EmulatorOK = true
	if m, why := Route(j, []Machine{win}, noLoad, false); why != "" || m.Name != "win" {
		t.Fatalf("opted-in windows should run it: %v %q", m.Name, why)
	}
}

func TestRouteFlutterPrefersHost(t *testing.T) {
	tools := []string{"bash", "node", "flutter"}
	host := Machine{Name: "mac", OS: "darwin", Tools: tools, Host: true}
	other := Machine{ID: "w", Name: "win", OS: "windows", Tools: tools}
	j := jobWith("ubuntu-latest", "subosito/flutter-action@v2")
	busy := func(k string) int {
		if k == "/mac" {
			return 5
		}
		return 0
	}
	if m, _ := Route(j, []Machine{other, host}, busy, false); m.Name != "mac" {
		t.Fatalf("flutter job went to %s", m.Name)
	}
	host.Tools = []string{"bash", "node"}
	if m, _ := Route(j, []Machine{other, host}, busy, false); m.Name != "win" {
		t.Fatalf("host lacks flutter, got %s", m.Name)
	}
}

func TestRouteOSAndLoad(t *testing.T) {
	tools := []string{"bash", "node"}
	mac := Machine{Name: "mac", OS: "darwin", Tools: tools, Host: true}
	win := Machine{ID: "w", Name: "win", OS: "windows", Tools: tools}
	if m, why := Route(jobWith("windows-latest"), []Machine{mac, win}, noLoad, false); why != "" || m.Name != "win" {
		t.Fatalf("windows job: %v %q", m.Name, why)
	}
	if _, why := Route(jobWith("windows-latest"), []Machine{mac}, noLoad, false); why == "" {
		t.Fatal("windows job on a mac only should be unsupported")
	}
	busy := func(k string) int {
		if k == "/mac" {
			return 2
		}
		return 0
	}
	if m, _ := Route(jobWith("ubuntu-latest"), []Machine{mac, win}, busy, false); m.Name != "win" {
		t.Fatalf("least loaded should win, got %s", m.Name)
	}
	if m, _ := Route(jobWith("ubuntu-latest"), []Machine{win, mac}, noLoad, false); m.Name != "mac" {
		t.Fatalf("tie should go to the host, got %s", m.Name)
	}
}

func TestRouteNoFitIsUnsupported(t *testing.T) {
	mac := Machine{Name: "mac", OS: "darwin", Tools: []string{"bash"}, Host: true}
	_, why := Route(jobWith("ubuntu-latest"), []Machine{mac}, noLoad, false)
	if !strings.Contains(why, "lacks node") {
		t.Fatalf("reason %q", why)
	}
	d := &Dispatcher{Machines: func() []Machine { return []Machine{mac} }}
	r := d.RunJob(context.Background(), JobSpec{Name: "x", Job: jobWith("ubuntu-latest")})
	if r.Status != Unsupported || r.Reason == "" {
		t.Fatalf("got %+v", r)
	}
}

func TestScrubEnvKeepsToolchainVars(t *testing.T) {
	freshEnvFn = func(b []string) []string { return b }
	t.Cleanup(func() { freshEnvFn = freshEnv })
	out := ScrubEnv([]string{"JAVA_HOME=/jdk", "GITHUB_TOKEN=x", "RANDOM=1"}, RunEnv{}, nil)
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "JAVA_HOME=/jdk") || strings.Contains(joined, "GITHUB_TOKEN") || strings.Contains(joined, "RANDOM") {
		t.Fatalf("env %v", out)
	}
}

func TestRouteSecretJobStaysOnThisMachine(t *testing.T) {
	tools := []string{"bash", "node"}
	host := Machine{Name: "mac", OS: "darwin", Tools: tools, Host: true}
	other := Machine{ID: "w", Name: "win", OS: "windows", Tools: tools}
	busy := func(k string) int {
		if k == "/mac" {
			return 9
		}
		return 0
	}
	if m, _ := Route(jobWith("ubuntu-latest"), []Machine{host, other}, busy, false); m.Name != "win" {
		t.Fatalf("setup: the idle remote should win, got %s", m.Name)
	}
	if m, why := Route(jobWith("ubuntu-latest"), []Machine{host, other}, busy, true); why != "" || m.Name != "mac" {
		t.Fatalf("secret job went to %s %q", m.Name, why)
	}
	if _, why := Route(jobWith("windows-latest"), []Machine{host, other}, busy, true); why == "" {
		t.Fatal("a windows secret job must not go to the remote windows machine")
	}
}

type countExec struct{ ran int }

func (c *countExec) RunJob(ctx context.Context, s JobSpec) Result {
	c.ran++
	return Result{Job: s.Name, Status: Pass}
}

func TestDispatcherSecretJobPinnedToRemoteIsBlocked(t *testing.T) {
	tools := []string{"bash", "node"}
	other := Machine{ID: "w", Name: "win", OS: "windows", Tools: tools}
	remote := &countExec{}
	d := &Dispatcher{Machines: func() []Machine { return []Machine{other} }, Remote: func(Machine) Executor { return remote }}
	r := d.RunJob(context.Background(), JobSpec{Name: "up", Job: jobWith("ubuntu-latest"), SecretValues: []string{"abcd1234"}})
	if r.Status != Blocked || !strings.Contains(r.Reason, "needs secrets, which stay on this machine") || remote.ran != 0 {
		t.Fatalf("got %+v, remote ran %d", r, remote.ran)
	}
	// Without secrets the same job goes to the remote machine.
	if r := d.RunJob(context.Background(), JobSpec{Name: "up", Job: jobWith("ubuntu-latest")}); r.Status != Pass || remote.ran != 1 {
		t.Fatalf("plain job: %+v ran %d", r, remote.ran)
	}
	// A local OS that does not fit runs-on blocks too.
	host := Machine{Name: "mac", OS: "darwin", Tools: tools, Host: true}
	d = &Dispatcher{Machines: func() []Machine { return []Machine{host, other} }, Remote: func(Machine) Executor { return remote }}
	r = d.RunJob(context.Background(), JobSpec{Name: "up", Job: jobWith("windows-latest"), SecretEnv: map[string]string{"K": "abcd1234"}})
	if r.Status != Blocked || remote.ran != 1 {
		t.Fatalf("os mismatch: %+v ran %d", r, remote.ran)
	}
}

// seqExec answers per machine: a canned result for each call.
type seqExec struct {
	res  []Result
	runs int
}

func (s *seqExec) RunJob(ctx context.Context, sp JobSpec) Result {
	r := s.res[s.runs]
	s.runs++
	r.Job = sp.Name
	return r
}

func twoRemotes(ex map[string]*seqExec) *Dispatcher {
	tools := []string{"bash", "node"}
	ms := []Machine{{ID: "a", Name: "one", OS: "linux", Tools: tools}, {ID: "b", Name: "two", OS: "linux", Tools: tools}}
	return &Dispatcher{Machines: func() []Machine { return ms }, Remote: func(m Machine) Executor { return ex[m.Name] }}
}

func offlineRes() Result { return Result{Status: Fail, Reason: "machine went offline", Offline: true} }

func TestDispatcherRetriesOfflineMachineElsewhere(t *testing.T) {
	// Equal load picks the first listed machine, which drops.
	one, two := &seqExec{res: []Result{offlineRes()}}, &seqExec{res: []Result{{Status: Pass}}}
	d := twoRemotes(map[string]*seqExec{"one": one, "two": two})
	log := filepath.Join(t.TempDir(), "job.log")
	r := d.RunJob(context.Background(), JobSpec{Name: "b", Job: jobWith("ubuntu-latest"), Log: log})
	if r.Status != Pass || r.Machine != "two" || one.runs != 1 || two.runs != 1 {
		t.Fatalf("got %+v, runs %d/%d", r, one.runs, two.runs)
	}
	b, _ := os.ReadFile(log)
	if want := "::retry machine one went offline; running on two"; !strings.Contains(string(b), want) {
		t.Errorf("log = %q, want %q", b, want)
	}
	for k, n := range d.busy {
		if n != 0 {
			t.Errorf("busy[%s] = %d", k, n)
		}
	}
}

func TestDispatcherOfflineSingleMachineIsBlocked(t *testing.T) {
	one := &seqExec{res: []Result{offlineRes()}}
	tools := []string{"bash", "node"}
	m := Machine{ID: "a", Name: "one", OS: "linux", Tools: tools}
	d := &Dispatcher{Machines: func() []Machine { return []Machine{m} }, Remote: func(Machine) Executor { return one }}
	r := d.RunJob(context.Background(), JobSpec{Name: "b", Job: jobWith("ubuntu-latest")})
	if r.Status != Blocked || !strings.Contains(r.Reason, "machine one went offline; no other machine can run it") || one.runs != 1 {
		t.Fatalf("got %+v runs %d", r, one.runs)
	}
	if n := d.busy["a/one"]; n != 0 {
		t.Errorf("busy = %d", n)
	}
}

func TestDispatcherBothOfflineIsBlockedAfterTwoTries(t *testing.T) {
	one, two := &seqExec{res: []Result{offlineRes(), offlineRes()}}, &seqExec{res: []Result{offlineRes(), offlineRes()}}
	d := twoRemotes(map[string]*seqExec{"one": one, "two": two})
	r := d.RunJob(context.Background(), JobSpec{Name: "b", Job: jobWith("ubuntu-latest")})
	if r.Status != Blocked || !strings.Contains(r.Reason, "went offline") || !strings.HasPrefix(r.Reason, "machines ") || one.runs+two.runs != 2 {
		t.Fatalf("got %+v, runs %d", r, one.runs+two.runs)
	}
	for k, n := range d.busy {
		if n != 0 {
			t.Errorf("busy[%s] = %d", k, n)
		}
	}
}

func TestDispatcherDoesNotRetryTestFailureOrInfra(t *testing.T) {
	for name, res := range map[string]Result{
		"test failure": {Status: Fail, Reason: "step failed: go test"},
		"infra":        {Status: Fail, Reason: "dart crashed", Infra: true},
	} {
		one, two := &seqExec{res: []Result{res, res}}, &seqExec{res: []Result{res, res}}
		d := twoRemotes(map[string]*seqExec{"one": one, "two": two})
		r := d.RunJob(context.Background(), JobSpec{Name: "b", Job: jobWith("ubuntu-latest")})
		if r.Status != Fail || one.runs+two.runs != 1 {
			t.Errorf("%s: got %+v, runs %d", name, r, one.runs+two.runs)
		}
	}
}

func TestRouteNeedToolsUnsupported(t *testing.T) {
	m := Machine{Name: "box", OS: "linux", Tools: []string{"bash", "node"}, Host: true}
	j := &Job{RunsOn: "ubuntu-latest", NeedTools: []string{"flutter"}}
	_, why := Route(j, []Machine{m}, noLoad, false)
	if !strings.Contains(why, "lacks flutter") {
		t.Fatalf("got %q", why)
	}
}

func TestRouteSkipsMachineInUse(t *testing.T) {
	tools := []string{"bash", "node"}
	busy := Machine{Name: "desk", OS: "linux", Tools: tools, Host: true, InUse: true}
	free := Machine{ID: "f", Name: "other", OS: "linux", Tools: tools}
	j := jobWith("ubuntu-latest")
	if m, why := Route(j, []Machine{busy, free}, noLoad, false); why != "" || m.Name != "other" {
		t.Fatalf("got %v %q", m.Name, why)
	}
	_, why := Route(j, []Machine{busy}, noLoad, false)
	if !strings.Contains(why, "desk is in use") {
		t.Fatalf("want the in-use reason, got %q", why)
	}
}
