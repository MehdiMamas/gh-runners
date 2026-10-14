package daemon

import (
	"runtime"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func TestJobMatches(t *testing.T) {
	ours := []string{"self-hosted", "macOS", "ARM64", "gh-runners", "box"}
	cases := []struct {
		job  []string
		want bool
	}{
		{[]string{"self-hosted"}, true},
		{[]string{"self-hosted", "macos", "arm64"}, true},
		{[]string{"self-hosted", "gh-runners"}, true},
		{[]string{"self-hosted", "Linux"}, false},
		{[]string{"ubuntu-latest"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := JobMatches(c.job, ours); got != c.want {
			t.Errorf("JobMatches(%v) = %v, want %v", c.job, got, c.want)
		}
	}
}

func TestMachineLabels(t *testing.T) {
	got := MachineLabels(store.Config{Labels: []string{"gh-runners"}, RunnerName: "Box"})
	want := map[string]string{"darwin": "macOS", "linux": "Linux", "windows": "Windows"}[runtime.GOOS]
	if len(got) != 5 || got[0] != "self-hosted" || got[1] != want || got[3] != "gh-runners" || got[4] != "box" {
		t.Errorf("MachineLabels = %v", got)
	}
}
