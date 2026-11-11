package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// taskXML is the Task Scheduler definition of a per-user task that runs
// command every 5 minutes. The principal logs on with S4U (service for user),
// so the task also runs after a reboot before anybody signs in, and with
// LeastPrivilege, so creating it needs no admin rights and no UAC prompt.
// limit is the ExecutionTimeLimit, e.g. "PT5M".
func taskXML(name, user, command, args, limit string) string {
	e := xmlEscape
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>` + "\n")
	b.WriteString(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n")
	b.WriteString("  <RegistrationInfo><Description>" + e(name) + "</Description></RegistrationInfo>\n")
	b.WriteString("  <Triggers><TimeTrigger>" +
		"<Repetition><Interval>PT5M</Interval></Repetition>" +
		"<StartBoundary>2026-01-01T00:00:00</StartBoundary>" +
		"<Enabled>true</Enabled></TimeTrigger></Triggers>\n")
	b.WriteString(`  <Principals><Principal id="Author"><UserId>` + e(user) + `</UserId>` +
		`<LogonType>S4U</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>` + "\n")
	b.WriteString("  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>" +
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>" +
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>" +
		"<StartWhenAvailable>true</StartWhenAvailable>" +
		"<ExecutionTimeLimit>" + e(limit) + "</ExecutionTimeLimit>" +
		"<Enabled>true</Enabled><Hidden>true</Hidden></Settings>\n")
	b.WriteString(`  <Actions Context="Author"><Exec><Command>` + e(command) + `</Command>` +
		`<Arguments>` + e(args) + `</Arguments></Exec></Actions>` + "\n")
	b.WriteString("</Task>\n")
	return b.String()
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// encodeUTF16 is the file form schtasks /XML expects: UTF-16LE with a BOM.
func encodeUTF16(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+2*len(u))
	out = append(out, 0xFF, 0xFE)
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

// needS4U tells whether a task read back with `schtasks /Query /XML` still
// logs on with an interactive token and must be recreated. A query that
// failed (task missing) or an S4U task needs nothing.
func needS4U(queryXML string, queryErr error) bool {
	if queryErr != nil {
		return false
	}
	// The output may be UTF-16 with NUL bytes between the characters.
	s := strings.ReplaceAll(queryXML, "\x00", "")
	return strings.Contains(s, "<LogonType>InteractiveToken</LogonType>")
}

// registerTaskXML writes the definition to a temporary file in dir (a path
// the OS this runs on can write) and creates the task from it with create,
// which gets the schtasks arguments. winPath turns the file name into the
// path schtasks.exe sees. The file is deleted afterwards.
func registerTaskXML(create func(args ...string) error, name, def, dir string, winPath func(base string) string) error {
	f, err := os.CreateTemp(dir, "gh-runners-task-*.xml")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.Write(encodeUTF16(def)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := create("/Create", "/F", "/TN", name, "/XML", winPath(filepath.Base(path))); err != nil {
		return fmt.Errorf("schtasks /Create /XML: %w", err)
	}
	return nil
}
