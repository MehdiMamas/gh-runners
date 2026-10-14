package cli

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/fleet"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/shortcut"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func newJoinCmd() *cobra.Command {
	var noService bool
	cmd := &cobra.Command{
		Use:   "join <code>",
		Short: "Let a main host manage this machine (code from Add a machine on its dashboard)",
		Long: `join pairs this machine with a main host, using the one-time code from
Add a machine on the main host's dashboard. The main host can then add,
start, stop, restart and remove this machine's runners and update
gh-runners here, and makes this machine's GitHub calls with its own
sign-in. This machine dials out to the main host; it opens no port.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := store.LoadConfig()
			if err != nil {
				return err
			}
			if cfg.MainHost {
				return errors.New("this machine is a main host; turn that off in Settings first")
			}
			host, _ := os.Hostname()
			info := fleet.Info{Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: Version, Runners: []fleet.RunnerInfo{}}
			name, err := fleet.Join(ctx(cmd), store.FleetDir(), args[0], info)
			if err != nil {
				return err
			}
			fmt.Printf("Joined the main host %s.\n", name)
			if noService {
				_ = control.Fleet()
				return nil
			}
			if _, err := control.GetStatus(); err == nil {
				if err := control.Fleet(); err != nil {
					return fmt.Errorf("tell the daemon: %w (restart it with `gh-runners service install`)", err)
				}
				fmt.Println("The daemon is connecting to it now.")
				return nil
			}
			where, err := service.Install()
			if err != nil {
				return fmt.Errorf("start the daemon: %w", err)
			}
			fmt.Printf("Installed (%s). The daemon is running and connecting to %s.\n", where, name)
			if lnk, err := shortcut.Install(); err == nil {
				fmt.Printf("Open the dashboard from %s or with `gh-runners ui`.\n", lnk)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noService, "no-service", false, "do not start the daemon at login")
	return cmd
}

func newLeaveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "leave",
		Short: "Stop being managed by the main host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			told, err := (&fleet.Member{Dir: store.FleetDir()}).Leave(ctx(cmd))
			if err != nil {
				return err
			}
			_ = control.Fleet()
			if told {
				fmt.Println("Left the main host. Runners on this machine stay; sign in here to manage them.")
			} else {
				fmt.Println("Left the main host (it could not be reached: remove this machine there too). Runners on this machine stay.")
			}
			return nil
		},
	}
}
