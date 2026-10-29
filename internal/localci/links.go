package localci

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// symlink is os.Symlink; tests swap it to act like a PC that refuses links.
var symlink = os.Symlink

// pendingLink is a tarball symlink the OS refused to create.
type pendingLink struct{ target, linkname string }

// resolveLinks stands in for symlinks the OS refused: it copies what each
// link points at, when that is inside dest. It goes in passes so a link to a
// link works. Links it can't resolve come back as log lines.
func resolveLinks(dest string, links []pendingLink) []string {
	for progress := true; progress && len(links) > 0; {
		progress = false
		var left []pendingLink
		for _, l := range links {
			src := filepath.Join(filepath.Dir(l.target), filepath.FromSlash(l.linkname))
			if filepath.IsAbs(l.linkname) || (src != dest && !strings.HasPrefix(src, dest+string(filepath.Separator))) || copyLinkTarget(src, l.target) != nil {
				left = append(left, l)
				continue
			}
			progress = true
		}
		links = left
	}
	var skipped []string
	for _, l := range links {
		rel, _ := filepath.Rel(dest, l.target)
		skipped = append(skipped, fmt.Sprintf("skipped symlink %s -> %s (this PC can't create symlinks)", filepath.ToSlash(rel), l.linkname))
	}
	return skipped
}

// copyLinkTarget copies a file (keeping its mode) or a directory (skipping the
// symlinks inside it) from src to dst.
func copyLinkTarget(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return nil
		case d.IsDir():
			return os.MkdirAll(out, 0o755)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(p, out, fi.Mode())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode&0o777|0o200)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}
