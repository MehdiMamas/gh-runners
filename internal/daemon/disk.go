package daemon

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/fleet"
	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/proc"
	"github.com/MehdiMamas/gh-runners/internal/runner"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// diskEvery is how often old runner versions, idle work folders and old CI
// runs are cleaned.
const diskEvery = time.Hour

// diskLoop cleans once at start and then every hour.
func (d *Daemon) diskLoop(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		timer.Reset(diskEvery)
		d.cleanDisk()
	}
}

// jobLimitEvery is how often running jobs are checked against the time limit.
const jobLimitEvery = time.Minute

// jobLimitLoop stops runner jobs that ran longer than the configured limit.
func (d *Daemon) jobLimitLoop(ctx context.Context) {
	t := time.NewTicker(jobLimitEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cfg, _ := store.LoadConfig()
		d.stopLongJobs(store.RunnersDir(), cfg.JobLimit(), proc.KillTree)
	}
}

// stopLongJobs kills, with everything it started, each job under a runner
// folder of root that ran longer than limit.
func (d *Daemon) stopLongJobs(root string, limit time.Duration, kill func(pid int) error) {
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		for _, s := range runner.LongJobs(filepath.Join(root, e.Name()), limit) {
			if err := kill(s.PID); err != nil {
				d.Log.Printf("job in %s ran over %d min; could not stop it: %v", e.Name(), int(limit.Minutes()), err)
				continue
			}
			d.Log.Printf("job in %s ran over %d min; stopped it", e.Name(), int(limit.Minutes()))
		}
	}
}

func (d *Daemon) cleanDisk() {
	freed := runner.CleanRunners(store.RunnersDir(), runner.Busy)
	freed += localci.PruneRuns(filepath.Join(store.Home(), "ci"), localci.CIRunAge)
	// The ciRunners are not reachable from here, so a job folder counts as
	// running when it changed within the 6 h job time limit.
	jobs := filepath.Join(store.Home(), "ci", "fleet", "jobs")
	pruned := fleet.PruneJobDirs(jobs, func(id string) bool {
		fi, err := os.Stat(filepath.Join(jobs, id))
		return err != nil || time.Since(fi.ModTime()) < 6*time.Hour
	})
	if pruned > 0 {
		d.Log.Printf("disk: removed %d leftover remote CI job folders", pruned)
	}
	if freed > 0 {
		d.Log.Printf("disk: freed %.1f GB (old runner versions, idle work folders, CI runs older than 14 days)", float64(freed)/(1<<30))
	}
}
