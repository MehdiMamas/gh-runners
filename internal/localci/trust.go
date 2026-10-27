package localci

import (
	"fmt"
	"strings"

	"github.com/MehdiMamas/gh-runners/internal/github"
)

// CheckTrust decides whether a pull request may run without a person looking
// at it first: its head must live in the base repo (never a fork) and its
// author must be on the allowlist. approve is the person's explicit go-ahead.
func CheckTrust(pr github.PullRequest, allow []string, approve bool) error {
	if approve {
		return nil
	}
	head, base := pr.Head.RepoName(), pr.Base.RepoName()
	if head == "" || !strings.EqualFold(head, base) {
		return fmt.Errorf("PR #%d comes from a fork (%s); not running it on its own. Re-run with --approve after reading the changes", pr.Number, orNone(head))
	}
	for _, a := range allow {
		if strings.EqualFold(a, pr.User.Login) {
			return nil
		}
	}
	return fmt.Errorf("PR #%d is by %s, who is not on the allowlist (%s). Re-run with --approve, or add them with `gh-runners ci config %s --allow`", pr.Number, pr.User.Login, strings.Join(allow, ", "), base)
}

func orNone(s string) string {
	if s == "" {
		return "deleted repo"
	}
	return s
}
