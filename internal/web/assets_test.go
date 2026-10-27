package web

import (
	"regexp"
	"strings"
	"testing"
)

// Background refreshes must go through redraw(), which skips a tab while
// someone is typing in it. A bare `if (S.tab === 'x') renderX()` wipes what
// was typed every few seconds (the update interval field once did that).
func TestAppJSRedrawsThroughGuard(t *testing.T) {
	b, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	bare := regexp.MustCompile(`S\.tab\s*===?\s*'([a-z]+)'\)\s*render[A-Z]\w*\(`)
	for i, line := range strings.Split(string(b), "\n") {
		for _, m := range bare.FindAllStringSubmatch(line, -1) {
			// The activity tab has no inputs, only a feed.
			if m[1] != "activity" {
				t.Errorf("app.js:%d redraws the %s tab without redraw(): %s", i+1, m[1], strings.TrimSpace(line))
			}
		}
	}
}
