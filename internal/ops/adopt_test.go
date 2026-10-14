package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

// fakeRunnerDir writes what config.sh leaves behind.
func fakeRunnerDir(t *testing.T, dir, name, url string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := "\xef\xbb\xbf{\n  \"agentId\": 21,\n  \"agentName\": \"" + name + "\",\n  \"gitHubUrl\": \"" + url + "\",\n  \"workFolder\": \"_work\"\n}"
	for f, body := range map[string]string{".runner": settings, "config.sh": "#!/bin/sh\n", "config.cmd": "@echo off\n"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanFindsHandMadeRunners(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GH_RUNNERS_HOME", filepath.Join(root, ".gh-runners"))
	fakeRunnerDir(t, filepath.Join(root, "actions-runner"), "Macbook M4", "https://github.com/MehdiMamas/klawly")
	fakeRunnerDir(t, filepath.Join(root, "actions-runner-light"), "Macbook M4 light", "https://github.com/MehdiMamas/klawly")
	fakeRunnerDir(t, filepath.Join(root, "runners", "spotter"), "mac", "https://github.com/MehdiMamas/spotter/")
	fakeRunnerDir(t, filepath.Join(root, "Projects", "deep", "x"), "too-deep", "https://github.com/a/b")
	fakeRunnerDir(t, filepath.Join(root, "org-runner"), "org", "https://github.com/some-org")
	fakeRunnerDir(t, filepath.Join(root, "ghes"), "ghes", "https://ghe.example.com/a/b")
	// Runners gh-runners already manages are not "found".
	fakeRunnerDir(t, filepath.Join(store.RunnersDir(), "MehdiMamas__klawly"), "managed", "https://github.com/MehdiMamas/klawly")

	got := map[string]Found{}
	for _, f := range Scan([]string{root}) {
		got[f.Name] = f
	}
	if len(got) != 5 {
		t.Fatalf("found %d runners: %+v", len(got), got)
	}
	if f := got["Macbook M4"]; f.Scope != "MehdiMamas/klawly" || f.Kind != store.KindRepo || f.Problem != "" {
		t.Errorf("Macbook M4: %+v", f)
	}
	if f := got["mac"]; f.Scope != "MehdiMamas/spotter" {
		t.Errorf("trailing slash: %+v", f)
	}
	if f := got["org"]; f.Kind != store.KindOrg || f.Scope != "some-org" {
		t.Errorf("org: %+v", f)
	}
	if f := got["ghes"]; f.Problem == "" {
		t.Errorf("a GHES runner should not be movable: %+v", f)
	}
	if _, ok := got["too-deep"]; ok {
		t.Error("scanned too deep")
	}
}

func TestClaimID(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	base := store.IDFor(store.KindRepo, "MehdiMamas", "klawly")
	want := []string{base, base + "__Macbook-M4", base + "__Macbook-M4-2"}
	var got []string
	for range want {
		id, err := claimID(base, "Macbook M4")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(store.RunnersDir(), id)); err != nil {
			t.Fatalf("%s not claimed: %v", id, err)
		}
		got = append(got, id)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("claimID = %v, want %v", got, want)
	}
}

func TestContainsPath(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"Users", "me", "actions-runner")
	sep := string(filepath.Separator)
	cases := map[string]bool{
		"<string>" + dir + sep + "runsvc.sh</string>": true,
		"<string>" + dir + "</string>":                true,
		"<string>" + dir + "-light" + sep + "run.sh":  false,
		"nothing here": false,
	}
	for text, want := range cases {
		if got := containsPath(text, dir); got != want {
			t.Errorf("containsPath(%q) = %v, want %v", text, got, want)
		}
	}
}
