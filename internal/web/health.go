package web

import "github.com/MehdiMamas/gh-runners/internal/control"

// runnerHealthView is what the daemon's GitHub health check (#149) says
// about one runner.
type runnerHealthView struct {
	GitHubOffline      bool   `json:"github_offline,omitempty"`
	GitHubOfflineSince string `json:"github_offline_since,omitempty"`
}

func runnerHealthOf(p control.ProcState) runnerHealthView {
	return runnerHealthView{p.GitHubOffline, since(p.GitHubOfflineSince)}
}

// stateHealthView is the machine-wide summary: how many runners GitHub shows
// offline while they run here, and when GitHub was last asked.
type stateHealthView struct {
	GitHubOffline    int    `json:"github_offline"`
	GitHubCheckAt    string `json:"github_check_at,omitempty"`
	GitHubCheckError string `json:"github_check_error,omitempty"`
}

func stateHealthOf(st control.Status) stateHealthView {
	v := stateHealthView{GitHubCheckAt: since(st.GitHubCheck.At), GitHubCheckError: st.GitHubCheck.Err}
	for _, p := range st.Runners {
		if p.GitHubOffline {
			v.GitHubOffline++
		}
	}
	return v
}
