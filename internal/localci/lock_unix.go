//go:build !windows

package localci

import (
	"os"
	"syscall"
)

// flock takes an exclusive lock on f, waiting until it is free.
func flock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

func funlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
