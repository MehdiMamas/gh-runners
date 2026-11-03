package localci

import (
	"regexp"
	"strings"
)

// testFailMarkers are lines a test tool prints when tests failed. A step
// that exits 0 but prints one is a failed step.
var testFailMarkers = []*regexp.Regexp{
	regexp.MustCompile(`Some tests failed\.`),
	regexp.MustCompile(`(?m)^\d+:\d+ \+\d+ -[1-9]\d*:`), // Flutter's "+N -M:" summary
	regexp.MustCompile(`(?m)^\s*\[E\]`),
}

// infraMarkers are tool crashes, not test results. A step that fails and
// prints one is rerun once.
var infraMarkers = []string{
	"The Dart compiler exited unexpectedly",
}

func testFailed(out string) bool {
	for _, re := range testFailMarkers {
		if re.MatchString(out) {
			return true
		}
	}
	return false
}

// infraMarker returns the infrastructure marker in out, or "".
func infraMarker(out string) string {
	for _, m := range infraMarkers {
		if strings.Contains(out, m) {
			return m
		}
	}
	return ""
}
