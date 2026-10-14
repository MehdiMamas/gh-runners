package store

import (
	"errors"
	"syscall"
)

// transient reports errors Windows returns while another handle has the file
// open: a reader racing the rename in writeJSON, or the rename racing a reader.
func transient(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case syscall.ERROR_ACCESS_DENIED, 32 /* ERROR_SHARING_VIOLATION */, 33 /* ERROR_LOCK_VIOLATION */ :
		return true
	}
	return false
}
