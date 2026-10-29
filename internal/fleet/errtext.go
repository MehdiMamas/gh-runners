package fleet

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
)

// plainErr says why a connection failed in words a user can act on.
func plainErr(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrFingerprint):
		return "Something else answered at its address (its certificate does not match the directory)"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "Connection refused: gh-runners is not running there, or a firewall blocks the port"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "No answer: the PC is off, on another network, or a firewall blocks the port"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "No answer: the PC is off, on another network, or a firewall blocks the port"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "bad certificate"), strings.Contains(msg, "certificate required"):
		return "It did not accept this PC's certificate (it may not have seen this PC in the directory yet)"
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "timed out"):
		return "No answer: the PC is off, on another network, or a firewall blocks the port"
	case strings.Contains(msg, "refused"):
		return "Connection refused: gh-runners is not running there, or a firewall blocks the port"
	case strings.Contains(msg, "no route"), strings.Contains(msg, "unreachable"):
		return "No route to it: it is on another network"
	}
	if i := strings.LastIndex(msg, "\n"); i >= 0 {
		msg = msg[i+1:]
	}
	return msg
}
