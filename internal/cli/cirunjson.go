package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/MehdiMamas/gh-runners/internal/localci"
)

type ciJobJSON struct {
	Name       string `json:"name"`
	Machine    string `json:"machine"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	DurationMS int64  `json:"duration_ms"`
	Log        string `json:"log"`
}

type ciRunJSON struct {
	Repo string      `json:"repo"`
	PR   int         `json:"pr"`
	SHA  string      `json:"sha"`
	Jobs []ciJobJSON `json:"jobs"`
}

// writeCIJSON prints the machine-readable summary of a run.
func writeCIJSON(w io.Writer, repo string, rep localci.Report) error {
	s := ciRunJSON{Repo: repo, PR: rep.PR.Number, SHA: rep.PR.Head.SHA, Jobs: []ciJobJSON{}}
	for _, r := range rep.Results {
		s.Jobs = append(s.Jobs, ciJobJSON{
			Name: r.Job, Machine: r.Machine, Status: string(r.Status), Reason: r.Reason,
			DurationMS: r.Duration.Milliseconds(), Log: r.Log,
		})
	}
	return json.NewEncoder(w).Encode(s)
}

// ciVerdict is nil only when every job passed.
func ciVerdict(rep localci.Report) error {
	if rep.HasBlocked() {
		return fmt.Errorf("blocked by an infrastructure error (not a test failure); nothing was posted or merged")
	}
	if !rep.AllPassed() {
		return fmt.Errorf("some jobs did not pass")
	}
	return nil
}
