package cli

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func newCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Run a repo's ci.yml on this machine and report back on the pull request",
	}
	cmd.AddCommand(newCIRunCmd(), newCIWatchCmd(), newCIConfigCmd(), newCIDoctorCmd(), newCIEmulatorCmd())
	return cmd
}

func splitRepo(s string) (string, string, error) {
	o, r, ok := strings.Cut(s, "/")
	if !ok || o == "" || r == "" || strings.Contains(r, "/") {
		return "", "", fmt.Errorf("%q is not owner/repo", s)
	}
	return o, r, nil
}

func ciSettings(repo string) store.CIRepo {
	c, err := store.LoadConfig()
	if err != nil {
		return store.CIRepo{}
	}
	return c.CI[repo]
}

func newCIRunCmd() *cobra.Command {
	var dry, approve bool
	var jobs string
	cmd := &cobra.Command{
		Use:   "run <owner/repo> <pr>",
		Short: "Run the jobs of one pull request here and print the results",
		Example: `  gh-runners ci run owner/repo 12 --dry-run     # run and print, post nothing
  gh-runners ci run owner/repo 12 --jobs backend`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, repo, err := splitRepo(args[0])
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("%q is not a pull request number", args[1])
			}
			c, err := client()
			if err != nil {
				return err
			}
			me, _, _ := c.Me(ctx(cmd))
			o := localci.Options{
				Client: c, Owner: owner, Repo: repo, PR: n, DryRun: dry, Approve: approve,
				Settings: ciSettings(args[0]), Login: me.Login, Out: cmd.OutOrStdout(),
				Exec: control.CIDispatcher(),
			}
			if jobs != "" {
				o.Jobs = strings.Split(jobs, ",")
			}
			start := time.Now()
			rep, err := localci.Run(ctx(cmd), o)
			if herr := localci.AddHistory(localci.NewHistoryRun(o, false, start, rep, err)); herr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not save the run to the dashboard history: %v\n", herr)
			}
			if err != nil {
				return err
			}
			if rep.HasBlocked() {
				return fmt.Errorf("blocked by an infrastructure error (not a test failure); nothing was posted or merged")
			}
			if !rep.AllPassed() {
				return fmt.Errorf("some jobs did not pass")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "run the jobs but post and merge nothing")
	cmd.Flags().BoolVar(&approve, "approve", false, "run even if the author is not on the allowlist or the PR is from a fork (read the changes first)")
	cmd.Flags().StringVar(&jobs, "jobs", "", "only these jobs (comma separated), plus what they need")
	return cmd
}

func newCIWatchCmd() *cobra.Command {
	var dry bool
	cmd := &cobra.Command{
		Use:   "watch <owner/repo>...",
		Short: "Poll the repos and run every new pull request head that passes the trust gate",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			sig, stop := signal.NotifyContext(ctx(cmd), os.Interrupt, syscall.SIGTERM)
			defer stop()
			me, _, _ := c.Me(sig)
			disp := control.CIDispatcher()
			seenPath := filepath.Join(store.Home(), "ci", "seen.json")
			seen := map[string]bool{}
			_ = store.ReadJSON(seenPath, &seen)
			out := cmd.OutOrStdout()
			for {
				for _, full := range args {
					owner, repo, err := splitRepo(full)
					if err != nil {
						return err
					}
					prs, err := c.OpenPullRequests(sig, owner, repo)
					if err != nil {
						fmt.Fprintf(out, "%s: %v\n", full, err)
						continue
					}
					set := ciSettings(full)
					for _, pr := range prs {
						key := fmt.Sprintf("%s#%d@%s", full, pr.Number, pr.Head.SHA)
						if seen[key] || sig.Err() != nil {
							continue
						}
						if err := localci.CheckTrust(pr, localci.Allowlist(set, me.Login), false); err != nil {
							fmt.Fprintln(out, err)
						} else {
							o := localci.Options{
								Client: c, Owner: owner, Repo: repo, PR: pr.Number, DryRun: dry,
								Settings: set, Login: me.Login, Out: out, Exec: disp,
							}
							start := time.Now()
							rep, err := localci.Run(sig, o)
							if err != nil {
								fmt.Fprintf(out, "%s #%d: %v\n", full, pr.Number, err)
							}
							if sig.Err() == nil {
								_ = localci.AddHistory(localci.NewHistoryRun(o, true, start, rep, err))
							}
						}
						seen[key] = true
						_ = store.WriteJSON(seenPath, seen, 0o644)
					}
				}
				select {
				case <-sig.Done():
					return nil
				case <-time.After(60 * time.Second):
				}
			}
		},
	}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "run the jobs but post and merge nothing")
	return cmd
}

