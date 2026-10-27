//go:build windows

package localci

import (
	"os"

	"golang.org/x/sys/windows"
)

// flock takes an exclusive lock on f, waiting until it is free.
func flock(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}

func funlock(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
