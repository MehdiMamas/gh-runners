//go:build !windows

package service

func ensureKeepalive() (bool, error) { return false, nil }
