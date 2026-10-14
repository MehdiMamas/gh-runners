package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/doctor"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine: token, platform, daemon, runners",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("gh-runners %s on %s/%s, home %s\n\n", Version, runtime.GOOS, runtime.GOARCH, store.Home())
			problems := 0
			for _, c := range doctor.Run(ctx(cmd), nil) {
				tag := map[string]string{doctor.OK: "ok  ", doctor.Fix: "FIX ", doctor.Note: "note"}[c.Level]
				if c.Level == doctor.Fix {
					problems++
				}
				fmt.Printf("  %s  %s\n", tag, c.Text)
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
