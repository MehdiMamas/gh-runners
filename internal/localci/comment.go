package localci

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
)

const (
	maxComment   = 60000 // GitHub refuses 65536; keep a margin
	maxBlock     = 60    // lines in one failure block
	maxErrLines  = 20    // error lines shown after the first failure
	maxLogLines  = 2000  // only the tail of a log is read
	maxLogBytes  = 4 << 20
	minName      = 8 // shortest test name matched against issue titles
	maxFailNames = 15
)

// CommentInput is what the pull request comment is built from.
type CommentInput struct {
	PR      github.PullRequest
	Results []Result
	Secrets []string              // values never to show, e.g. env passed to the jobs
	Known   func() []github.Issue // open issues; called once, only when a test failed
}

// CommentBody is the pull request comment: a verdict, a table and, for each
// failed job, what failed. It starts with the marker so a re-run edits it
// in place.
func CommentBody(in CommentInput) string {
	var b strings.Builder
	b.WriteString(Marker + "\n")
	b.WriteString(verdict(in.Results) + "\n\n")
	fmt.Fprintf(&b, "Head `%s` on base `%s`\n\n", short(in.PR.Head.SHA), short(in.PR.Base.SHA))
	b.WriteString("| Job | Machine | Result | Tests | Time |\n| --- | --- | --- | --- | --- |\n")

	type detail struct {
		res   Result
		lines []string
		ok    bool
	}
	var fails []detail
	for _, r := range in.Results {
		res := string(r.Status)
		if r.Reason != "" && r.Status != Pass {
			res += " - " + Scrub(r.Reason, in.Secrets)
		}
		mach := r.Machine
		if mach == "" {
			mach = "-"
		}
		lines, ok := readTail(r.Log)
		tests := "-"
		if ok {
			if c := ParseCounts(lines); c.Known {
				tests = c.String()
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", cell(r.Job), cell(mach), cell(res), tests, r.Duration.Round(time.Second))
		if r.Status == Fail {
			fails = append(fails, detail{r, lines, ok})
		}
	}

	var issues []github.Issue
	fetched := false
	for _, f := range fails {
		var blk strings.Builder
		if !f.ok {
			m := f.res.Machine
			if m == "" {
				m = "the machine that ran it"
			}
			fmt.Fprintf(&blk, "\n**%s**: log is on %s\n", f.res.Job, m)
		} else {
			clean := make([]string, len(f.lines))
			for i, l := range f.lines {
				clean[i] = Scrub(l, in.Secrets)
			}
			names := FailedTests(clean)
			fmt.Fprintf(&blk, "\n<details><summary>%s: why it failed</summary>\n\n", html(f.res.Job))
			if len(names) > 0 {
				for i, n := range names {
					if i == maxFailNames {
						fmt.Fprintf(&blk, "- and %d more\n", len(names)-i)
						break
					}
					fmt.Fprintf(&blk, "- `%s`\n", strings.ReplaceAll(n, "`", "'"))
				}
				blk.WriteString("\n")
			}
			blk.WriteString("```text\n")
			for _, l := range ErrorExcerpt(clean) {
				blk.WriteString(strings.ReplaceAll(l, "```", "'''") + "\n")
			}
			blk.WriteString("```\n")
			if len(names) > 0 {
				if !fetched && in.Known != nil {
					issues, fetched = in.Known(), true
				}
				for _, is := range KnownIssues(names, issues) {
					fmt.Fprintf(&blk, "\nAlso open as #%d: %s\n", is.Number, is.Title)
				}
			}
			blk.WriteString("\n</details>\n")
		}
		if b.Len()+blk.Len() > maxComment {
			b.WriteString("\n(more failure details left out: the comment is too long)\n")
			break
		}
		b.WriteString(blk.String())
	}
	return b.String()
}

func verdict(rs []Result) string {
	ran, failed, other := 0, 0, 0
	for _, r := range rs {
		switch r.Status {
		case Pass:
			ran++
		case Fail:
			ran++
			failed++
		case Unsupported, Blocked:
			other++
		}
	}
	switch {
	case failed > 0:
		return fmt.Sprintf("**FAIL**: %d of %d jobs that ran failed", failed, ran)
	case other > 0:
		return fmt.Sprintf("**FAIL**: %d jobs could not run here", other)
	case ran == 0:
		return "**PASS**: no jobs needed to run"
	}
	return fmt.Sprintf("**PASS**: all %d jobs that ran passed", ran)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	if sha == "" {
		return "-"
	}
	return sha
}

func cell(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "/"), "\n", " ") }

