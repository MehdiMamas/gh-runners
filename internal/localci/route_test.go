package localci

import (
	"context"
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
