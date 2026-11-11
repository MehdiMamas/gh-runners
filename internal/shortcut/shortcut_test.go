package shortcut

import (
	"reflect"
	"testing"
)

func TestUIArgs(t *testing.T) {
	tests := []struct {
		name string
		home string
		want []string
	}{
		{"unset", "", []string{"ui"}},
		{"plain path", "/home/u/gh", []string{"ui", "--home", "/home/u/gh"}},
		{"path with spaces", "/home/u/my runners", []string{"ui", "--home", "/home/u/my runners"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uiArgs(tt.home); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("uiArgs(%q) = %q, want %q", tt.home, got, tt.want)
			}
		})
	}
}
