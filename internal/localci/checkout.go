package localci

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// envValue returns the value of key in a KEY=VALUE list.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// gitInit makes dir a git repository with one commit holding its files, so
// scripts that call git after actions/checkout work. It never fails the job:
// any problem is written to the log as a warning.
func gitInit(ctx context.Context, dir string, env []string, log io.Writer, repo, sha string) error {
	if err := gitInitRun(ctx, dir, env, repo, sha); err != nil {
		fmt.Fprintf(log, "::warning checkout: %v\n", err)
	}
	return nil
}

func gitInitRun(ctx context.Context, dir string, env []string, repo, sha string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	steps := [][]string{
		{"init", "-q"},
		{"-c", "core.autocrlf=false", "-c", "core.safecrlf=false", "add", "-A"},
		{"-c", "user.name=gh-runners", "-c", "user.email=gh-runners@localhost", "-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "-m", sha},
		{"remote", "add", "origin", "https://github.com/" + repo + ".git"},
		{"config", "core.autocrlf", "false"},
	}
	for _, args := range steps {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", args[len(args)-1], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
