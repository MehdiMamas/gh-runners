package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.3.0", "v0.2.0", true},
		{"v0.2.1", "v0.2.0", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.10.0", "v0.9.0", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.1.9", "v0.2.0", false},
		{"v0.3.0", "v0.3.0-rc.1", true},
		{"v0.3.0-rc.2", "v0.3.0-rc.1", true},
		{"v0.3.0-rc.1", "v0.3.0", false},
		{"v0.3.0", "dev", false},
		{"v0.3.0", "v0.2.0-3-gabc", true},
		{"garbage", "v0.2.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestAssetFor(t *testing.T) {
	if got := AssetFor("windows", "amd64"); got != "gh-runners-windows-amd64.exe" {
		t.Error(got)
	}
	if got := AssetFor("darwin", "arm64"); got != "gh-runners-darwin-arm64" {
		t.Error(got)
	}
}

// fakeReleases serves /releases/latest and one release's files the way
// github.com does.
func fakeReleases(t *testing.T, tag string, body []byte, sum string) {
	t.Helper()
	asset := Asset()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/"+Repo+"/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/"+Repo+"/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  gh-runners-other-os\n%s  %s\n", sum, sum, asset)
	})
	mux.HandleFunc("/"+Repo+"/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := Base
	Base = srv.URL
	t.Cleanup(func() { Base = old })
}

func TestLatestAndApply(t *testing.T) {
	body := []byte("new binary")
	h := sha256.Sum256(body)
	fakeReleases(t, "v9.9.9", body, hex.EncodeToString(h[:]))

	tag, err := Latest(context.Background())
	if err != nil || tag != "v9.9.9" {
		t.Fatalf("Latest = %q, %v", tag, err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "gh-runners")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), tag, exe, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatalf("exe = %q", b)
	}
	Cleanup(exe)
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("left behind: %v", left)
	}
}

func TestApplyRejectsBadChecksum(t *testing.T) {
	fakeReleases(t, "v9.9.9", []byte("tampered"), "00")
	dir := t.TempDir()
	exe := filepath.Join(dir, "gh-runners")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), "v9.9.9", exe, io.Discard); err == nil {
		t.Fatal("want a checksum error")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old binary" {
		t.Fatalf("exe changed to %q", b)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("left behind: %v", left)
	}
}
