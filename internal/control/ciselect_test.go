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

func TestSelectCIMachineClearsInUseOnPin(t *testing.T) {
	ms := []localci.Machine{{Name: "desk", Host: true, InUse: true}, {ID: "x", Name: "other", InUse: true}}
	got, err := SelectCIMachine(ms, "desk")
	if err != nil || len(got) != 1 || got[0].InUse {
		t.Fatalf("pinned machine keeps InUse: %+v %v", got, err)
	}
	if all, _ := SelectCIMachine(ms, ""); !all[0].InUse || !all[1].InUse {
		t.Fatal("no pin must leave InUse alone")
	}
}
