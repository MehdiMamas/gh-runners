// Package feedback builds the "Send feedback" issue: what the user wrote plus
// scrubbed logs, and files it under the user's own GitHub sign-in.
package feedback

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/doctor"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// Repo is where feedback goes.
const (
	Owner = "MehdiMamas"
	Name  = "gh-runners"
)

// MaxBody is GitHub's issue body limit, less a margin.
const MaxBody = 65000

// minSection is the least a section may be squeezed to.
const minSection = 400

var emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`)

// Scrub hides secrets (the given values and token-shaped text), email
// addresses, home folders and run folders.
func Scrub(s string, secrets []string) string {
	s = localci.Scrub(s, secrets)
	return emailRe.ReplaceAllString(s, "<email>")
}

// Section is one collapsed block of the issue.
type Section struct {
	Title string
	Text  string
}

// Options says what to attach.
type Options struct {
	Text      string
	RunID     string // a Local CI run from history; empty for none
	TaskLog   string // the queued task's log of that run, when known
	Doctor    bool
	DaemonLog bool
	Version   string
	Status    *control.Status
}

// Collect reads the chosen attachments. Nothing is scrubbed yet.
func Collect(ctx context.Context, o Options) []Section {
	var out []Section
	if o.Doctor {
		var b strings.Builder
		for _, c := range doctor.Run(ctx, o.Status) {
			fmt.Fprintf(&b, "%-4s %s\n", c.Level, c.Text)
		}
		out = append(out, Section{"gh-runners doctor", b.String()})
	}
	if o.RunID != "" {
		for _, r := range localci.History() {
			if r.ID != o.RunID {
				continue
			}
			head := fmt.Sprintf("%s PR %d, passed=%v", r.Repo, r.PR, r.Passed)
			if r.Error != "" {
				head += ", error: " + r.Error
			}
			out = append(out, Section{"Local CI run " + r.ID, head})
			if o.TaskLog != "" {
				out = append(out, Section{"Local CI task log", o.TaskLog})
			}
			root := filepath.Join(store.Home(), "ci") + string(filepath.Separator)
			for _, res := range r.Results {
				t := fmt.Sprintf("%s: %s %s", res.Job, res.Status, res.Reason)
				if res.Log != "" && strings.HasPrefix(filepath.Clean(res.Log), root) {
					if tail, err := TailFile(res.Log, 200); err == nil {
						t += "\n\n" + tail
					}
				}
				out = append(out, Section{"Job log: " + res.Job, t})
			}
			break
		}
	}
	if o.DaemonLog {
		if tail, err := TailFile(store.DaemonLog(), 200); err == nil {
			out = append(out, Section{"Daemon log (tail)", tail})
		}
	}
	return out
}

// Env is the version and platform line.
func Env(version string) string {
	return fmt.Sprintf("gh-runners %s on %s/%s", version, runtime.GOOS, runtime.GOARCH)
}

// Build scrubs everything and returns the issue body, trimmed under MaxBody.
func Build(text, version string, sections []Section, secrets []string) string {
	head := "## What happened\n\n" + Scrub(strings.TrimSpace(text), secrets) + "\n\n**" + Env(version) + "**\n"
	var blocks []Section
	for _, s := range sections {
		blocks = append(blocks, Section{s.Title, Scrub(s.Text, secrets)})
	}
	return fit(head, blocks)
}

func render(s Section) string {
	fence := "```"
	for strings.Contains(s.Text, fence) {
		fence += "`"
	}
	return "\n<details><summary>" + s.Title + "</summary>\n\n" + fence + "text\n" + strings.TrimRight(s.Text, "\n") + "\n" + fence + "\n\n</details>\n"
}

// fit renders head and sections, cutting the start of the longest sections
// first (the end of a log matters most) until the body fits MaxBody.
func fit(head string, sections []Section) string {
	for i := 0; i < 1000; i++ {
		var b strings.Builder
		b.WriteString(head)
		for _, s := range sections {
			b.WriteString(render(s))
		}
		if b.Len() <= MaxBody {
			return b.String()
		}
		over := b.Len() - MaxBody
		big := -1
		for j, s := range sections {
			if len(s.Text) > minSection && (big < 0 || len(s.Text) > len(sections[big].Text)) {
				big = j
			}
		}
		if big < 0 {
			return TrimTail(b.String(), MaxBody)
		}
		keep := len(sections[big].Text) - over - 200
		if half := len(sections[big].Text) / 2; keep < half && len(sections[big].Text) > 2*minSection {
			keep = half
		}
		if keep < minSection {
			keep = minSection
		}
		sections[big].Text = TrimTail(sections[big].Text, keep)
	}
	return TrimTail(head, MaxBody)
}

// TrimTail keeps the last max bytes of s (on a line start when it can),
// with a note where the start was cut.
func TrimTail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[len(s)-max:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 && i < len(cut)/2 {
		cut = cut[i+1:]
	}
	return "[... earlier lines cut to fit GitHub's size limit ...]\n" + cut
}

// Title is the issue title: the first line of the text.
func Title(text string, secrets []string) string {
	t := strings.TrimSpace(Scrub(text, secrets))
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[:i]
	}
	r := []rune(t)
	if len(r) > 80 {
		t = string(r[:80]) + "..."
	}
	if t == "" {
		t = "Feedback"
	}
	return "Feedback: " + t
}

// Send files body under the signed-in user. The body is scrubbed and trimmed
// again, so whatever the caller passes, no known secret leaves.
func Send(ctx context.Context, c *github.Client, token, text, body string) (github.NewIssue, error) {
	secrets := []string{token}
	body = TrimTail(Scrub(body, secrets), MaxBody)
	return c.CreateIssue(ctx, Owner, Name, Title(text, secrets), body, []string{"feedback"})
}

// TailFile returns the last n lines of a file.
func TailFile(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 1<<20 {
		_, _ = f.Seek(st.Size()-1<<20, io.SeekStart)
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return strings.Join(lines, "\n"), sc.Err()
}
