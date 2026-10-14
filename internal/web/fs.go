package web

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

// The folder picker: a browser cannot hand a real path to a page, so the
// page browses folders through these calls.

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type dirView struct {
	Path   string     `json:"path"`
	Parent string     `json:"parent,omitempty"`
	Dirs   []dirEntry `json:"dirs"`
	Roots  []dirEntry `json:"roots"`
	Error  string     `json:"error,omitempty"`
}

func roots() []dirEntry {
	var out []dirEntry
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, dirEntry{"Home", home})
	}
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			d := string(c) + `:\`
			if _, err := os.Stat(d); err == nil {
				out = append(out, dirEntry{string(c) + ":", d})
			}
		}
		return out
	}
	out = append(out, dirEntry{"/", "/"})
	if runtime.GOOS == "darwin" {
		if es, err := os.ReadDir("/Volumes"); err == nil {
			for _, e := range es {
				out = append(out, dirEntry{e.Name(), filepath.Join("/Volumes", e.Name())})
			}
		}
	}
	return out
}

func (s *Server) getFS(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p = filepath.Dir(store.RunnersDir())
	}
	if !filepath.IsAbs(p) {
		fail(w, http.StatusBadRequest, errors.New("not a full path"))
		return
	}
	p = filepath.Clean(p)
	// Walk up to the nearest folder that exists, so a typed path that is
	// not created yet still opens somewhere sensible.
	for {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			break
		}
		up := filepath.Dir(p)
		if up == p {
			break
		}
		p = up
	}
	v := dirView{Path: p, Dirs: []dirEntry{}, Roots: roots()}
	if up := filepath.Dir(p); up != p {
		v.Parent = up
	}
	hidden := r.URL.Query().Get("hidden") == "1"
	es, err := os.ReadDir(p)
	if err != nil {
		v.Error = err.Error()
	}
	for _, e := range es {
		name := e.Name()
		if !hidden && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$")) {
			continue
		}
		full := filepath.Join(p, name)
		if !e.IsDir() {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if st, err := os.Stat(full); err != nil || !st.IsDir() {
				continue
			}
		}
		v.Dirs = append(v.Dirs, dirEntry{name, full})
	}
	sort.Slice(v.Dirs, func(i, j int) bool { return strings.ToLower(v.Dirs[i].Name) < strings.ToLower(v.Dirs[j].Name) })
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) postMkdir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Parent string `json:"parent"`
		Name   string `json:"name"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if !filepath.IsAbs(body.Parent) || name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:*?"<>|`) {
		fail(w, http.StatusBadRequest, errors.New(`pick a folder name without / \ : * ? " < > |`))
		return
	}
	p := filepath.Join(filepath.Clean(body.Parent), name)
	if err := os.Mkdir(p, 0o755); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, dirEntry{name, p})
}