func newCIConfigCmd() *cobra.Command {
	var post, merge, allow string
	cmd := &cobra.Command{
		Use:   "config <owner/repo>",
		Short: "Show or change whether results are posted and approved PRs merged (both off by default)",
		Example: `  gh-runners ci config owner/repo
  gh-runners ci config owner/repo --post on --allow alice,bob`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, _, err := splitRepo(args[0]); err != nil {
				return err
			}
			cfg, err := store.LoadConfig()
			if err != nil {
				return err
			}
			s := cfg.CI[args[0]]
			changed := false
			for _, f := range []struct {
				v   string
				dst *bool
			}{{post, &s.Post}, {merge, &s.Merge}} {
				switch f.v {
				case "":
				case "on":
					*f.dst, changed = true, true
				case "off":
					*f.dst, changed = false, true
				default:
					return fmt.Errorf("use on or off, not %q", f.v)
				}
			}
			if cmd.Flags().Changed("allow") {
				s.Allow, changed = nil, true
				for _, a := range strings.Split(allow, ",") {
					if a = strings.TrimSpace(a); a != "" {
						s.Allow = append(s.Allow, a)
					}
				}
			}
			if changed {
				if cfg.CI == nil {
					cfg.CI = map[string]store.CIRepo{}
				}
				cfg.CI[args[0]] = s
				if err := store.SaveConfig(cfg); err != nil {
					return err
				}
			}
			who := "only you"
			if len(s.Allow) > 0 {
				who = strings.Join(s.Allow, ", ")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: post results %s, merge approved PRs %s, authors allowed: %s\n", args[0], onOffWord(s.Post), onOffWord(s.Merge), who)
			return nil
		},
	}
	cmd.Flags().StringVar(&post, "post", "", "on|off: post statuses and a comment on the PR")
	cmd.Flags().StringVar(&merge, "merge", "", "on|off: squash-merge when every job passed and the PR is approved")
	cmd.Flags().StringVar(&allow, "allow", "", "comma separated authors whose PRs run on their own (default: you)")
	return cmd
}

func onOffWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func newCIEmulatorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "emulator [on|off]",
		Short: "Show or set whether this machine may run the jobs that start the Firebase emulators",
		Long: `Jobs that set up Java start the Firebase emulators on fixed ports, so only one
may run at a time on a machine. macOS and Linux allow them by default; Windows
does not until you turn this on.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := store.LoadConfig()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				var v bool
				switch args[0] {
				case "on":
					v = true
				case "off":
				default:
					return fmt.Errorf("use on or off, not %q", args[0])
				}
				cfg.CIEmulator = &v
				if err := store.SaveConfig(cfg); err != nil {
					return err
				}
				_ = control.Fleet() // tell a running daemon; fine if none runs
			}
			fmt.Fprintf(cmd.OutOrStdout(), "emulator jobs on this machine: %s\n", onOffWord(cfg.CIEmulatorOK()))
			return nil
		},
	}
}

func newCIDoctorCmd() *cobra.Command {
	var ref string
	cmd := &cobra.Command{
		Use:   "doctor [owner/repo]",
		Short: "Show the tools found on this machine and what each job of a repo's ci.yml still needs",
		Example: `  gh-runners ci doctor
  gh-runners ci doctor owner/repo`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			cfg, _ := store.LoadConfig()
			have := localci.DetectTools()
			bash, bashErr := localci.BashPath()
			fmt.Fprintf(out, "This machine: %s/%s\n\n", runtime.GOOS, runtime.GOARCH)
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "TOOL\tFOUND\tNOTE")
			for _, t := range localci.ToolNames {
				found, note := "no", ""
				for _, h := range have {
					if h == t {
						found = "yes"
					}
				}
				if t == "bash" {
					if bashErr == nil {
						note = bash
					} else {
						note = bashErr.Error()
					}
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", t, found, note)
			}
			tw.Flush()
			fmt.Fprintf(out, "\nEmulator jobs allowed here: %s (change with `gh-runners ci emulator on|off`)\n", onOffWord(cfg.CIEmulatorOK()))
			if len(args) == 0 {
				return nil
			}
			owner, repo, err := splitRepo(args[0])
			if err != nil {
				return err
			}
			c, err := client()
			if err != nil {
				return err
			}
			if ref == "" {
				r, err := c.GetRepo(ctx(cmd), owner, repo)
				if err != nil {
					return err
				}
				ref = r.DefaultBranch
			}
			data, err := c.FileAt(ctx(cmd), owner, repo, localci.WorkflowPath, ref)
			if err != nil {
				return fmt.Errorf("read %s of %s: %w", localci.WorkflowPath, args[0], err)
			}
			wf, err := localci.ParseWorkflow(data)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "\nJobs in %s (%s):\n", args[0], ref)
			tw = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "JOB\tNEEDS\tMISSING HERE")
			allOK := true
			for _, name := range wf.Order {
				j := wf.Jobs[name]
				need, emu := j.Requires()
				missing := localci.Missing(have, need)
				if emu && !cfg.CIEmulatorOK() {
					missing = append(missing, "emulator permission")
				}
				sort.Strings(missing)
				m := "nothing"
				if len(missing) > 0 {
					m, allOK = strings.Join(missing, ", "), false
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", name, strings.Join(need, ", "), m)
			}
			tw.Flush()
			if allOK {
				fmt.Fprintln(out, "\nThis machine can run every job.")
			} else {
				fmt.Fprintln(out, "\nInstall what is missing, or the jobs go to another machine.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "branch or sha to read ci.yml from (default: the default branch)")
	return cmd
}
