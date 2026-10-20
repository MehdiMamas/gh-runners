package store

import (
	"syscall"
	"time"
)

var getTickCount64 = syscall.NewLazyDLL("kernel32.dll").NewProc("GetTickCount64")

// BootTime is when the system started: now minus the uptime.
func BootTime() (time.Time, error) {
	ms, _, _ := getTickCount64.Call()
	return time.Now().Add(-time.Duration(ms) * time.Millisecond).Truncate(time.Second), nil
}
