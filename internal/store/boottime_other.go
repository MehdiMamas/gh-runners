//go:build !darwin && !linux && !windows

package store

import (
	"errors"
	"time"
)

// BootTime is not known on this system.
func BootTime() (time.Time, error) { return time.Time{}, errors.New("boot time not available") }
