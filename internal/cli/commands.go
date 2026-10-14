package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/daemon"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/ops"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/shortcut"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func newAddCmd() *cobra.Command {
	var o ops.AddOptions
	var org bool
	var labels string
	cmd := &cobra.Command{
		Use:   "add [owner/repo | org]",
		Short: "Register a runner on this machine for a repo (default: the repo in the current directory)",
		Example: `  gh-runners add                      # the repo of the current directory
  gh-runners add MehdiMamas/spotter
  gh-runners add --org my-org         # one runner for every repo of an org
  gh-runners add owner/repo --labels gpu,big`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := scopeFromArgs(args, org)
			if err != nil {
				return err
			}
			c, err := client()
			if err != nil && o.RegistrationToken == "" {
				return err
			}
			if c == nil {
				c = github.New("") // registration token only: anonymous downloads
			}
			o.Labels, o.Source = splitList(labels), "add"
			r, err := ops.Add(ctx(cmd), c, scope, o, os.Stdout)
			if errors.Is(err, ops.ErrExists) {
				fmt.Println(err)
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Printf("\nRunner %q is registered for %s.\n", r.Name, r.Scope())
			afterChange()
			fmt.Printf("Use it in a workflow with:\n  runs-on: [self-hosted, %s]\n", strings.Join(append([]string{osLabel()}, r.Labels...), ", "))
			return nil
		},
	}
	cmd.Flags().BoolVar(&org, "org", false, "register an organization runner")
	cmd.Flags().StringVar(&labels, "labels", "", "extra labels, comma separated")
	cmd.Flags().StringVar(&o.Group, "group", "", "runner group (organizations)")
	cmd.Flags().StringVar(&o.Name, "name", "", "runner name (default: this machine's hostname)")
	cmd.Flags().BoolVar(&o.AllowPublic, "allow-public", false, "allow a public repository (unsafe)")
	cmd.Flags().BoolVar(&o.Force, "force", false, "re-register even if it already exists")
	cmd.Flags().StringVar(&o.RegistrationToken, "registration-token", "", "register with a token from the repo's Settings > Actions > Runners page (no GitHub login needed)")
	return cmd
}

func osLabel() string {
	return daemon.MachineLabels(store.Config{})[1]
}

// afterChange tells the user whether something will actually run the runner.
func afterChange() {
	if _, err := control.GetStatus(); err == nil {
		fmt.Println("The gh-runners daemon picked it up and is running it.")
		return
	}
	if service.Get().Installed {
		fmt.Println("Starting the gh-runners daemon ...")
		if err := service.Start(); err != nil {
			fmt.Println("  could not start it:", err)
		}
		return
	}
	fmt.Println("Nothing runs it yet. Run `gh-runners service install` once on this machine to start runners now and at every login (or turn on \"Start at login\" in `gh-runners ui`).")
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newRemoveCmd() *cobra.Command {
	var force, all bool
	cmd := &cobra.Command{
		Use:     "remove [owner/repo | org]",
		Aliases: []string{"rm"},
		Short:   "Unregister and delete a runner on this machine (default: the repo in the current directory)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var targets []store.Runner
			var err error
			switch {
			case all:
				targets, err = store.List()
			case len(args) == 1:
				targets, err = store.Find(args[0])
			default:
				var scope github.Scope
				if scope, err = scopeFromArgs(nil, false); err == nil {
					targets, err = store.Find(scope.String())
				}
			}
			if err != nil {
				return err
			}
			c, err := client()
			if err != nil {
				return err
			}
			var failed int
			for _, r := range targets {
				if err := ops.Remove(ctx(cmd), c, r, force, os.Stdout); err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
					failed++
					continue
				}
				fmt.Printf("Removed %s.\n", r.Scope())
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d removals failed", failed, len(targets))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "delete local files even if GitHub cannot be updated")
	cmd.Flags().BoolVar(&all, "all", false, "remove every runner on this machine")
	return cmd
}

func newStartStopCmd(start bool) *cobra.Command {
	use, short := "stop <owner/repo | org>", "Stop a runner on this machine (it stays registered)"
	if start {
		use, short = "start <owner/repo | org>", "Start a stopped runner again"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rs, err := store.Find(args[0])
			if err != nil {
				return err
			}
			for _, r := range rs {
				if err := ops.SetDisabled(r, !start); err != nil {
					return err
				}
				if !start {
					_ = control.WaitStopped(r.ID, 90*time.Second)
				}
				fmt.Printf("%s: %s\n", r.Scope(), map[bool]string{true: "started", false: "stopped"}[start])
			}
			if start {
				afterChange()
			}
			return nil
		},
	}
}

type listRow struct {
	Scope   string   `json:"scope"`
	Name    string   `json:"name"`
	Local   string   `json:"local"`
	GitHub  string   `json:"github"`
	Labels  []string `json:"labels"`
	Dir     string   `json:"dir"`
	LastErr string   `json:"last_exit,omitempty"`
}

func newListCmd() *cobra.Command {
	var asJSON, offline bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "status"},
		Short:   "Show the runners on this machine and their state locally and on GitHub",
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, st, derr := collectRows(cmd, offline)
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			if derr != nil {
				fmt.Println("Daemon: not running. `gh-runners service install` starts it now and at login.")
			} else {
				fmt.Printf("Daemon: running (pid %d, %s, up %s)\n", st.PID, st.Version, time.Since(st.Started).Round(time.Second))
				if st.OnDemand != "" && st.OnDemand != "off" {
					fmt.Printf("On-demand: %s\n", st.OnDemand)
				}
				if st.SyncErr != "" {
					fmt.Printf("Last sync failed: %s\n", st.SyncErr)
				}
			}
			if len(rows) == 0 {
				fmt.Println("\nNo runners yet. Add one with `gh-runners add owner/repo` (or `gh-runners` for the menu).")
				return nil
			}
			fmt.Println()
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SCOPE\tNAME\tLOCAL\tGITHUB\tLABELS")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Scope, r.Name, r.Local, r.GitHub, strings.Join(r.Labels, ","))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().BoolVar(&offline, "offline", false, "do not ask GitHub for runner status")
	return cmd
}

