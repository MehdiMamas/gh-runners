package localci

import (
	"reflect"
	"testing"
)

func TestMergeRegistryEnv(t *testing.T) {
	base := []string{"A=1", "Path=C:\\git;C:\\win"}
	tests := []struct {
		name          string
		machine, user map[string]string
		want          []string
	}{
		{"empty", nil, nil, base},
		{"user overrides machine", map[string]string{"JAVA_HOME": "m"}, map[string]string{"JAVA_HOME": "u"}, []string{"A=1", "Path=C:\\git;C:\\win", "JAVA_HOME=u"}},
		{"case-insensitive", map[string]string{"a": "m"}, nil, []string{"a=m", "Path=C:\\git;C:\\win"}},
		{"path merged and deduped", map[string]string{"PATH": "C:\\win;C:\\m"}, map[string]string{"Path": "C:\\u;c:\\M"}, []string{"A=1", "Path=C:\\win;C:\\m;C:\\u;C:\\git"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeRegistryEnv(base, tc.machine, tc.user)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
