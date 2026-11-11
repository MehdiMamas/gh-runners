package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

const goldenKeepalive = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>gh-runners keepalive</Description></RegistrationInfo>
  <Triggers><TimeTrigger><Repetition><Interval>PT5M</Interval></Repetition><StartBoundary>2026-01-01T00:00:00</StartBoundary><Enabled>true</Enabled></TimeTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>PC\me</UserId><LogonType>S4U</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><StartWhenAvailable>true</StartWhenAvailable><ExecutionTimeLimit>PT72H</ExecutionTimeLimit><Enabled>true</Enabled><Hidden>true</Hidden></Settings>
  <Actions Context="Author"><Exec><Command>conhost.exe</Command><Arguments>--headless &#34;C:\bin\gh-runners.exe&#34; daemon --detach --keepalive</Arguments></Exec></Actions>
</Task>
`

func TestKeepaliveXMLGolden(t *testing.T) {
	if got := keepaliveXML(`C:\bin\gh-runners.exe`, "", `PC\me`); got != goldenKeepalive {
		t.Errorf("got:\n%s", got)
	}
}

func TestTaskXMLEscaping(t *testing.T) {
	got := taskXML("a&b", `D\<u>`, "c.exe", `x "y" <z> & 'w'`, "PT5M")
	for _, want := range []string{
		"<Description>a&amp;b</Description>",
		`<UserId>D\&lt;u&gt;</UserId>`,
		"x &#34;y&#34; &lt;z&gt; &amp; &#39;w&#39;",
		"<ExecutionTimeLimit>PT5M</ExecutionTimeLimit>",
		"<LogonType>S4U</LogonType>", "<RunLevel>LeastPrivilege</RunLevel>",
		"<Interval>PT5M</Interval>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "InteractiveToken") {
		t.Error("interactive token in S4U task")
	}
}

func TestEncodeUTF16(t *testing.T) {
	b := encodeUTF16("a\u00e9\U0001F600")
	if b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("no BOM: % x", b[:2])
	}
	var u []uint16
	for i := 2; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	if got := string(utf16.Decode(u)); got != "a\u00e9\U0001F600" {
		t.Errorf("round trip: %q", got)
	}
}

func TestNeedS4U(t *testing.T) {
	inter := `<Principal id="Author"><UserId>PC\me</UserId><LogonType>InteractiveToken</LogonType></Principal>`
	s4u := `<Principal id="Author"><LogonType>S4U</LogonType></Principal>`
	utf16ish := strings.Join(strings.Split(inter, ""), "\x00")
	for _, c := range []struct {
		name string
		xml  string
		err  error
		want bool
	}{
		{"interactive", inter, nil, true},
		{"interactive utf16", utf16ish, nil, true},
		{"s4u", s4u, nil, false},
		{"missing task", "", errors.New("exit status 1"), false},
		{"error output mentions it", inter, errors.New("x"), false},
	} {
		if got := needS4U(c.xml, c.err); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestRegisterTaskXML(t *testing.T) {
	dir := t.TempDir()
	var gotArgs []string
	var content []byte
	err := registerTaskXML(func(args ...string) error {
		gotArgs = args
		content, _ = os.ReadFile(filepath.Join(dir, filepath.Base(strings.ReplaceAll(args[len(args)-1], `\`, "/"))))
		return nil
	}, "my task", "<Task/>", dir, func(base string) string { return `C:\w\` + base })
	if err != nil {
		t.Fatal(err)
	}
	if len(gotArgs) != 6 || gotArgs[0] != "/Create" || gotArgs[1] != "/F" || gotArgs[2] != "/TN" ||
		gotArgs[3] != "my task" || gotArgs[4] != "/XML" || !strings.HasPrefix(gotArgs[5], `C:\w\gh-runners-task-`) {
		t.Errorf("args %q", gotArgs)
	}
	if len(content) < 2 || content[0] != 0xFF || content[1] != 0xFE {
		t.Errorf("file not UTF-16 with BOM")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Errorf("temp file left: %v", left)
	}
	err = registerTaskXML(func(...string) error { return errors.New("denied") }, "t", "<Task/>", dir, func(b string) string { return b })
	if err == nil {
		t.Error("error swallowed")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Errorf("temp file left after error: %v", left)
	}
}
