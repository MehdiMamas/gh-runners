package daemon

import (
	"context"
	"path/filepath"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/localci"
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

func (d *Daemon) cleanDisk() {
	freed := runner.CleanRunners(store.RunnersDir(), runner.Busy)
	freed += localci.PruneRuns(filepath.Join(store.Home(), "ci"), localci.CIRunAge)
	if freed > 0 {
		d.Log.Printf("disk: freed %.1f GB (old runner versions, idle work folders, CI runs older than 14 days)", float64(freed)/(1<<30))
	}
}
