package localci

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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

// Collaborators is the allowlist word that trusts every author with write,
// maintain or admin permission on the repo.
const Collaborators = "collaborators"

const permCacheTTL = 10 * time.Minute

var (
	permMu    sync.Mutex
	permCache = map[string]permEntry{}
)

type permEntry struct {
	ok bool
	at time.Time
}

// TrustPR is CheckTrust plus the `collaborators` allowlist word: a same-repo PR
// whose author is not listed still passes when allow has the word and the
// author has write, maintain or admin permission. Forks are always refused. A
// failed lookup (403, 404, network) means not trusted.
func TrustPR(ctx context.Context, c *github.Client, pr github.PullRequest, allow []string, approve bool) error {
	err := CheckTrust(pr, allow, approve)
	if err == nil || !hasWord(allow, Collaborators) {
		return err
	}
	if head := pr.Head.RepoName(); head == "" || !strings.EqualFold(head, pr.Base.RepoName()) {
		return err
	}
	base := pr.Base.RepoName()
	key := strings.ToLower(base + "/" + pr.User.Login)
	permMu.Lock()
	e, hit := permCache[key]
	permMu.Unlock()
	if !hit || time.Since(e.at) > permCacheTTL {
		owner, repo, _ := strings.Cut(base, "/")
		perm, perr := c.CollaboratorPermission(ctx, owner, repo, pr.User.Login)
		e = permEntry{ok: perr == nil && (perm == "admin" || perm == "maintain" || perm == "write"), at: time.Now()}
		permMu.Lock()
		permCache[key] = e
		permMu.Unlock()
	}
	if e.ok {
		return nil
	}
	return err
}

func hasWord(list []string, w string) bool {
	for _, a := range list {
		if strings.EqualFold(a, w) {
			return true
		}
	}
	return false
}
