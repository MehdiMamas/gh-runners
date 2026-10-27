// Package cli defines the gh-runners commands.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// Version is set at build time with -ldflags "-X .../cli.Version=v1.2.3".
var Version = "dev"

// Execute runs the CLI.
func Execute() int {
	root := newRoot()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "gh-runners",
		Short: "Self-hosted GitHub Actions runners on your own machines, without the busywork",
		Long: `gh-runners registers GitHub Actions self-hosted runners on this machine and
keeps them running: one click or command per repo (or none, with sync or
on-demand mode), a background daemon that restarts them, and login
autostart on macOS, Linux and Windows.

Run it with no arguments to open the dashboard in your browser, or use
` + "`gh-runners menu`" + ` for a menu in the terminal.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return newUICmd().RunE(cmd, args)
		},
	}
	var home string
	root.PersistentFlags().StringVar(&home, "home", "", "state directory (default ~/.gh-runners, or $GH_RUNNERS_HOME)")
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if home != "" {
			os.Setenv("GH_RUNNERS_HOME", home)
		}
	}
	root.AddCommand(
		newAddCmd(), newRemoveCmd(), newListCmd(), newStartStopCmd(true), newStartStopCmd(false),
		newSyncCmd(), newOnDemandCmd(), newServiceCmd(), newDaemonCmd(), newLogsCmd(), newLabelsCmd(),
		newDoctorCmd(), newLoginCmd(), newConfigCmd(), newUICmd(), newMenuCmd(), newShortcutCmd(), newUpdateCmd(), newJoinCmd(), newLeaveCmd(), newPauseCmd(), newResumeCmd(), newCICmd(),
	)
	return root
}

func interactive() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd()) ||
		isatty.IsCygwinTerminal(os.Stdin.Fd()) && isatty.IsCygwinTerminal(os.Stdout.Fd())
}

func client() (*github.Client, error) {
	ts, err := github.ResolveToken(store.TokenPath())
	if err != nil {
		return nil, err
	}
	return github.New(ts.Token), nil
}

// scopeFromArgs takes owner/repo (or org with --org) from args, or the
// current directory's git remote "origin".
func scopeFromArgs(args []string, org bool) (github.Scope, error) {
	if len(args) > 0 {
		return github.ParseScope(args[0], org)
	}
	if org {
		return github.Scope{}, fmt.Errorf("name the organization: gh-runners add --org <org>")
	}
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return github.Scope{}, fmt.Errorf("no repo given and the current directory has no git remote \"origin\"; use gh-runners add owner/repo")
	}
	remote := strings.TrimSpace(string(out))
	if !strings.Contains(remote, "github.com") {
		return github.Scope{}, fmt.Errorf("origin %q is not a github.com repository", remote)
	}
	return github.ParseScope(strings.TrimLeft(remote[strings.Index(remote, "github.com")+len("github.com"):], ":/"), false)
}

func ctx(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}
