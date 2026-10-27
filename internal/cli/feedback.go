package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/feedback"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func newFeedbackCmd() *cobra.Command {
	var run string
	var noDoctor, daemonLog bool
	cmd := &cobra.Command{
		Use:   "feedback <what happened>",
		Short: "Send feedback as a GitHub issue under your own account, with scrubbed logs",
		Long: `Builds an issue for MehdiMamas/gh-runners from your text, the version, the
doctor output and optionally a Local CI run and the daemon log. Everything is
scrubbed of tokens, emails and home folders. You read the exact text first and
confirm before anything is sent. It is filed with your own GitHub sign-in.`,
		Example: `  gh-runners feedback "the ci run hangs on lint" --run 1abc2d --daemon-log`,
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ts, err := github.ResolveToken(store.TokenPath())
			if err != nil {
				return errors.New("sign in to GitHub first (gh-runners login, or gh auth login): feedback is filed under your own account")
			}
			text := strings.Join(args, " ")
			sections := feedback.Collect(ctx(cmd), feedback.Options{
				RunID: run, Doctor: !noDoctor, DaemonLog: daemonLog, Version: Version})
			body := feedback.Build(text, Version, sections, []string{ts.Token})
			fmt.Printf("This is exactly what will be filed in %s/%s as issue \"%s\":\n\n%s\n\n",
				feedback.Owner, feedback.Name, feedback.Title(text, []string{ts.Token}), body)
			if !confirm("Send it under your GitHub account?") {
				fmt.Println("Not sent.")
				return nil
			}
			is, err := feedback.Send(ctx(cmd), github.New(ts.Token), ts.Token, text, body)
			if err != nil {
				return fmt.Errorf("GitHub refused the issue: %w", err)
			}
			fmt.Println("Thank you. Filed:", is.HTMLURL)
			return nil
		},
	}
	cmd.Flags().StringVar(&run, "run", "", "attach a Local CI run by id (see the dashboard's Local CI runs)")
	cmd.Flags().BoolVar(&noDoctor, "no-doctor", false, "leave the doctor output out")
	cmd.Flags().BoolVar(&daemonLog, "daemon-log", false, "attach the end of the daemon log")
	return cmd
}
