//go:build !windows

package store

// transient is only needed on Windows, where open files block renames.
func transient(error) bool { return false }
