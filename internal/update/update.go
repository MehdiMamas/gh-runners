// Package update replaces the gh-runners binary with the latest release.
// It uses the same files and checksums as install.sh and install.ps1.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo publishes the releases.
const Repo = "MehdiMamas/gh-runners"

// Base is where releases are downloaded from (a test server in tests).
var Base = "https://github.com"

var client = &http.Client{
	Timeout: 5 * time.Minute,
	// Latest reads the redirect itself.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.Header.Get("X-No-Redirect") != "" {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	},
}

// Latest returns the tag of the latest release, like "v0.3.0". It follows
// github.com's /releases/latest redirect, which needs no token and has no
// API rate limit.
func Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, Base+"/"+Repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-No-Redirect", "1")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode/100 != 3 || loc == "" {
		return "", fmt.Errorf("latest release: unexpected answer %s", resp.Status)
	}
	tag := path.Base(loc)
	if _, ok := parse(tag); !ok {
		return "", fmt.Errorf("latest release: unexpected tag in %s", loc)
	}
	return tag, nil
}

// Newer reports whether latest is a newer release than current. Builds
// that aren't releases ("dev", or anything not vX.Y.Z) never update.
func Newer(latest, current string) bool {
	l, ok1 := parse(latest)
	c, ok2 := parse(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if l.n[i] != c.n[i] {
			return l.n[i] > c.n[i]
		}
	}
	// v1.2.3-rc.1 comes before v1.2.3.
	return c.pre != "" && (l.pre == "" || l.pre > c.pre)
}

// Releasable reports whether a version string is a release that can update.
func Releasable(v string) bool { _, ok := parse(v); return ok }

type semver struct {
	n   [3]int
	pre string
}

func parse(v string) (semver, bool) {
	var s semver
	if !strings.HasPrefix(v, "v") {
		return s, false
	}
	v = v[1:]
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, s.pre = v[:i], v[i+1:]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return s, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return s, false
		}
		s.n[i] = n
	}
	return s, true
}

// Asset is the release file for this OS and CPU, as install.sh picks it.
func Asset() string { return AssetFor(runtime.GOOS, runtime.GOARCH) }

func AssetFor(goos, goarch string) string {
	name := "gh-runners-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// Apply downloads release tag, checks it and puts it in place of exe.
// The running process keeps its old copy until it restarts.
func Apply(ctx context.Context, tag, exe string, log io.Writer) error {
	staged, err := Download(ctx, tag, filepath.Dir(exe), log)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	if err := Replace(staged, exe); err != nil {
		return err
	}
	fmt.Fprintf(log, "Installed %s at %s\n", tag, exe)
	return nil
}

// Download fetches release tag into dir (next to the binary, so Replace is
// a rename), checks it against the release's checksums.txt and returns its
// path. The caller removes it if it isn't used.
func Download(ctx context.Context, tag, dir string, log io.Writer) (string, error) {
	asset := Asset()
	dl := Base + "/" + Repo + "/releases/download/" + tag + "/"
	fmt.Fprintf(log, "Downloading %s %s ...\n", asset, tag)
	sums, err := get(ctx, dl+"checksums.txt", 1<<20)
	if err != nil {
		return "", err
	}
	want := checksum(sums, asset)
	if want == "" {
		return "", fmt.Errorf("%s is not in the release's checksums.txt", asset)
	}
	tmp, err := os.CreateTemp(dir, ".gh-runners-update-*")
	if err != nil {
		return "", fmt.Errorf("can't write to %s (%v); run the install command again instead", dir, err)
	}
	h := sha256.New()
	err = download(ctx, dl+asset, io.MultiWriter(tmp, h))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			err = fmt.Errorf("checksum mismatch for %s: want %s, got %s", asset, want, got)
		}
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// Replace swaps src in for exe. Windows can't overwrite a running exe but
// can rename it, so the old one moves aside to exe.old first.
func Replace(src, exe string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(src, exe)
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(src, exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	return nil
}

// Cleanup removes the copy a Windows update left behind, once nothing runs it.
func Cleanup(exe string) { _ = os.Remove(exe + ".old") }

func checksum(sums []byte, asset string) string {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func get(ctx context.Context, url string, limit int64) ([]byte, error) {
	var b strings.Builder
	if err := download(ctx, url, &limitWriter{&b, limit}); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func download(ctx context.Context, url string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

type limitWriter struct {
	w io.Writer
	n int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.n {
		return 0, errors.New("response too large")
	}
	l.n -= int64(len(p))
	return l.w.Write(p)
}
