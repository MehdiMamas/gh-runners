package service

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPlist(t *testing.T) {
	p := Plist("/Users/me/bin/gh-runners", "/usr/bin:/bin", "/Users/me/R&D")
	d := xml.NewDecoder(strings.NewReader(p))
	for {
		_, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("plist is not valid XML: %v\n%s", err, p)
		}
	}
	for _, want := range []string{"<string>" + Label + SuffixFor("/Users/me/R&D") + "</string>", "<string>daemon</string>", "R&amp;D", "GH_RUNNERS_HOME"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q:\n%s", want, p)
		}
	}
}
