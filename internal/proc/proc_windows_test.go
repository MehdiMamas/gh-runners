package proc

import (
	"strings"
	"testing"
)

// A PATH changed only in this process (as if the daemon started before a
// tool was installed) is replaced by the one in the registry.
func TestEnvRereadsRegistry(t *testing.T) {
	t.Setenv("PATH", `C:\stale`)
	t.Setenv("GH_RUNNERS_ONLY_HERE", "1")
	var path string
	here := false
	for _, kv := range Env() {
		k, v, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "PATH":
			path = v
		case "GH_RUNNERS_ONLY_HERE":
			here = true
		}
	}
	if path == "" || path == `C:\stale` || !strings.Contains(strings.ToLower(path), `system32`) {
		t.Fatalf("PATH = %q", path)
	}
	if !here {
		t.Fatal("variables set only in this process are kept")
	}
}

// Runners get Git's bash, gh and node appended to PATH when installed but
// missing, never twice, and PowerShell scripts allowed to run.
func TestJobEnv(t *testing.T) {
	defer func(f func(string, string) bool) { exeIn = f }(exeIn)
	exeIn = func(dir, name string) bool { return strings.EqualFold(dir, `D:\Git\cmd`) && name == "git" }
	installed := map[string]bool{
		`d:\git\bin`: true, `d:\git\usr\bin`: true, `c:\pf\git\bin`: true,
		`c:\pf\github cli`: true, `c:\pf\nodejs`: true,
	}
	exists := func(p string) bool { return installed[strings.ToLower(p)] }
	env := []string{`Path=C:\WINDOWS;D:\Git\cmd;C:\PF\nodejs\;`, `ProgramFiles=C:\PF`, `LOCALAPPDATA=C:\L`}
	got := map[string]string{}
	for _, kv := range jobEnv(env, exists) {
		k, v, _ := strings.Cut(kv, "=")
		got[strings.ToUpper(k)] = v
	}
	want := `C:\WINDOWS;D:\Git\cmd;C:\PF\nodejs\;D:\Git\bin;D:\Git\usr\bin;C:\PF\Git\bin;C:\PF\GitHub CLI`
	if got["PATH"] != want {
		t.Fatalf("PATH\n got %q\nwant %q", got["PATH"], want)
	}
	if got["PSEXECUTIONPOLICYPREFERENCE"] != "Bypass" {
		t.Fatalf("policy = %q", got["PSEXECUTIONPOLICYPREFERENCE"])
	}

	// A policy the user set is kept, and nothing missing leaves PATH alone.
	env = []string{`Path=C:\WINDOWS`, `PSExecutionPolicyPreference=AllSigned`}
	out := strings.Join(jobEnv(env, func(string) bool { return false }), "|")
	if out != `Path=C:\WINDOWS|PSExecutionPolicyPreference=AllSigned` {
		t.Fatalf("got %q", out)
	}
}

// Doctor flags each job tool that no PATH entry holds.
func TestMissingIn(t *testing.T) {
	exe := func(dir, name string) bool {
		return (dir == `C:\Git\bin` && name == "bash") || (dir == `C:\nodejs` && name == "node")
	}
	got := missingIn([]string{`Path=C:\Windows;;C:\Git\bin;C:\nodejs`}, exe)
	if strings.Join(got, ",") != "gh" {
		t.Fatalf("missing = %v, want [gh]", got)
	}
	if got := missingIn(nil, exe); len(got) != 3 {
		t.Fatalf("no PATH: missing = %v, want all three", got)
	}
}
