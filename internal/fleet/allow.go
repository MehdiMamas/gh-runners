package fleet

import (
	"regexp"
	"strings"
)

// The allowlist of dashboard API calls one machine may make on another: a
// main host on a member, and an account machine on another one. The
// receiving side checks it on every request. Runners, labels, logs, state, tasks and updates only; never
// sign-in or token routes, the service, the settings, the folder browser
// (it lists any folder) or moving hand-made runners (it takes any path).
var memberAPI = []rule{
	{"GET", `/api/state`},
	{"GET", `/api/repos`},
	{"GET", `/api/github-runners`},
	{"POST", `/api/add`},
	{"GET", `/api/workflow-labels`},
	{"POST", `/api/repo-runners`},
	{"PUT", `/api/runner-labels`},
	{"POST", `/api/runners/start-(last|all)`},
	{"POST", `/api/runners/` + id + `/(start|stop|restart|remove)`},
	{"GET", `/api/runners/` + id + `/log`},
	{"GET", `/api/daemon-log`},
	{"GET", `/api/tasks`},
	{"GET", `/api/tasks/[0-9]+`},
	{"POST", `/api/update`},
	{"POST", `/api/(pause|resume)`},
}

// The GitHub API calls a member may have the host make: exactly the ones
// the github package's client uses.
var githubAPI = []rule{
	{"POST", runners + `/(registration-token|remove-token|generate-jitconfig)`},
	{"GET", runners + `(/downloads)?`},
	{"DELETE", runners + `/[0-9]+`},
	{"PUT", runners + `/[0-9]+/labels`},
	{"GET", repo + `/contents/\.github/workflows(/` + name + `)?`},
	{"GET", repo},
	{"GET", repo + `/actions/runs`},
	{"GET", repo + `/actions/runs/[0-9]+/jobs`},
	{"GET", `/repos/actions/runner/releases/latest`},
	{"GET", `/user`},
	{"GET", `/user/repos`},
}

const (
	name    = `[A-Za-z0-9_][A-Za-z0-9_.-]*`
	id      = `[A-Za-z0-9_][A-Za-z0-9_.-]*`
	repo    = `/repos/` + name + `/` + name
	runners = `(` + repo + `|/orgs/` + name + `)/actions/runners`
)

type rule struct{ method, path string }

type matcher struct {
	method string
	re     *regexp.Regexp
}

func compile(rules []rule) []matcher {
	out := make([]matcher, len(rules))
	for i, r := range rules {
		out[i] = matcher{r.method, regexp.MustCompile(`^` + r.path + `$`)}
	}
	return out
}

var (
	memberAPIRe = compile(memberAPI)
	githubAPIRe = compile(githubAPI)
)

func allowed(list []matcher, method, pathQuery string) bool {
	p, _, _ := strings.Cut(pathQuery, "?")
	// No tricks with the path: GitHub and Go's mux would read these
	// differently from the patterns above.
	if strings.Contains(p, "..") || strings.ContainsAny(p, `%\#`) || strings.Contains(p, "//") {
		return false
	}
	for _, m := range list {
		if m.method == method && m.re.MatchString(p) {
			return true
		}
	}
	return false
}

// MemberAllowed reports whether a host may call this dashboard API on a
// member. Members check it on every request; the host checks it too, to
// give a clear error.
func MemberAllowed(method, pathQuery string) bool { return allowed(memberAPIRe, method, pathQuery) }

// PeerAllowed reports whether a machine may call this dashboard API on
// another machine of the same GitHub account (the same list as for a main
// host). Both sides check it: the sender for a clear error, the receiver
// because it cannot trust the sender.
func PeerAllowed(method, pathQuery string) bool { return allowed(memberAPIRe, method, pathQuery) }

// GitHubAllowed reports whether the host makes this GitHub API call for a
// member.
func GitHubAllowed(method, pathQuery string) bool { return allowed(githubAPIRe, method, pathQuery) }
