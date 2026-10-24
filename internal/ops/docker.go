package ops

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// DockerLabel is the capability label a runner carries exactly when this
// machine can run Docker, so `runs-on: [self-hosted, gh-runners, docker]`
// finds it without anyone adding the label by hand.
const DockerLabel = "docker"

// DockerAvailable reports whether Docker works here. Tests replace it.
var DockerAvailable = dockerInfoWorks

var dockerCache struct {
	sync.Mutex
	at time.Time
	ok bool
}

// dockerInfoWorks runs `docker info` with a short timeout. The answer is
// kept for a minute because the on-demand loop asks for labels often.
func dockerInfoWorks() bool {
	dockerCache.Lock()
	defer dockerCache.Unlock()
	if !dockerCache.at.IsZero() && time.Since(dockerCache.at) < time.Minute {
		return dockerCache.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "info")
	hideWindow(cmd)
	dockerCache.ok = cmd.Run() == nil
	dockerCache.at = time.Now()
	return dockerCache.ok
}

// WithDockerLabel returns labels without any docker label, plus docker when
// has is true. Other labels are untouched and keep their order.
func WithDockerLabel(labels []string, has bool) []string {
	out := make([]string, 0, len(labels)+1)
	for _, l := range labels {
		if !strings.EqualFold(l, DockerLabel) {
			out = append(out, l)
		}
	}
	if has {
		out = append(out, DockerLabel)
	}
	return out
}

// hasLabel reports whether labels holds l, ignoring case.
func hasLabel(labels []string, l string) bool {
	for _, x := range labels {
		if strings.EqualFold(x, l) {
			return true
		}
	}
	return false
}

// SyncDockerLabel checks Docker now and makes the docker label of every
// runner on this machine match, on GitHub and in the store, through
// SetLabels (the code the `labels` command uses). It returns one line per
// change. Other custom labels are left as they are.
func SyncDockerLabel(ctx context.Context, c *github.Client) ([]string, error) {
	want := DockerAvailable()
	local, err := store.List()
	if err != nil {
		return nil, err
	}
	var done []string
	byScope := map[string][]github.Runner{}
	for _, r := range local {
		if r.Disabled || !r.Configured() {
			continue
		}
		scope := r.Scope()
		gs := github.Scope{Owner: r.Owner, Repo: r.Repo}
		gh, ok := byScope[scope]
		if !ok {
			var err error
			if gh, err = c.ListRunners(ctx, gs); err != nil {
				return done, err
			}
			byScope[scope] = gh
		}
		for _, g := range gh {
			if !strings.EqualFold(g.Name, r.Name) {
				continue
			}
			var custom []string
			for _, l := range g.Labels {
				if l.Type != "read-only" && !IsBuiltinLabel(l.Name) {
					custom = append(custom, l.Name)
				}
			}
			if hasLabel(custom, DockerLabel) == want {
				continue
			}
			if _, err := SetLabels(ctx, c, gs, g.ID, WithDockerLabel(custom, want)); err != nil {
				return done, err
			}
			verb := "removed"
			if want {
				verb = "added"
			}
			done = append(done, verb+" docker label on "+r.Display())
		}
	}
	return done, nil
}