func collectRows(cmd *cobra.Command, offline bool) ([]listRow, control.Status, error) {
	runners, _ := store.List()
	st, derr := control.GetStatus()
	var c *github.Client
	if !offline {
		c, _ = client()
	}
	remote := map[string]map[string]github.Runner{}
	rows := []listRow{}
	for _, r := range runners {
		row := listRow{Scope: r.Scope(), Name: r.Name, Labels: r.Labels, Dir: r.Dir(), GitHub: "?"}
		row.Local, row.LastErr = ops.LocalState(r, st, derr == nil)
		if c != nil {
			byName, seen := remote[r.Scope()]
			if !seen {
				byName = map[string]github.Runner{}
				list, err := c.ListRunners(ctx(cmd), github.Scope{Owner: r.Owner, Repo: r.Repo})
				if err != nil {
					byName = nil
				}
				for _, gr := range list {
					byName[gr.Name] = gr
				}
				remote[r.Scope()] = byName
			}
			switch gr, ok := byName[r.Name]; {
			case byName == nil:
				row.GitHub = "error"
			case !ok:
				row.GitHub = "missing"
			case gr.Busy:
				row.GitHub = "busy"
			default:
				row.GitHub = gr.Status
			}
		}
		rows = append(rows, row)
	}
	return rows, st, derr
}

