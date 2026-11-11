package daemon

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/update"
)

const (
	// updateDrainAfter is how long a pending update waits for jobs to finish
	// by itself before the runners are drained (#174).
	updateDrainAfter = 30 * time.Minute
	// updateDrainCheck is how often a draining update looks for the moment
	// the last job ended.
	updateDrainCheck = time.Minute
)

// drain is the state of a pending update that waits for jobs to finish.
type drain struct {
	draining atomic.Bool  // idle runners are held and on-demand ones take no job
	updSince atomic.Int64 // unix nanoseconds the pending update first found jobs running, 0 when none
}

// drainDue says a pending update should start draining: it found jobs
// running at since (zero when it did not), still does at now, and has waited
// at least after.
func drainDue(since, now time.Time, busy bool, after time.Duration) bool {
	return busy && !since.IsZero() && now.Sub(since) >= after
}

// noteWaiting records that the pending update found jobs running and, once
// it has waited updateDrainAfter, starts the drain: idle always-on runners
// are held and on-demand runners take no new jobs, so the install happens as
// soon as the busy ones finish. A running job is never stopped.
func (d *Daemon) noteWaiting(now time.Time) {
	d.updSince.CompareAndSwap(0, now.UnixNano())
	since := time.Unix(0, d.updSince.Load())
	if drainDue(since, now, true, updateDrainAfter) && d.draining.CompareAndSwap(false, true) {
		d.Log.Printf("update pending: draining runners")
	}
}

// endDrain forgets the pending update and lifts the hold.
func (d *Daemon) endDrain() {
	d.updSince.Store(0)
	d.draining.Store(false)
}

// latestRelease is update.Latest (a seam for tests).
var latestRelease = update.Latest

// noteWait handles a pending update that has to wait for why. Only waiting
// for runner jobs starts the clock and the drain. Any other reason (CI runs,
// peer jobs, GitHub not answering) neither starts nor resets them, so a short
// CI run during the wait does not push the drain back (#174).
func (d *Daemon) noteWait(why string, now time.Time) {
	if runnerJobWait(why) {
		d.noteWaiting(now)
	}
}

// Busy reasons for runner jobs; Daemon.busy and JobRunning build them with
// these, and runnerJobWait reads them back.
const (
	jobWaitPrefix = "the job on "
	jitWaitSuffix = " on-demand runner(s) to finish their job"
)

// runnerJobWait says a busy reason from Daemon.busy is a runner job
// (always-on or on-demand) that draining can bring to an end.
func runnerJobWait(why string) bool {
	return strings.HasPrefix(why, jobWaitPrefix) || strings.HasSuffix(why, jitWaitSuffix)
}
