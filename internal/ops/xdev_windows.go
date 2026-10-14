//go:build windows

package ops

import (
	"errors"
	"syscall"
)

// ERROR_NOT_SAME_DEVICE
func isCrossDevice(err error) bool { return errors.Is(err, syscall.Errno(17)) }
