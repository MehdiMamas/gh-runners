// Package shortcut adds gh-runners to the app launcher (Start menu,
// ~/Applications, the desktop's application menu), so the dashboard opens
// with a click instead of a terminal command.
package shortcut

import (
	"os"

	"github.com/MehdiMamas/gh-runners/internal/service"
)

// Install creates or updates the shortcut and returns where it is.
func Install() (string, error) {
	exe, err := service.Executable()
	if err != nil {
		return "", err
	}
	return install(exe, uiArgs(os.Getenv("GH_RUNNERS_HOME")))
}

// uiArgs is the command line after the executable: "ui", plus --home when
// home is set.
func uiArgs(home string) []string {
	if home == "" {
		return []string{"ui"}
	}
	return []string{"ui", "--home", home}
}

// Uninstall removes the shortcut; it is not an error if there is none.
func Uninstall() (string, error) { return uninstall() }

// Path is where the shortcut lives on this OS.
func Path() string { return path() }
