//go:build !windows

package localci

import (
	"errors"
	"os"
	"syscall"
)

// flock takes an exclusive lock on f. With try it returns false at once
// when the lock is held; otherwise it waits until it is free.
func flock(f *os.File, try bool) (bool, error) {
	how := syscall.LOCK_EX
	if try {
		how |= syscall.LOCK_NB
	}
	err := syscall.Flock(int(f.Fd()), how)
	if try && errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

func funlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
