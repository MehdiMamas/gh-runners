//go:build !windows

package fleet

import "os/exec"

func hideWindow(*exec.Cmd) {}
