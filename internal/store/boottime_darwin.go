package store

import (
	"time"

	"golang.org/x/sys/unix"
)

// BootTime is when the system started.
func BootTime() (time.Time, error) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(int64(tv.Sec), 0), nil
}