func newSyncCmd() *cobra.Command {
	var dry, yes bool
	var auto string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Give every one of your private repos a runner on this machine",
		Long: `sync lists the repositories you administer, keeps the ones that match the
"sync" rules in config.json (default: private, non-fork, non-archived repos
you own) and adds a runner for each one that does not have one yet.

--auto on makes the daemon do this every hour, so new repos get a runner
without you doing anything.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := store.LoadConfig()
			if err != nil {
				return err
			}
			if auto != "" {
				cfg.Sync.Auto = auto == "on"
				if err := store.SaveConfig(cfg); err != nil {
					return err
				}
				fmt.Printf("Auto-sync %s (every %s, by the daemon).\n", auto, cfg.Sync.Interval.Or(time.Hour))
				if cfg.Sync.Auto {
					_ = control.Sync()
					afterChange()
				}
				return nil
			}
			c, err := client()
			if err != nil {
				return err
			}
			plan, err := ops.PlanSync(ctx(cmd), c, cfg.Sync)
			if err != nil {
				return err
			}
			fmt.Printf("%d repos already have a runner here.\n", plan.Have)
			for _, r := range plan.Add {
				fmt.Printf("  + %s\n", r.FullName)
			}
			for _, r := range plan.Prune {
				fmt.Printf("  - %s\n", r.Scope())
			}
			if len(plan.Add)+len(plan.Prune) == 0 {
				fmt.Println("Nothing to do.")
				return nil
			}
			if dry {
				return nil
			}
			if !yes && !confirm(fmt.Sprintf("Apply %d additions and %d removals?", len(plan.Add), len(plan.Prune))) {
				return nil
			}
			ops.ApplySync(ctx(cmd), c, &plan, os.Stdout)
			afterChange()
			if len(plan.Errors) > 0 {
				return fmt.Errorf("%d repos failed:\n  %s", len(plan.Errors), strings.Join(plan.Errors, "\n  "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "only show what would change")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().StringVar(&auto, "auto", "", "on|off: let the daemon sync every hour")
	return cmd
}

func confirm(q string) bool {
	if !interactive() {
		return false
	}
	fmt.Printf("%s [y/N] ", q)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func newOnDemandCmd() *cobra.Command {
	var max int
	var owners string
	cmd := &cobra.Command{
		Use:       "on-demand <on|off|status>",
		Short:     "Start a one-job runner whenever one of your repos queues a matching job",
		ValidArgs: []string{"on", "off", "status"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		Long: `On-demand mode needs no per-repo setup at all. The daemon watches your
repositories (same rules as sync, recently pushed ones only) for queued jobs
whose runs-on labels this machine has, and starts a just-in-time runner that
takes one job and unregisters itself. Idle cost: no runner processes.

Target it from a workflow with labels this machine has, for example:
  runs-on: [self-hosted, macOS]`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := store.LoadConfig()
			if err != nil {
				return err
			}
			switch args[0] {
			case "status":
				st, err := control.GetStatus()
				if err != nil {
					return err
				}
				fmt.Printf("on-demand: %s\nlabels this machine answers to: %s\n", st.OnDemand, strings.Join(daemon.MachineLabels(cfg), ", "))
				return nil
			case "on":
				cfg.OnDemand.Enabled = true
			case "off":
				cfg.OnDemand.Enabled = false
			}
			if cmd.Flags().Changed("max") {
				cfg.OnDemand.MaxConcurrent = max
			}
			if owners != "" {
				cfg.OnDemand.Owners = splitList(owners)
			}
			if err := store.SaveConfig(cfg); err != nil {
				return err
			}
			fmt.Printf("On-demand mode %s.\n", args[0])
			if cfg.OnDemand.Enabled {
				fmt.Printf("This machine answers to: %s\n", strings.Join(daemon.MachineLabels(cfg), ", "))
				afterChange()
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&max, "max", 2, "most on-demand runners at once on this machine")
	cmd.Flags().StringVar(&owners, "owners", "", "users/orgs whose repos to watch, comma separated (default: you)")
	return cmd
}

func newServiceCmd() *cobra.Command {
	var keep bool
	uninstall := &cobra.Command{Use: "uninstall", Short: "Stop the daemon and remove the login entry", RunE: func(*cobra.Command, []string) error {
		if keep {
			if err := service.UninstallKeepRunning(); err != nil {
				return err
			}
			fmt.Println("Removed the login entry. The daemon keeps running until you log out or stop it.")
			return nil
		}
		if err := service.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Uninstalled. Runners stay registered; `gh-runners service install` brings them back.")
		return nil
	}}
	uninstall.Flags().BoolVar(&keep, "keep-running", false, "only remove the login entry; leave the daemon running for now")
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Start the daemon at login (launchd, systemd --user, or the Windows Run key)",
	}
	cmd.AddCommand(
		&cobra.Command{Use: "install", Short: "Start the daemon now and at every login", RunE: func(*cobra.Command, []string) error {
			where, err := service.Install()
			if err != nil {
				return err
			}
			fmt.Printf("Installed (%s). The daemon is running.\n", where)
			if lnk, err := shortcut.Install(); err == nil {
				fmt.Printf("Open the dashboard from %s or with `gh-runners ui`.\n", lnk)
			}
			return nil
		}},
		uninstall,
		&cobra.Command{Use: "start", Short: "Start the daemon now", RunE: func(*cobra.Command, []string) error {
			if err := service.Start(); err != nil {
				return err
			}
			fmt.Println("The daemon is running.")
			return nil
		}},
		&cobra.Command{Use: "stop", Short: "Stop the daemon and every runner until the next start or login", RunE: func(*cobra.Command, []string) error {
			if err := service.Stop(); err != nil {
				return err
			}
			fmt.Println("Stopped.")
			return nil
		}},
		&cobra.Command{Use: "restart", Short: "Stop and start the daemon (after an update, for example)", RunE: func(*cobra.Command, []string) error {
			if err := service.Restart(); err != nil {
				return err
			}
			fmt.Println("The daemon is running.")
			return nil
		}},
		&cobra.Command{Use: "status", Short: "Show whether the daemon is installed and running", RunE: func(*cobra.Command, []string) error {
			s := service.Get()
			fmt.Printf("installed: %v (%s)\nrunning:   %v", s.Installed, s.Where, s.Running)
			if s.Running {
				fmt.Printf(" (pid %d)", s.PID)
			}
			fmt.Println()
			return nil
		}},
	)
	return cmd
}

func newDaemonCmd() *cobra.Command {
	var detach bool
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the supervisor in the foreground (the service runs this)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if detach {
				return service.StartDetached()
			}
			lf, err := openDaemonLog()
			if err != nil {
				return err
			}
			defer lf.Close()
			var w io.Writer = lf
			if interactive() {
				w = io.MultiWriter(lf, os.Stderr)
			}
			ctx, stop := signal.NotifyContext(ctx(cmd), os.Interrupt, syscall.SIGTERM)
			defer stop()
			d := &daemon.Daemon{Version: Version, Log: newLogger(w)}
			return d.Run(ctx)
		},
	}
	cmd.Flags().BoolVar(&detach, "detach", false, "start in the background and return")
	return cmd
}

func newLogsCmd() *cobra.Command {
	var follow, daemonLog bool
	var lines int
	cmd := &cobra.Command{
		Use:   "logs [owner/repo | org]",
		Short: "Show a runner's output (or the daemon's with --daemon)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := store.DaemonLog()
			if !daemonLog {
				var q string
				if len(args) == 1 {
					q = args[0]
				} else {
					scope, err := scopeFromArgs(nil, false)
					if err != nil {
						return fmt.Errorf("%v (or use --daemon)", err)
					}
					q = scope.String()
				}
				rs, err := store.Find(q)
				if err != nil {
					return err
				}
				path = rs[0].LogPath()
			}
			return tail(ctx(cmd), path, lines, follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	cmd.Flags().BoolVar(&daemonLog, "daemon", false, "show the daemon log")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "number of lines to show")
	return cmd
}

func newLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Save a personal access token, for machines without the GitHub CLI",
		Long: `gh-runners uses, in order: $GH_RUNNERS_TOKEN, $GH_TOKEN, $GITHUB_TOKEN,
the token saved here (or by signing in from the dashboard), then the GitHub
CLI's token (gh auth token).

Create a classic token with the "repo" scope (add "admin:org" for org runners)
at https://github.com/settings/tokens/new?scopes=repo&description=gh-runners`,
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := promptSecret("Paste a GitHub token (repo scope): ")
			if err != nil {
				return err
			}
			token = strings.TrimSpace(token)
			me, _, err := github.New(token).Me(ctx(cmd))
			if err != nil {
				return err
			}
			if err := os.MkdirAll(store.Home(), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(store.TokenPath(), []byte(token+"\n"), 0o600); err != nil {
				return err
			}
			fmt.Printf("Saved a token for %s in %s.\n", me.Login, store.TokenPath())
			return nil
		},
	}
}

func newConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the config file path and contents",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := store.LoadConfig()
			if err != nil {
				return err
			}
			if _, err := os.Stat(store.ConfigPath()); err != nil {
				if err := store.SaveConfig(cfg); err != nil {
					return err
				}
			}
			b, _ := json.MarshalIndent(cfg, "", "  ")
			fmt.Printf("# %s\n%s\n", store.ConfigPath(), b)
			return nil
		},
	}
}
