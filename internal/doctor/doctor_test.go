package doctor

import (
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/service"
)

func TestKeepaliveCheck(t *testing.T) {
	cases := []struct {
		goos string
		s    service.Status
		warn bool
	}{
		{"windows", service.Status{Installed: true}, true},
		{"windows", service.Status{Installed: true, Keepalive: true}, false},
		{"windows", service.Status{}, false},
		{"linux", service.Status{Installed: true}, false},
		{"darwin", service.Status{Installed: true}, false},
	}
	for _, c := range cases {
		if got := keepaliveCheck(c.goos, c.s) != ""; got != c.warn {
			t.Errorf("%s %+v: warn=%v, want %v", c.goos, c.s, got, c.warn)
		}
	}
}
