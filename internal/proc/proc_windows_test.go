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
