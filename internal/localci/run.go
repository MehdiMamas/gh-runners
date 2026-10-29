package localci

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// Marker is the hidden tag that lets a re-run find and edit its own comment.
const Marker = "<!-- local-ci -->"

// Options is one run of the local CI against a pull request.
type Options struct {
	Client   *github.Client
	Owner    string
	Repo     string
	PR       int
	DryRun   bool     // never post, never merge
	Approve  bool     // run despite the trust gate
	Jobs     []string // only these jobs (and what they need); empty means all
	Workflow string   // workflow file to read at the PR head; empty means WorkflowPath
	Settings store.CIRepo
	Login    string // signed-in user, the default allowlist
	Conc     int    // parallel jobs; default NumCPU/2
	Exec     Executor
	Out      io.Writer
	Before   []Event // events from before the run started (head seen, queued), put first in its feed
}

// Report is the outcome of a run, in workflow order.
type Report struct {
	PR      github.PullRequest
	Results []Result
	Merged  bool
	Dir     string  // the run folder: logs and the event feed
	Outcome Outcome // whether the run posted on the pull request, and why not
}

// AllPassed reports whether nothing failed or was unsupported.
func (r Report) AllPassed() bool {
	for _, x := range r.Results {
		if x.Status == Fail || x.Status == Unsupported || x.Status == Blocked {
			return false
		}
	}
	return true
}

// HasBlocked reports whether any job ended as an infrastructure error. Such
// a run is not a verdict: nothing is posted and nothing is merged.
func (r Report) HasBlocked() bool {
	for _, x := range r.Results {
		if x.Status == Blocked {
			return true
		}
	}
	return false
}

// HasUnsupported reports whether any job could not run on any machine here.
// Such a run is not a verdict either: another machine may be able to run it.
func (r Report) HasUnsupported() bool {
	for _, x := range r.Results {
		if x.Status == Unsupported {
			return true
		}
	}
	return false
}

// Allowlist is who may start runs on their own for the repo.
func Allowlist(s store.CIRepo, login string) []string {
	if len(s.Allow) > 0 {
		return s.Allow
	}
	if login != "" {
		return []string{login}
	}
	return nil
}

