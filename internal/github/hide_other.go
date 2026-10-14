//go:build !windows

package github

import "os/exec"

func hideWindow(*exec.Cmd) {}
