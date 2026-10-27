package localci

import "strings"

// Outcome states: did the run put anything on the pull request?
const (
	Posted    = "posted"
	NotPosted = "not-posted"
)

// Outcome says whether a run posted its results on the pull request and, when
// it did not, why in plain words and what to do about it. Unsupported and
// blocked runs post nothing on purpose (a run that is not a verdict must not
// show as one); this makes that visible here.
type Outcome struct {
	State     string `json:"state,omitempty"` // Posted or NotPosted; empty for runs from before this was recorded
	Reason    string `json:"reason,omitempty"`
	Next      string `json:"next,omitempty"`
	PostError string `json:"post_error,omitempty"` // GitHub's message when a post failed
}

// Summary is the outcome as one sentence for the event feed.
func (o Outcome) Summary() string {
	if o.Next == "" {
		return o.Reason
	}
	return o.Reason + " " + o.Next
}

func jobList(rs []Result, st Status) (names, reasons []string) {
	for _, x := range rs {
		if x.Status == st {
			names = append(names, x.Job)
			reasons = append(reasons, x.Job+": "+x.Reason)
		}
	}
	return
}

// UnsupportedOutcome is the outcome of a run where some job cannot run on any machine.
func UnsupportedOutcome(rs []Result) Outcome {
	names, reasons := jobList(rs, Unsupported)
	o := Outcome{State: NotPosted, Reason: "No comment: " + plural(names, "job") + " " + strings.Join(names, ", ") + " cannot run on any machine here (" + strings.Join(reasons, "; ") + "). A run that skipped a job is not a verdict, so nothing is posted or merged."}
	if strings.Contains(strings.Join(reasons, " "), "emulator") {
		o.Next = "Run `gh-runners ci emulator on` on a machine that should run it, or let a Mac run it."
	} else {
		o.Next = "Add a machine that fits the job (see Machines), then run again."
	}
	return o
}

// BlockedOutcome is the outcome of a run where some job hit an infrastructure error.
func BlockedOutcome(rs []Result) Outcome {
	names, reasons := jobList(rs, Blocked)
	return Outcome{State: NotPosted,
		Reason: "No comment: " + plural(names, "job") + " " + strings.Join(names, ", ") + " hit an infrastructure error, not a test failure (" + strings.Join(reasons, "; ") + "). Nothing is posted or merged.",
		Next:   "Fix the cause, then run again."}
}

// NotPostingOutcome is the outcome of a run that is not allowed to post.
func NotPostingOutcome(dry bool) Outcome {
	if dry {
		return Outcome{State: NotPosted, Reason: "No comment: this was a dry run, which posts and merges nothing on purpose.", Next: "Run again without Dry run to post the results."}
	}
	return Outcome{State: NotPosted, Reason: "No comment: posting is off for this repository, so results stay on this machine.", Next: "Turn on \"Post results on the pull request\" in the repository's Local CI settings."}
}

// CommentFailedOutcome is the outcome of a run whose results comment GitHub refused.
func CommentFailedOutcome(repo, msg string) Outcome {
	return Outcome{State: NotPosted, PostError: msg,
		Reason: "No comment: GitHub refused the results comment: " + msg,
		Next:   "Check that your GitHub sign-in can write to " + repo + " (a pull request into someone else's repository needs access), then run again."}
}

// PostedOutcome is the outcome of a run that posted. statusErr is the first
// status GitHub refused, if any.
func PostedOutcome(statusErr string) Outcome {
	o := Outcome{State: Posted, Reason: "The results comment is on the pull request."}
	if statusErr != "" {
		o.PostError = statusErr
		o.Reason += " Some statuses could not be set: " + statusErr
		o.Next = "Check that your GitHub sign-in may set commit statuses on this repository."
	}
	return o
}

func plural(names []string, word string) string {
	if len(names) > 1 {
		return word + "s"
	}
	return word
}
