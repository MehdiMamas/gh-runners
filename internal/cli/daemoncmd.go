package cli

import (
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/daemon"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func newDaemonCmd() *cobra.Command {
	var detach, keepalive bool
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the supervisor in the foreground (the service runs this)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if detach {
				// The keepalive task only revives a daemon that died: the
				// marker is removed by a deliberate stop (#144).
				if keepalive && !store.DaemonLiveBefore() {
					return nil
				}
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
	cmd.Flags().BoolVar(&keepalive, "keepalive", false, "with --detach: start only if the last run ended without a stop (used by the keepalive task)")
	return cmd
}
