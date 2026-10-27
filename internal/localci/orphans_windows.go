//go:build windows

package localci

import "os"

func selfPID() int { return os.Getpid() }

// Windows has no cheap way to read another process's working folder; the
// job's tree is killed with taskkill when the run is cancelled.
func listCwds() []procCwd { return nil }

func killPIDs([]int) {}
