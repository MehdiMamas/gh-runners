package localci

import (
	"regexp"
	"strings"
)

// Cond is the outcome of evaluating an if: expression.
type Cond int

const (
	CondTrue Cond = iota
	CondFalse
	// CondAlways means the job or step runs whatever happened before.
	CondAlways
	// CondFailure means it runs only after an earlier failure.
	CondFailure
	// CondUnsupported is any expression the local CI cannot evaluate.
	CondUnsupported
)

var outputCmp = regexp.MustCompile(`^needs\.([A-Za-z0-9_-]+)\.outputs\.([A-Za-z0-9_-]+)\s*(==|!=)\s*'([^']*)'$`)

// EvalIf evaluates a job or step condition. Only the empty condition,
// always(), failure(), success() and needs.X.outputs.Y ==/!= 'v' are
// understood; outputs holds each needed job's outputs.
func EvalIf(expr string, outputs map[string]map[string]string) Cond {
	e := strings.TrimSpace(expr)
	if strings.HasPrefix(e, "${{") && strings.HasSuffix(e, "}}") {
		e = strings.TrimSpace(e[3 : len(e)-2])
	}
	switch e {
	case "", "success()":
		return CondTrue
	case "always()":
		return CondAlways
	case "failure()":
		return CondFailure
	}
	if m := outputCmp.FindStringSubmatch(e); m != nil {
		got := outputs[m[1]][m[2]]
		if (m[3] == "==") == (got == m[4]) {
			return CondTrue
		}
		return CondFalse
	}
	return CondUnsupported
}