func html(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// readTail returns the last lines of a log, ANSI codes removed.
func readTail(file string) ([]string, bool) {
	if file == "" {
		return nil, false
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > maxLogBytes {
		if _, err := f.Seek(st.Size()-maxLogBytes, 0); err != nil {
			return nil, false
		}
	}
	buf := make([]byte, maxLogBytes)
	n, _ := f.Read(buf)
	lines := strings.Split(StripANSI(string(buf[:n])), "\n")
	if len(lines) > maxLogLines {
		lines = lines[len(lines)-maxLogLines:]
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\r")
	}
	return lines, true
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// StripANSI removes terminal colour and cursor codes.
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// Counts are the test totals read from a log.
type Counts struct {
	Passed, Failed, Skipped, Flaky int
	Known                          bool
}

// String is "12 passed, 1 failed, 2 skipped" (zero parts left out).
func (c Counts) String() string {
	var p []string
	for _, x := range []struct {
		n int
		w string
	}{{c.Passed, "passed"}, {c.Failed, "failed"}, {c.Skipped, "skipped"}, {c.Flaky, "flaky"}} {
		if x.n > 0 {
			p = append(p, fmt.Sprintf("%d %s", x.n, x.w))
		}
	}
	if len(p) == 0 {
		return "0 tests"
	}
	return strings.Join(p, ", ")
}

var (
	pwRe     = regexp.MustCompile(`^\s*(\d+) (passed|failed|skipped|flaky)\b`)
	vitestRe = regexp.MustCompile(`^\s*Tests\s+(.*?)(?:\s+\(\d+\))?\s*$`)
	jestRe   = regexp.MustCompile(`^\s*Tests:\s+(.*)$`)
	partRe   = regexp.MustCompile(`(\d+) (passed|failed|skipped|flaky)\b`)
	goRe     = regexp.MustCompile(`^--- (FAIL|PASS|SKIP):`)
)

func (c *Counts) add(n int, kind string) {
	switch kind {
	case "passed":
		c.Passed = n
	case "failed":
		c.Failed = n
	case "skipped":
		c.Skipped = n
	case "flaky":
		c.Flaky = n
	}
	c.Known = true
}

// ParseCounts reads test totals from a log (ANSI already stripped or not),
// trying Vitest, Jest, Playwright and go test in that order. Known is false
// when nothing matched.
func ParseCounts(lines []string) Counts {
	var vi, je, pw, gt Counts
	for _, l := range lines {
		l = StripANSI(l)
		if m := vitestRe.FindStringSubmatch(l); m != nil && partRe.MatchString(m[1]) {
			vi = Counts{}
			for _, p := range partRe.FindAllStringSubmatch(m[1], -1) {
				vi.add(atoi(p[1]), p[2])
			}
		} else if m := jestRe.FindStringSubmatch(l); m != nil {
			je = Counts{}
			for _, p := range partRe.FindAllStringSubmatch(m[1], -1) {
				je.add(atoi(p[1]), p[2])
			}
		} else if m := pwRe.FindStringSubmatch(l); m != nil {
			pw.add(atoi(m[1]), m[2])
		} else if m := goRe.FindStringSubmatch(l); m != nil {
			switch m[1] {
			case "FAIL":
				gt.add(gt.Failed+1, "failed")
			case "PASS":
				gt.add(gt.Passed+1, "passed")
			case "SKIP":
				gt.add(gt.Skipped+1, "skipped")
			}
		}
	}
	for _, c := range []Counts{vi, je, pw, gt} {
		if c.Known {
			return c
		}
	}
	return Counts{}
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

var (
	pwFailRe   = regexp.MustCompile(`^\s*\d+\) (.+›.+)$`)
	vitFailRe  = regexp.MustCompile(`^\s*FAIL {1,}(\S.*)$`)
	goFailRe   = regexp.MustCompile(`^--- FAIL: (\S+)`)
	jestFailRe = regexp.MustCompile(`^\s*● (.+›.+)$`)
)

// failMatch returns the failing test name on a line, if it names one.
func failMatch(l string) (string, bool) {
	for _, re := range []*regexp.Regexp{pwFailRe, vitFailRe, goFailRe, jestFailRe} {
		if m := re.FindStringSubmatch(l); m != nil {
			return strings.TrimRight(strings.TrimSpace(m[1]), "─- "), true
		}
	}
	return "", false
}

// FailedTests lists the failing test names found in a log, once each.
func FailedTests(lines []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lines {
		if n, ok := failMatch(StripANSI(l)); ok && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// ErrorExcerpt is the first lines from the first failure on, at most
// maxErrLines (the last lines of the log when no failure line is found).
func ErrorExcerpt(lines []string) []string {
	start := -1
	for i, l := range lines {
		if _, ok := failMatch(StripANSI(l)); ok {
			start = i
			break
		}
	}
	var out []string
	if start < 0 {
		start = len(lines) - maxErrLines
		if start < 0 {
			start = 0
		}
	}
	for _, l := range lines[start:] {
		if len(out) == maxErrLines {
			break
		}
		out = append(out, l)
	}
	if len(out) > maxBlock {
		out = out[:maxBlock]
	}
	return out
}

var secretRes = []*regexp.Regexp{
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
}

var keyValRe = regexp.MustCompile(`(?i)((?:key|token|secret|password|passwd)["']?\s*[=:]\s*["']?)[A-Za-z0-9+/=_-]{20,}`)

// Scrub hides secrets in a log line: the given values and anything that
// looks like a token or a long key after key=.
func Scrub(s string, secrets []string) string {
	for _, v := range secrets {
		if len(v) >= 8 {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	for _, re := range secretRes {
		s = re.ReplaceAllString(s, "***")
	}
	s = keyValRe.ReplaceAllString(s, "${1}***")
	return ScrubPaths(s)
}

var (
	// a run folder: <anything>/.gh-runners/ci/<repo>/<run>, either separator
	runDirRe = regexp.MustCompile(`(?:[A-Za-z]:)?[^\s"'<>|*?:]*[/\\]\.gh-runners[/\\]ci[/\\][^/\\\s"'<>|]+[/\\][^/\\\s"'<>|]+`)
	// a home folder of any user on macOS, Linux or Windows
	homeRe = regexp.MustCompile(`(?:[A-Za-z]:[/\\]Users[/\\]|/Users/|/home/)[^/\\\s"'<>|:]+`)
)

// ScrubPaths hides this machine's local paths: a run folder becomes
// <workdir> and a home folder becomes ~. Both / and \ separators work.
func ScrubPaths(s string) string {
	s = runDirRe.ReplaceAllString(s, "<workdir>")
	if h, err := os.UserHomeDir(); err == nil && len(h) > 3 {
		s = strings.ReplaceAll(s, h, "~")
		s = strings.ReplaceAll(s, strings.ReplaceAll(h, "\\", "/"), "~")
	}
	return homeRe.ReplaceAllString(s, "~")
}

// envValues returns the values of KEY=VALUE entries long enough to be worth
// hiding from the comment.
func envValues(env []string) []string {
	var out []string
	for _, kv := range env {
		if _, v, ok := strings.Cut(kv, "="); ok && len(v) >= 8 && !strings.Contains(v, "/") {
			out = append(out, v)
		}
	}
	return out
}

var specRe = regexp.MustCompile(`[\w./-]+\.(?:spec|test)\.[jt]sx?`)

func norm(s string) string {
	return strings.ToLower(strings.NewReplacer(`"`, "", `'`, "", "`", "").Replace(s))
}

// KnownIssues returns the open issues that look like the failing tests: the
// title holds the test name (8+ characters), or the spec file name together
// with "fail" or "flak".
func KnownIssues(names []string, issues []github.Issue) []github.Issue {
	var out []github.Issue
	for _, is := range issues {
		t := norm(is.Title)
		hit := false
		for _, n := range names {
			title := n
			if i := strings.LastIndex(n, " › "); i >= 0 {
				title = n[i+len(" › "):]
			} else if i := strings.LastIndex(n, " > "); i >= 0 {
				title = n[i+len(" > "):]
			}
			if nt := norm(title); len(nt) >= minName && strings.Contains(t, nt) {
				hit = true
			}
			if f := specRe.FindString(n); f != "" {
				if strings.Contains(t, strings.ToLower(path.Base(f))) && (strings.Contains(t, "fail") || strings.Contains(t, "flak")) {
					hit = true
				}
			}
		}
		if hit {
			out = append(out, is)
		}
	}
	return out
}
