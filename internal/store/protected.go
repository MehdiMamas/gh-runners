package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// protectedUnderHome are the folders macOS guards with permission prompts
// (Documents, Desktop, Downloads, Pictures, ...) and iCloud Drive. A runner
// that works inside one makes macOS ask "gh-runners wants to access files",
// and the only way to silence it is Full Disk Access, which a runner running
// CI code must not get (#194).
var protectedUnderHome = []string{
	"Documents", "Desktop", "Downloads", "Pictures", "Movies", "Music",
	filepath.Join("Library", "Mobile Documents"),
	filepath.Join("Library", "CloudStorage"),
}

// ProtectedFolder returns the name of the macOS-protected folder under home
// that dir is in (or equals), or "" when it is not in one or the OS is not macOS.
func ProtectedFolder(dir string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return protectedFolderIn(dir, home)
}

func protectedFolderIn(dir, home string) string {
	dir, home = filepath.Clean(dir), filepath.Clean(home)
	for _, p := range protectedUnderHome {
		full := filepath.Join(home, p)
		if strings.EqualFold(dir, full) || strings.HasPrefix(strings.ToLower(dir), strings.ToLower(full)+string(filepath.Separator)) {
			return "~/" + filepath.ToSlash(p)
		}
	}
	return ""
}
