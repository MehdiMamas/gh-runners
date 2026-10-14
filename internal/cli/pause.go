package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func newPauseCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "pause",
		Short: "Stop everything gh-runners runs on this machine until `gh-runners resume`",
		Long: `Stops every runner and the processes of its job, on-demand runners, auto-sync
and the update check, right away. The daemon stays up, idle. The pause lasts
across reboots and sleep until you run resume.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := control.GetStatus(); err != nil {
				// No daemon: nothing runs; the pause keeps it from starting any.
				if err := store.SetPaused(true); err != nil {
					return err
				}
				fmt.Println("Paused. Nothing is running; the daemon starts nothing until `gh-runners resume`.")
				return nil
			}
			if !force {
				var busy []string
				rows, _, _ := collectRows(cmd, false)
				for _, r := range rows {
					if r.GitHub == "busy" && r.Local != "stopped" {
						busy = append(busy, r.Name+" ("+r.Scope+")")
					}
				}
				if len(busy) > 0 {
					msg := "Pausing cancels the job running on " + strings.Join(busy, ", ") + "."
					if !interactive() {
						return errors.New(msg + " Run with --force to pause anyway.")
					}
					if !confirm(msg + " Pause anyway?") {
						return errors.New("not paused")
					}
				}
			}
			if err := control.Pause(); err != nil {
				return err
			}
			fmt.Println("Paused: no runner, on-demand runner, sync or update runs. `gh-runners resume` starts them again.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "pause even if a runner is running a job (the job is cancelled)")
	return cmd
}

func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume",
		Short: "Start again what `gh-runners pause` stopped",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := control.GetStatus(); err != nil {
				if err := store.SetPaused(false); err != nil {
					return err
				}
				fmt.Println("Resumed, but the daemon is not running. Start it with `gh-runners service start` or `gh-runners daemon`.")
				return nil
			}
			if err := control.Resume(); err != nil {
				return err
			}
			fmt.Println("Resumed: the runners that were running are starting again; on-demand, sync and updates are back on.")
			return nil
		},
	}
}

// pausedLine is the "Paused since ..." line list and doctor show.
func pausedLine() string {
	p := store.LoadPause()
	if !p.Paused {
		return ""
	}
	s := "PAUSED: no runner runs on this machine until `gh-runners resume`"
	if !p.Since.IsZero() {
		s += " (since " + time.Since(p.Since).Round(time.Minute).String() + " ago)"
	}
	return s
}