// Run fetches the pull request, checks trust, runs the selected jobs and
// reports. Posting and merging happen only when not a dry run and enabled in
// the repo's settings.
func Run(ctx context.Context, o Options) (Report, error) {
	if o.Exec == nil {
		o.Exec = LocalExecutor{}
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	c := o.Client
	pr, err := c.PullRequest(ctx, o.Owner, o.Repo, o.PR)
	if err != nil {
		return Report{}, err
	}
	rep := Report{PR: pr}
	if pr.State != "open" {
		return rep, fmt.Errorf("PR #%d is %s", pr.Number, pr.State)
	}
	if err := CheckTrust(pr, Allowlist(o.Settings, o.Login), o.Approve); err != nil {
		return rep, err
	}
	sha := pr.Head.SHA
	wfPath := o.Workflow
	if wfPath == "" {
		wfPath = WorkflowPath
	}
	if err := ValidateWorkflowPath(wfPath); err != nil {
		return rep, err
	}
	data, err := c.FileAt(ctx, o.Owner, o.Repo, wfPath, sha)
	if err != nil {
		return rep, fmt.Errorf("read %s at %s: %w", wfPath, sha[:7], err)
	}
	wf, err := ParseWorkflow(data)
	if err != nil {
		return rep, err
	}
	files, err := c.PullRequestFiles(ctx, o.Owner, o.Repo, o.PR)
	if err != nil {
		return rep, err
	}

	post := o.Settings.Post && !o.DryRun
	runDir := filepath.Join(store.Home(), "ci", o.Owner+"-"+o.Repo, fmt.Sprintf("pr%d-%s-%d", o.PR, sha[:7], time.Now().Unix()))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return rep, err
	}
	// Marks which process owns the run, so another daemon's startup sweep
	// leaves a live run alone.
	WriteOwner(runDir)
	rep.Dir = runDir
	ev := OpenEvents(runDir)
	for _, e := range o.Before {
		ev.AddEvent(e)
	}
	ev.Add("started", "Run started for PR #%d at %s.", pr.Number, sha[:7])
	setJobs, runDone := StartRunning(RunningRun{Repo: o.Owner + "/" + o.Repo, PR: o.PR, SHA: sha, Title: pr.Title, Dir: runDir})
	defer runDone()
	tarball := filepath.Join(runDir, "source.tgz")
	if err := c.DownloadTarball(ctx, o.Owner, o.Repo, sha, tarball); err != nil {
		return rep, fmt.Errorf("download source: %w", err)
	}
	fmt.Fprintf(o.Out, "PR #%d %q at %s, logs in %s\n", pr.Number, pr.Title, sha[:7], runDir)

	// The changes job is computed here, never run.
	outputs := map[string]map[string]string{}
	changesName := ""
	for _, n := range wf.Order {
		if out, ok := ChangesOutputs(wf.Jobs[n], files); ok {
			outputs[n], changesName = out, n
		}
	}
	selected := selectJobs(wf, o.Jobs)
	var planned []string
	for _, n := range wf.Order {
		if selected[n] {
			planned = append(planned, n)
		}
	}
	setJobs(planned)
	var tasks []Task
	for _, n := range wf.Order {
		if !selected[n] {
			continue
		}
		j := wf.Jobs[n]
		t := Task{Name: n, Needs: j.Needs}
		if _, self := o.Exec.(interface{ SelfLocking() bool }); j.StartsServers() && !self {
			t.Lock = "emulator"
		}
		tasks = append(tasks, t)
	}

	var statusMu sync.Mutex
	statusErr := ""
	status := func(state, name, desc string) {
		if !post {
			return
		}
		if err := c.SetStatus(ctx, o.Owner, o.Repo, sha, state, name, desc); err != nil {
			fmt.Fprintf(o.Out, "could not set status %s: %v\n", name, err)
			ev.AddEvent(Event{Kind: "error", Text: fmt.Sprintf("Could not set the %s status on GitHub: %v", name, err)})
			statusMu.Lock()
			if statusErr == "" {
				statusErr = fmt.Sprintf("status %s: %v", name, err)
			}
			statusMu.Unlock()
			return
		}
		if state != "pending" {
			ev.AddEvent(Event{Kind: "status", Text: fmt.Sprintf("Status %s set to %s.", name, state)})
		}
	}
	for _, n := range wf.Order {
		if selected[n] && wf.Jobs[n].IsAggregate() {
			status("pending", n, "Waiting for the other jobs")
		}
	}

	renv := RunEnv{Repo: o.Owner + "/" + o.Repo, SHA: sha, Ref: fmt.Sprintf("refs/pull/%d/merge", o.PR)}
	jobEnv := ScrubEnv(os.Environ(), renv, nil)
	jobSecrets := envValues(jobEnv)
	runner := func(ctx context.Context, t Task, needs map[string]Result) Result {
		j := wf.Jobs[t.Name]
		res := Decide(wf, j, needs, outputs, changesName == t.Name)
		if res.Status == "" {
			ctxName := "local-ci/" + t.Name
			ev.AddEvent(Event{Kind: "job_started", Job: t.Name, Text: "Job " + t.Name + " started."})
			status("pending", ctxName, "Running")
			jobDir := filepath.Join(runDir, t.Name)
			res = o.Exec.RunJob(ctx, JobSpec{
				Name: t.Name, Job: j, Tarball: tarball, RunDir: jobDir,
				Log: filepath.Join(runDir, "logs", t.Name+".log"),
				Env: jobEnv, RunEnv: renv,
			})
			if res.Status == Pass {
				_ = os.RemoveAll(filepath.Join(jobDir, "src"))
			}
			state, desc := "success", "Passed in "+res.Duration.Round(time.Second).String()
			if res.Status == Fail {
				state, desc = "failure", "Failed: "+res.Reason
			}
			if res.Status != Blocked && res.Status != Unsupported {
				status(state, ctxName, desc)
			}
		} else if j.IsAggregate() {
			state, desc := "success", "Every job passed or was skipped"
			if res.Status != Pass {
				state, desc = "failure", res.Reason
			}
			upstream := false
			for _, nr := range needs {
				upstream = upstream || nr.Status == Blocked || nr.Status == Unsupported
			}
			if res.Status != Blocked && res.Status != Unsupported && !upstream {
				status(state, t.Name, desc)
			}
		}
		res.Job = t.Name
		ev.AddEvent(Event{Kind: "job_finished", Job: t.Name, Machine: res.Machine, Status: string(res.Status), Text: jobEventText(t.Name, res)})
		fmt.Fprintf(o.Out, "  %-5s %-18s %s\n", res.Status, t.Name, res.Reason)
		return res
	}
	conc := o.Conc
	if conc == 0 {
		conc = runtime.NumCPU() / 2
	}
	locks := &Locks{Dir: filepath.Join(store.Home(), "ci", "locks")}
	byName := Schedule(ctx, tasks, conc, locks, runner)
	for _, n := range wf.Order {
		if r, ok := byName[n]; ok {
			rep.Results = append(rep.Results, r)
		}
	}
	fmt.Fprint(o.Out, "\n"+FormatTable(rep.Results))

	finish := func() {
		ev.AddEvent(Event{Kind: "outcome", Text: rep.Outcome.Summary()})
	}
	if rep.HasBlocked() {
		rep.Outcome = BlockedOutcome(rep.Results)
		finish()
		fmt.Fprintln(o.Out, "\nBLOCKED: a job hit an infrastructure error, not a test failure. Nothing was posted or merged; fix it and run again.")
		return rep, nil
	}
	if rep.HasUnsupported() {
		var names, reasons []string
		for _, x := range rep.Results {
			if x.Status == Unsupported {
				names = append(names, x.Job)
				reasons = append(reasons, x.Reason)
			}
		}
		rep.Outcome = UnsupportedOutcome(rep.Results)
		finish()
		fmt.Fprintf(o.Out, "\nNOT SUPPORTED: no machine here can run %s: %s. Nothing was posted or merged.\n", strings.Join(names, ", "), strings.Join(reasons, "; "))
		return rep, nil
	}
	if !post {
		rep.Outcome = NotPostingOutcome(o.DryRun)
		finish()
		return rep, nil
	}
	hasAgg := false
	for _, n := range wf.Order {
		hasAgg = hasAgg || (selected[n] && wf.Jobs[n].IsAggregate())
	}
	if !hasAgg {
		if rep.AllPassed() {
			status("success", "ci-passed", "Every job passed or was skipped")
		} else {
			status("failure", "ci-passed", "A job failed")
		}
	}
	if err := c.UpsertComment(ctx, o.Owner, o.Repo, o.PR, Marker, CommentBody(CommentInput{
		PR: pr, Results: rep.Results, Secrets: jobSecrets,
		Known: func() []github.Issue {
			is, _ := c.ListOpenIssues(ctx, o.Owner, o.Repo)
			return is
		},
	})); err != nil {
		fmt.Fprintf(o.Out, "could not post the results comment: %v\n", err)
		ev.AddEvent(Event{Kind: "error", Text: "Could not post the results comment: " + err.Error()})
		rep.Outcome = CommentFailedOutcome(o.Owner+"/"+o.Repo, err.Error())
	} else {
		ev.Add("comment", "Results comment posted on PR #%d.", o.PR)
		rep.Outcome = PostedOutcome(statusErr)
	}
	finish()
	if o.Settings.Merge && rep.AllPassed() && len(o.Jobs) == 0 {
		ok, err := c.Approved(ctx, o.Owner, o.Repo, o.PR)
		switch {
		case err != nil:
			fmt.Fprintf(o.Out, "could not read reviews: %v\n", err)
		case !ok:
			fmt.Fprintln(o.Out, "all jobs passed, but the PR is not approved yet; not merging")
		default:
			_ = c.Comment(ctx, o.Owner, o.Repo, o.PR, "All checks passed on "+sha[:7]+", merging.")
			if err := c.SquashMerge(ctx, o.Owner, o.Repo, o.PR, sha); err != nil {
				fmt.Fprintf(o.Out, "merge failed: %v\n", err)
			} else {
				rep.Merged = true
				ev.Add("merge", "Squash-merged PR #%d.", o.PR)
				fmt.Fprintln(o.Out, "merged")
			}
		}
	}
	return rep, nil
}

