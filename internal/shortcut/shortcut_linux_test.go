package shortcut

import "testing"

func TestExecLine(t *testing.T) {
	tests := []struct {
		name string
		exe  string
		args []string
		want string
	}{
		{"plain", "/usr/bin/gh-runners", []string{"ui"}, `"/usr/bin/gh-runners" "ui"`},
		{"spaces", "/opt/my apps/gh-runners", []string{"ui"}, `"/opt/my apps/gh-runners" "ui"`},
		{"double quote", `/opt/a"b/gh-runners`, []string{"ui"}, `"/opt/a\\"b/gh-runners" "ui"`},
		{"backslash", `/opt/a\b/gh-runners`, []string{"ui"}, `"/opt/a\\\\b/gh-runners" "ui"`},
		{"dollar", "/opt/a$b/gh-runners", []string{"ui"}, `"/opt/a\\$b/gh-runners" "ui"`},
		{"backtick", "/opt/a`b/gh-runners", []string{"ui"}, "\"/opt/a\\\\`b/gh-runners\" \"ui\""},
		{"percent", "/opt/a%b/gh-runners", []string{"ui"}, `"/opt/a%%b/gh-runners" "ui"`},
		{"home with spaces", "/usr/bin/gh-runners", []string{"ui", "--home", "/home/u/my runners"}, `"/usr/bin/gh-runners" "ui" "--home" "/home/u/my runners"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := execLine(tt.exe, tt.args); got != tt.want {
				t.Errorf("execLine = %s, want %s", got, tt.want)
			}
		})
	}
}
