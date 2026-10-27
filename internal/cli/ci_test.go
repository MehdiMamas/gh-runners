package cli

import (
	"bytes"
	"strings"
	"testing"
)

func runCIConfig(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newCIConfigCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestCIConfigWatch(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if got := runCIConfig(t, "o/r"); !strings.Contains(got, "auto-run off") {
		t.Fatalf("default: %q", got)
	}
	if got := runCIConfig(t, "o/r", "--watch", "on"); !strings.Contains(got, "auto-run on, post results off") {
		t.Fatalf("watch on: %q", got)
	}
	if got := runCIConfig(t, "o/r", "--post", "on"); !strings.Contains(got, "auto-run on, post results on") {
		t.Fatalf("watch kept: %q", got)
	}
	if got := runCIConfig(t, "o/r", "--watch", "off"); !strings.Contains(got, "auto-run off") {
		t.Fatalf("watch off: %q", got)
	}
	cmd := newCIConfigCmd()
	cmd.SetArgs([]string{"o/r", "--watch", "maybe"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if cmd.Execute() == nil {
		t.Fatal("bad value accepted")
	}
}
