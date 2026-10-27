package cli

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func newCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Run a repo's ci.yml on this machine and report back on the pull request",
	}
	cmd.AddCommand(newCIRunCmd(), newCIWatchCmd(), newCIConfigCmd())
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
			}
			if jobs != "" {
				o.Jobs = strings.Split(jobs, ",")
			}
			rep, err := localci.Run(ctx(cmd), o)
			if err != nil {
				return err
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
						} else if _, err := localci.Run(sig, localci.Options{
							Client: c, Owner: owner, Repo: repo, PR: pr.Number, DryRun: dry,
							Settings: set, Login: me.Login, Out: out,
						}); err != nil {
							fmt.Fprintf(out, "%s #%d: %v\n", full, pr.Number, err)
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
