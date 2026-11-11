package service

import "testing"

func TestInteractiveOnly(t *testing.T) {
	for _, c := range []struct {
		name, out string
		want      logon
	}{
		{"interactive", "HostName: PC\r\nTaskName:    \\gh-runners keepalive\r\nLogon Mode:                           Interactive only\r\nLast Result: 0\r\n", logonInteractive},
		{"s4u", "TaskName: \\x\r\nLogon Mode:  Interactive/Background\r\n", logonOther},
		{"other locale label", "Nom de la tâche: \\x\r\nMode d'ouverture de session: Interactif uniquement\r\n", logonUnknown},
		{"translated value", "Logon Mode: Nur interaktiv\r\n", logonUnknown},
		{"empty", "", logonUnknown},
	} {
		if got := interactiveOnly(c.out); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestCreateTaskFallsBackWithoutNP(t *testing.T) {
	var calls [][]string
	err := createTask([]string{"/Create", "/NP"}, func(a []string) error {
		calls = append(calls, a)
		if len(a) == 2 {
			return errString("denied")
		}
		return nil
	})
	if err != nil || len(calls) != 2 || len(calls[1]) != 1 {
		t.Fatalf("err %v calls %q", err, calls)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