// selectJobs returns the jobs to run: all of them, or the named ones with
// everything they need.
func selectJobs(wf *Workflow, names []string) map[string]bool {
	sel := map[string]bool{}
	if len(names) == 0 {
		for n := range wf.Jobs {
			sel[n] = true
		}
		return sel
	}
	var add func(string)
	add = func(n string) {
		j, ok := wf.Jobs[n]
		if !ok || sel[n] {
			return
		}
		sel[n] = true
		for _, d := range j.Needs {
			add(d)
		}
	}
	for _, n := range names {
		add(strings.TrimSpace(n))
	}
	return sel
}

// Decide settles a job without running it when it can: computed, skipped,
// unsupported, or aggregate. A zero Status in the answer means "run it".
func Decide(wf *Workflow, j *Job, needs map[string]Result, outputs map[string]map[string]string, isChanges bool) Result {
	if isChanges {
		return Result{Status: Pass, Reason: "computed from the changed files"}
	}
	cond := EvalIf(j.If, outputs)
	for _, n := range sortedKeys(needs) {
		if cond == CondAlways {
			break
		}
		if s := needs[n].Status; s == Fail || s == Unsupported || s == Blocked {
			return Result{Status: Skipped, Reason: "needs " + n + ", which did not pass"}
		}
	}
	if j.IsAggregate() {
		for _, n := range sortedKeys(needs) {
			if needs[n].Status == Blocked {
				return Result{Status: Blocked, Reason: n + " is blocked: " + needs[n].Reason}
			}
		}
		for _, n := range sortedKeys(needs) {
			if s := needs[n].Status; s != Pass && s != Skipped {
				return Result{Status: Fail, Reason: n + " ended with " + strings.ToLower(string(s))}
			}
		}
		return Result{Status: Pass, Reason: "every job passed or was skipped"}
	}
	switch cond {
	case CondFalse:
		return Result{Status: Skipped, Reason: "no changed files match its filter"}
	case CondUnsupported:
		return Result{Status: Unsupported, Reason: "unsupported expression: " + j.If}
	}
	if j.NeedsSecrets() {
		return Result{Status: Skipped, Reason: "needs secrets"}
	}
	for _, s := range j.Steps {
		if s.Uses != "" && !knownUses(s.Uses) {
			return Result{Status: Unsupported, Reason: "unsupported action " + s.Uses}
		}
		if strings.Contains(s.Run, "${{") {
			return Result{Status: Unsupported, Reason: "unsupported expression in a run step"}
		}
	}
	return Result{}
}

