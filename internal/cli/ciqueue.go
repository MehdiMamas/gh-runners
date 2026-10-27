package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

// errNoDaemon means nothing answered at the daemon's address.
type errNoDaemon struct{ err error }

func (e errNoDaemon) Error() string { return "the gh-runners daemon is not running: " + e.err.Error() }

// queueCI asks the daemon at addr (host:port) to queue a Local CI run. The
// secret is only sent as the dashboard cookie, never printed.
func queueCI(ctx context.Context, addr, secret, repo string, pr int, dry, approve bool) (int, error) {
	body, _ := json.Marshal(map[string]any{"repo": repo, "pr": pr, "dry_run": dry, "approve": approve})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/api/ci/run", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GHR", "1")
	req.AddCookie(&http.Cookie{Name: "ghr_ui", Value: secret})
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, errNoDaemon{err}
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusAccepted {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return 0, fmt.Errorf("the daemon refused the run (%d): %s", res.StatusCode, e.Error)
		}
		return 0, fmt.Errorf("the daemon refused the run (%d)", res.StatusCode)
	}
	var out struct {
		Task int `json:"task"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return 0, fmt.Errorf("unexpected answer from the daemon: %w", err)
	}
	return out.Task, nil
}

func newCIQueueCmd() *cobra.Command {
	var dry, approve bool
	cmd := &cobra.Command{
		Use:   "queue <owner/repo> <pr>",
		Short: "Queue a Local CI run in the running daemon (one at a time, with the auto-runs)",
		Long: `queue hands the run to the gh-runners daemon, which runs Local CI one run at a
time. Unlike ` + "`ci run`" + `, it cannot clash with runs the daemon queued itself.
It needs the daemon to be running (gh-runners ui starts it).`,
		Example: `  gh-runners ci queue owner/repo 12
  gh-runners ci queue owner/repo 12 --dry-run`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, _, err := splitRepo(args[0]); err != nil {
				return err
			}
			n, err := strconv.Atoi(args[1])
			if err != nil || n <= 0 {
				return fmt.Errorf("%q is not a pull request number", args[1])
			}
			info, err := store.LoadDaemonInfo()
			if err != nil || info.Addr == "" {
				return fmt.Errorf("the gh-runners daemon is not running; start it with `gh-runners ui`, or run the PR here without the queue with `gh-runners ci run %s %d`", args[0], n)
			}
			secret, err := store.UISecret()
			if err != nil {
				return err
			}
			id, err := queueCI(ctx(cmd), info.Addr, secret, args[0], n, dry, approve)
			if _, down := err.(errNoDaemon); down {
				return fmt.Errorf("the gh-runners daemon is not reachable at %s; start it with `gh-runners ui`, or run the PR here without the queue with `gh-runners ci run %s %d`", info.Addr, args[0], n)
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Queued as task %d\n", id)
			fmt.Fprintf(out, "Watch it on the dashboard: http://%s/ (open it with `gh-runners ui`)\n", info.Addr)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "run the jobs but post and merge nothing")
	cmd.Flags().BoolVar(&approve, "approve", false, "run even if the author is not on the allowlist or the PR is from a fork (read the changes first)")
	return cmd
}
