package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// lockTimeoutExit is the exit code when the lock stays held (EX_TEMPFAIL).
const lockTimeoutExit = 75

func newCILockCmd() *cobra.Command {
	var name, timeout string
	cmd := &cobra.Command{
		Use:   "lock [--name emulator] [--timeout 30m] -- <command> [args...]",
		Short: "Run a command while holding the machine-wide lock local CI uses for emulators",
		Example: `  gh-runners ci lock -- npx firebase emulators:exec "npm test"
  gh-runners ci lock --timeout 10min -- ./run-e2e.sh`,
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := store.ParseHumanDuration(timeout)
			if err != nil {
				return err
			}
			locks := &localci.Locks{Dir: filepath.Join(store.Home(), "ci", "locks")}
			code, err := runLocked(cmd.Context(), locks, name, d, args, os.Stdin, os.Stdout, os.Stderr)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "emulator", "lock name")
	cmd.Flags().StringVar(&timeout, "timeout", "30m", "how long to wait for the lock, e.g. 10min")
	return cmd
}

// runLocked waits up to timeout for the named lock in arrival order,
// runs args with the given stdio and returns its exit code. A timeout gives
// lockTimeoutExit.
func runLocked(ctx context.Context, locks *localci.Locks, name string, timeout time.Duration, args []string, in io.Reader, out, errw io.Writer) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	release, err := locks.Acquire(wctx, name, func() {
		fmt.Fprintf(errw, "waiting for the %s lock (held by a local CI job)\n", name)
	})
	cancel()
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return lockTimeoutExit, fmt.Errorf("%s lock still held after %s", name, timeout)
	}
	if err != nil {
		return 1, err
	}
	defer release()
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	c.Stdin, c.Stdout, c.Stderr = in, out, errw
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 127, err
	}
	return 0, nil
}
