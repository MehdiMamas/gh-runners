//go:build !windows

package localci

func freshEnv(base []string) []string { return base }
