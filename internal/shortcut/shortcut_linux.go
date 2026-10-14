package shortcut

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/MehdiMamas/gh-runners/internal/service"
)

func path() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "applications", "gh-runners"+service.Suffix()+".desktop")
}

// execArg quotes one argument of a desktop entry's Exec key.
func execArg(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return `"` + r.Replace(s) + `"`
}

func install(exe string, args []string) (string, error) {
	p := path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	line := execArg(exe)
	for _, a := range args {
		line += " " + execArg(a)
	}
	// The Exec value is itself unescaped once more for \ in desktop files.
	line = strings.ReplaceAll(line, `\`, `\\`)
	entry := "[Desktop Entry]\nType=Application\nName=gh-runners\nComment=Manage GitHub Actions runners on this computer\nExec=" + line +
		"\nIcon=utilities-terminal\nTerminal=false\nCategories=Development;\n"
	if err := os.WriteFile(p, []byte(entry), 0o644); err != nil {
		return "", err
	}
	return p, nil
}

func uninstall() (string, error) {
	p := path()
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return p, nil
}
