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
func queueCI(ctx context.Context, addr, secret, repo string, pr int, dry, approve bool) (int, bool, error) {
	body, _ := json.Marshal(map[string]any{"repo": repo, "pr": pr, "dry_run": dry, "approve": approve})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/api/ci/run", bytes.NewReader(body))
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GHR", "1")
	req.AddCookie(&http.Cookie{Name: "ghr_ui", Value: secret})
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, false, errNoDaemon{err}
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusAccepted {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return 0, false, fmt.Errorf("the daemon refused the run (%d): %s", res.StatusCode, e.Error)
		}
		return 0, false, fmt.Errorf("the daemon refused the run (%d)", res.StatusCode)
	}
	var out struct {
		Task     int  `json:"task"`
		Existing bool `json:"existing"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return 0, false, fmt.Errorf("unexpected answer from the daemon: %w", err)
	}
	return out.Task, out.Existing, nil
}

// ciTaskCall sends one request about a queued task (move it next, or cancel
// it) to the daemon at addr.
func ciTaskCall(ctx context.Context, addr, secret, method, path string) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://"+addr+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-GHR", "1")
	req.AddCookie(&http.Cookie{Name: "ghr_ui", Value: secret})
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return errNoDaemon{err}
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("the daemon refused (%d): %s", res.StatusCode, e.Error)
		}
		return fmt.Errorf("the daemon refused (%d)", res.StatusCode)
	}
	return nil
}

// ciTaskCmd runs ciTaskCall for the task id in arg, finding the daemon first.
func ciTaskCmd(cmd *cobra.Command, arg, method, suffix, done string) error {
	id, err := strconv.Atoi(arg)
	if err != nil || id <= 0 {
		return fmt.Errorf("%q is not a task number (the number `ci queue` printed)", arg)
	}
	info, err := store.LoadDaemonInfo()
	if err != nil || info.Addr == "" {
		return fmt.Errorf("the gh-runners daemon is not running; start it with `gh-runners ui`")
	}
	secret, err := store.UISecret()
	if err != nil {
		return err
	}
	err = ciTaskCall(ctx(cmd), info.Addr, secret, method, fmt.Sprintf("/api/ci/tasks/%d%s", id, suffix))
	if _, down := err.(errNoDaemon); down {
		return fmt.Errorf("the gh-runners daemon is not reachable at %s; start it with `gh-runners ui`", info.Addr)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Task %d %s\n", id, done)
	return nil
}

func newCICancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "cancel <task>",
		Short:   "Cancel a queued or running Local CI run in the daemon",
		Example: `  gh-runners ci cancel 7`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ciTaskCmd(cmd, args[0], http.MethodDelete, "", "is cancelled")
		},
	}
}

func newCIQueueCmd() *cobra.Command {
	var dry, approve bool
	var next string
	cmd := &cobra.Command{
		Use:   "queue <owner/repo> <pr> | --next <task>",
		Short: "Queue a Local CI run in the running daemon (one at a time, with the auto-runs)",
		Long: `queue hands the run to the gh-runners daemon, which runs Local CI one run at a
time. Unlike ` + "`ci run`" + `, it cannot clash with runs the daemon queued itself.
It needs the daemon to be running (gh-runners ui starts it).`,
		Example: `  gh-runners ci queue owner/repo 12
  gh-runners ci queue owner/repo 12 --dry-run
  gh-runners ci queue --next 7   # run queued task 7 before the others`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if next != "" {
				if len(args) != 0 {
					return fmt.Errorf("--next takes only the task number")
				}
				return ciTaskCmd(cmd, next, http.MethodPost, "/next", "will run next")
			}
			if len(args) != 2 {
				return fmt.Errorf("give a repository and a pull request number, or --next <task>")
			}
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
			id, existing, err := queueCI(ctx(cmd), info.Addr, secret, args[0], n, dry, approve)
			if _, down := err.(errNoDaemon); down {
				return fmt.Errorf("the gh-runners daemon is not reachable at %s; start it with `gh-runners ui`, or run the PR here without the queue with `gh-runners ci run %s %d`", info.Addr, args[0], n)
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if existing {
				fmt.Fprintf(out, "Already queued as task %d\n", id)
			} else {
				fmt.Fprintf(out, "Queued as task %d\n", id)
			}
			fmt.Fprintf(out, "Watch it on the dashboard: http://%s/ (open it with `gh-runners ui`)\n", info.Addr)
			return nil
		},
	}
	cmd.Flags().StringVar(&next, "next", "", "move the queued task with this number to the front of the queue")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "run the jobs but post and merge nothing")
	cmd.Flags().BoolVar(&approve, "approve", false, "run even if the author is not on the allowlist or the PR is from a fork (read the changes first)")
	return cmd
}
