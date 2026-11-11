package service

import (
	"log"
	"strings"
	"sync"
)

// logon is what `schtasks /Query /V /FO LIST` says about a task's logon mode.
type logon int

const (
	logonUnknown     logon = iota // no readable "Logon Mode:" line (other locale)
	logonInteractive              // "Interactive only": never runs with nobody signed in
	logonOther                    // anything else, e.g. "Interactive/Background" (S4U)
)

// interactiveOnly parses the verbose list output of schtasks. A missing line
// or a translated value is unknown, so the task is left alone (#177).
func interactiveOnly(out string) logon {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "Logon Mode") {
			continue
		}
		v = strings.TrimSpace(v)
		switch {
		case strings.EqualFold(v, "Interactive only"):
			return logonInteractive
		case strings.EqualFold(v, "Interactive/Background"), strings.EqualFold(v, "Background only"):
			return logonOther
		}
		return logonUnknown
	}
	return logonUnknown
}

// withoutNP drops /NP from a schtasks command line.
func withoutNP(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != "/NP" {
			out = append(out, a)
		}
	}
	return out
}

var npFallbackOnce sync.Once

// createTask runs create; when the /NP (S4U) form fails, for example without
// the "Log on as a batch job" right, it retries without /NP so an install
// never ends up with no task, and logs that once (#177).
func createTask(args []string, create func(args []string) error) error {
	err := create(args)
	if err == nil {
		return nil
	}
	plain := withoutNP(args)
	if len(plain) == len(args) {
		return err
	}
	if err2 := create(plain); err2 != nil {
		return err
	}
	npFallbackOnce.Do(func() {
		log.Printf("scheduled task created without /NP (interactive only, runs only while signed in): %v", err)
	})
	return nil
}
