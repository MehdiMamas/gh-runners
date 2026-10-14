package shortcut

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MehdiMamas/gh-runners/internal/service"
)

func path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Applications", "gh-runners"+service.Suffix()+".app")
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Info is the bundle's Info.plist. LSBackgroundOnly: the script opens the
// browser and exits, so no Dock icon bounces.
const info = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>gh-runners</string>
  <key>CFBundleDisplayName</key><string>gh-runners</string>
  <key>CFBundleIdentifier</key><string>io.github.mehdimamas.gh-runners%s.app</string>
  <key>CFBundleExecutable</key><string>gh-runners</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>LSBackgroundOnly</key><true/>
</dict>
</plist>
`

func install(exe string, args []string) (string, error) {
	app := path()
	bin := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(fmt.Sprintf(info, service.Suffix())), 0o644); err != nil {
		return "", err
	}
	line := "exec " + shQuote(exe)
	for _, a := range args {
		line += " " + shQuote(a)
	}
	script := "#!/bin/sh\n" + line + "\n"
	if err := os.WriteFile(filepath.Join(bin, "gh-runners"), []byte(script), 0o755); err != nil {
		return "", err
	}
	return app, nil
}

func uninstall() (string, error) {
	app := path()
	return app, os.RemoveAll(app)
}
