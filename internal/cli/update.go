package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/update"
)

func newUpdateCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Install the latest gh-runners release",
		Long: `Install the latest gh-runners release in place of this binary.

The background service does this by itself every few hours (turn it off
with auto-update in the dashboard's Settings). When the service is running,
this asks it to update now; it waits until no runner is running a job,
then restarts into the new version.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			latest, err := update.Latest(ctx(cmd))
			if err != nil {
				return err
			}
			fmt.Printf("This version: %s\nLatest:       %s\n", Version, latest)
			if !update.Releasable(Version) {
				return errors.New("this is a development build; install a release with the install command from the README")
			}
			if !update.Newer(latest, Version) {
				fmt.Println("Up to date.")
				return nil
			}
			if check {
				return nil
			}
			if st, err := control.GetStatus(); err == nil && update.Releasable(st.Version) {
				if err := control.Update(); err != nil {
					return err
				}
				fmt.Printf("The background service installs %s and restarts as soon as no job is running.\n", latest)
				fmt.Println("Follow it under Settings > Updates in the dashboard.")
				return nil
			}
			exe, err := service.Executable()
			if err != nil {
				return err
			}
			return update.Apply(ctx(cmd), latest, exe, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only say whether a newer release exists")
	return cmd
}
