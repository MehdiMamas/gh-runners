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
	m, why := Route(j, []Machine{win, mac}, noLoad)
	if why != "" || m.Name != "mac" {
		t.Fatalf("got %v %q", m.Name, why)
	}
	_, why = Route(j, []Machine{win}, noLoad)
	if why == "" || !strings.Contains(why, "emulator") {
		t.Fatalf("want an emulator reason, got %q", why)
	}
	win.EmulatorOK = true
	if m, why := Route(j, []Machine{win}, noLoad); why != "" || m.Name != "win" {
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
	if m, _ := Route(j, []Machine{other, host}, busy); m.Name != "mac" {
		t.Fatalf("flutter job went to %s", m.Name)
	}
	host.Tools = []string{"bash", "node"}
	if m, _ := Route(j, []Machine{other, host}, busy); m.Name != "win" {
		t.Fatalf("host lacks flutter, got %s", m.Name)
	}
}

func TestRouteOSAndLoad(t *testing.T) {
	tools := []string{"bash", "node"}
	mac := Machine{Name: "mac", OS: "darwin", Tools: tools, Host: true}
	win := Machine{ID: "w", Name: "win", OS: "windows", Tools: tools}
	if m, why := Route(jobWith("windows-latest"), []Machine{mac, win}, noLoad); why != "" || m.Name != "win" {
		t.Fatalf("windows job: %v %q", m.Name, why)
	}
	if _, why := Route(jobWith("windows-latest"), []Machine{mac}, noLoad); why == "" {
		t.Fatal("windows job on a mac only should be unsupported")
	}
	busy := func(k string) int {
		if k == "/mac" {
			return 2
		}
		return 0
	}
	if m, _ := Route(jobWith("ubuntu-latest"), []Machine{mac, win}, busy); m.Name != "win" {
		t.Fatalf("least loaded should win, got %s", m.Name)
	}
	if m, _ := Route(jobWith("ubuntu-latest"), []Machine{win, mac}, noLoad); m.Name != "mac" {
		t.Fatalf("tie should go to the host, got %s", m.Name)
	}
}

func TestRouteNoFitIsUnsupported(t *testing.T) {
	mac := Machine{Name: "mac", OS: "darwin", Tools: []string{"bash"}, Host: true}
	_, why := Route(jobWith("ubuntu-latest"), []Machine{mac}, noLoad)
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
	out := ScrubEnv([]string{"JAVA_HOME=/jdk", "GITHUB_TOKEN=x", "RANDOM=1"}, RunEnv{}, nil)
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "JAVA_HOME=/jdk") || strings.Contains(joined, "GITHUB_TOKEN") || strings.Contains(joined, "RANDOM") {
		t.Fatalf("env %v", out)
	}
}
