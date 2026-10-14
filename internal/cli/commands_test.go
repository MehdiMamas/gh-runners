package cli

import (
	"strings"
	"testing"
)

func TestEditLabels(t *testing.T) {
	for _, c := range []struct {
		labels, add, del []string
		want             string
	}{
		{[]string{"heavy"}, []string{"light"}, nil, "heavy,light"},
		{[]string{"heavy", "gpu"}, nil, []string{"HEAVY"}, "gpu"},
		{[]string{"heavy"}, []string{"Heavy", "light"}, []string{"light"}, "heavy"},
		{nil, nil, nil, ""},
	} {
		if got := strings.Join(editLabels(c.labels, c.add, c.del), ","); got != c.want {
			t.Errorf("editLabels(%v, +%v, -%v) = %q, want %q", c.labels, c.add, c.del, got, c.want)
		}
	}
}
