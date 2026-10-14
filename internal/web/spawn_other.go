//go:build !linux

package web

import "os/exec"

func helperCommand(exe string, args ...string) *exec.Cmd { return exec.Command(exe, args...) }
