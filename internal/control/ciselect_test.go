package control

import (
	"strings"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/localci"
)

var testMachines = []localci.Machine{
	{Name: "mac", Host: true},
	{ID: "m1", Name: "DESKTOP-MEHDI"},
	{ID: "m2", Name: "laptop"},
}

func TestSelectCIMachineKeepsNamedDropsOthers(t *testing.T) {
	for _, name := range []string{"DESKTOP-MEHDI", "desktop-mehdi", "m1"} {
		got, err := SelectCIMachine(testMachines, name)
		if err != nil || len(got) != 1 || got[0].ID != "m1" {
			t.Fatalf("%s: %v %v", name, got, err)
		}
	}
	if got, _ := SelectCIMachine(testMachines, ""); len(got) != 3 {
		t.Fatalf("empty name should keep all, got %v", got)
	}
}

func TestSelectCIMachineUnknownErrors(t *testing.T) {
	_, err := SelectCIMachine(testMachines, "nope")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "laptop") {
		t.Fatalf("got %v", err)
	}
}
