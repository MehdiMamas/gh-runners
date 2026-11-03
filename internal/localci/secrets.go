package localci

import (
	"context"
	"os"
	"regexp"
	"strings"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

var (
	secretNameRe = regexp.MustCompile(`secrets\.([A-Za-z_][A-Za-z0-9_]*)`)
	secretExprRe = regexp.MustCompile(`\$\{\{\s*secrets\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
)

// SecretGate is what a run may hand to jobs that read secrets: only set
// when an allowlisted login approved the head commit (or a person ran
// `ci run --approve` by hand).
type SecretGate struct {
	OK      bool
	Allow   []string // logins whose approval counts, for the skip reason
	SHA     string   // the head commit
	Secrets map[string]store.Secret
}

// SecretNamesOf lists the secrets a job reads, in order of first use.
func SecretNamesOf(j *Job) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range secretNameRe.FindAllStringSubmatch(j.Raw, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// HasSecretJobs reports whether any job of the workflow reads secrets.
func (w *Workflow) HasSecretJobs() bool {
	for _, j := range w.Jobs {
		if j.NeedsSecrets() {
			return true
		}
	}
	return false
}

// missingSecret returns the first secret the job reads that is not set.
// GITHUB_TOKEN is never provided.
func missingSecret(j *Job, s map[string]store.Secret) string {
	for _, n := range SecretNamesOf(j) {
		if _, ok := s[n]; !ok || n == "GITHUB_TOKEN" {
			return n
		}
	}
	return ""
}

// ApplySecrets returns a copy of the job with ${{ secrets.NAME }} replaced
// in step run, step with, and step and job env, and the env pairs whose value
// came from a secret. A file secret becomes its absolute path. A name that is
// not in s is left as it is.
func ApplySecrets(j *Job, s map[string]store.Secret) (*Job, map[string]string) {
	envOut := map[string]string{}
	sub := func(v string) (string, bool) {
		hit := false
		out := secretExprRe.ReplaceAllStringFunc(v, func(m string) string {
			sec, ok := s[secretExprRe.FindStringSubmatch(m)[1]]
			if !ok {
				return m
			}
			hit = true
			if sec.File != "" {
				return sec.File
			}
			return sec.Value
		})
		return out, hit
	}
	subEnv := func(in map[string]string) map[string]string {
		if in == nil {
			return nil
		}
		out := make(map[string]string, len(in))
		for k, v := range in {
			nv, hit := sub(v)
			out[k] = nv
			if hit {
				envOut[k] = nv
			}
		}
		return out
	}
	subWith := func(in map[string]string) map[string]string {
		if in == nil {
			return nil
		}
		out := make(map[string]string, len(in))
		for k, v := range in {
			out[k], _ = sub(v)
		}
		return out
	}
	c := *j
	c.Env = subEnv(j.Env)
	c.Steps = make([]Step, len(j.Steps))
	for i, st := range j.Steps {
		st.Run, _ = sub(st.Run)
		st.With = subWith(st.With)
		st.Env = subEnv(st.Env)
		c.Steps[i] = st
	}
	return &c, envOut
}

// secretValues gives the strings to mask in the job's output: each inline
// value, and for a file secret its path and its contents.
func secretValues(j *Job, s map[string]store.Secret) []string {
	var out []string
	for _, n := range SecretNamesOf(j) {
		sec, ok := s[n]
		if !ok {
			continue
		}
		if sec.File == "" {
			out = append(out, sec.Value)
			continue
		}
		out = append(out, sec.File)
		if b, err := os.ReadFile(sec.File); err == nil {
			out = append(out, strings.TrimSpace(string(b)))
		}
	}
	return out
}

// ApprovalLogins is who may approve a head commit so its secret jobs run:
// the logins listed by name in the allowlist, or the signed-in user when none
// are listed. The word collaborators does not count.
func ApprovalLogins(s store.CIRepo, login string) []string {
	var out []string
	for _, a := range Allowlist(s, login) {
		if !strings.EqualFold(a, "collaborators") {
			out = append(out, a)
		}
	}
	return out
}

// HeadApproved reports whether a login in allow has an approving review of
// exactly the pull request's head commit. A person's latest verdict counts,
// so a later change request or dismissal takes the approval back.
func HeadApproved(reviews []github.Review, sha string, allow []string) bool {
	latest := map[string]github.Review{}
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			latest[strings.ToLower(r.Login)] = r
		}
	}
	for _, a := range allow {
		if r, ok := latest[strings.ToLower(a)]; ok && r.State == "APPROVED" && r.CommitID == sha {
			return true
		}
	}
	return false
}

// PullHeadApproved fetches the reviews and applies HeadApproved.
func PullHeadApproved(ctx context.Context, c *github.Client, owner, repo string, pr github.PullRequest, s store.CIRepo, login string) (bool, error) {
	allow := ApprovalLogins(s, login)
	if len(allow) == 0 {
		return false, nil
	}
	rs, err := c.PullRequestReviews(ctx, owner, repo, pr.Number)
	if err != nil {
		return false, err
	}
	return HeadApproved(rs, pr.Head.SHA, allow), nil
}