func sortedKeys(m map[string]Result) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

// FormatTable renders results as a text table: job, result, time, log.
func FormatTable(rs []Result) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "JOB\tMACHINE\tRESULT\tTIME\tLOG")
	for _, r := range rs {
		res := string(r.Status)
		if r.Reason != "" && r.Status != Pass {
			res += " (" + r.Reason + ")"
		}
		log := r.Log
		if log == "" {
			log = "-"
		}
		mach := r.Machine
		if mach == "" {
			mach = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Job, mach, res, r.Duration.Round(time.Second), log)
	}
	tw.Flush()
	return b.String()
}

// jobEventText says in plain words how a job ended.
func jobEventText(name string, r Result) string {
	on := ""
	if r.Machine != "" {
		on = " on " + r.Machine
	}
	took := ""
	if r.Duration > 0 {
		took = " in " + r.Duration.Round(time.Second).String()
	}
	switch r.Status {
	case Pass:
		return "Job " + name + " passed" + took + on + "."
	case Fail:
		return "Job " + name + " failed" + took + on + ": " + r.Reason
	case Skipped:
		return "Job " + name + " was skipped: " + r.Reason
	case Unsupported:
		return "Job " + name + " cannot run here: " + r.Reason
	case Blocked:
		return "Job " + name + " hit an infrastructure error" + on + ": " + r.Reason
	}
	return "Job " + name + " ended."
}
