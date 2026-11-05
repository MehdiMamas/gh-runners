//go:build !linux

package service

import "time"

// EnsureWatchdog does nothing outside Linux: the WSL watchdog is Linux only.
func EnsureWatchdog() (bool, error) { return false, nil }

// InteropBroken is always false outside Linux.
func InteropBroken() (time.Time, bool) { return time.Time{}, false }
