package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/daemon"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/runner"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine: token, platform, daemon, runners",
		RunE: func(cmd *cobra.Command, args []string) error {
			problems := 0
			ok := func(format string, a ...any) { fmt.Printf("  ok    "+format+"\n", a...) }
			bad := func(format string, a ...any) { problems++; fmt.Printf("  FIX   "+format+"\n", a...) }
			note := func(format string, a ...any) { fmt.Printf("  note  "+format+"\n", a...) }

			fmt.Printf("gh-runners %s on %s/%s, home %s\n\n", Version, runtime.GOOS, runtime.GOARCH, store.Home())

			if osName, arch, err := runner.Platform(); err != nil {
				bad("%v", err)
			} else {
				ok("runner package: %s-%s", osName, arch)
			}
			if runtime.GOOS != "windows" && os.Geteuid() == 0 {
				bad("running as root: the runner refuses to configure as root; use a normal user")
			}

			ts, err := github.ResolveToken(store.TokenPath())
			if err != nil {
				bad("%v", err)
			} else {
				me, scopes, err := github.New(ts.Token).Me(ctx(cmd))
				switch {
				case err != nil:
					bad("token from %s does not work: %v", ts.Source, err)
				case scopes == "":
					ok("signed in as %s (token from %s, fine-grained: needs Administration read/write on the repos)", me.Login, ts.Source)
				default:
					ok("signed in as %s (token from %s, scopes: %s)", me.Login, ts.Source, scopes)
					if !hasScope(scopes, "repo") {
						bad("the token lacks the `repo` scope: gh auth refresh -s repo")
					}
					if !hasScope(scopes, "admin:org") {
						note("no `admin:org` scope: fine for repo runners; for org runners run gh auth refresh -s admin:org")
					}
				}
			}

			if runtime.GOOS == "linux" {
				if out, err := exec.Command("ldconfig", "-p").Output(); err == nil && !strings.Contains(string(out), "libicu") {
					bad("libicu not found; the runner needs it. Install with: sudo ~/.gh-runners/runners/<any>/bin/installdependencies.sh (or your package manager's libicu)")
				}
			}

			s := service.Get()
			switch {
			case s.Running && s.Installed:
				ok("daemon running (pid %d), starts at login (%s)", s.PID, s.Where)
			case s.Running:
				note("daemon running (pid %d) but not set to start at login: gh-runners service install", s.PID)
			case s.Installed:
				bad("daemon set to start at login but not running: gh-runners service start")
			default:
				bad("daemon not installed: gh-runners service install")
			}

			cfg, _ := store.LoadConfig()
			ok("labels this machine answers to: %s", strings.Join(daemon.MachineLabels(cfg), ", "))
			if cfg.Sync.Auto {
				ok("auto-sync on (every %s)", cfg.Sync.Interval.Or(time.Hour))
			}
			if cfg.OnDemand.Enabled {
				if st, err := control.GetStatus(); err == nil {
					ok("on-demand: %s", st.OnDemand)
				} else {
					ok("on-demand on")
				}
			}

			runners, _ := store.List()
			unregistered := 0
			for _, r := range runners {
				if !r.Configured() {
					unregistered++
				}
			}
			ok("%d runners on this machine", len(runners))
			if unregistered > 0 {
				bad("%d runner folders were never registered (an add failed); rerun `gh-runners add <repo> --force` or remove them", unregistered)
			}
			fmt.Println()
			if problems > 0 {
				return fmt.Errorf("%d problem(s) to fix", problems)
			}
			fmt.Println("All good.")
			return nil
		},
	}
}

func hasScope(scopes, want string) bool {
	for _, s := range strings.Split(scopes, ",") {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}
