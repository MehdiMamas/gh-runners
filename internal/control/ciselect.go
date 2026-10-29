package control

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// SelectCIMachine keeps only the machine named by name (its name, ignoring
// case, or its id). An empty name keeps all. An unknown name is an error
// that lists the machines there are.
func SelectCIMachine(ms []localci.Machine, name string) ([]localci.Machine, error) {
	if name == "" {
		return ms, nil
	}
	var names []string
	for _, m := range ms {
		if strings.EqualFold(m.Name, name) || (m.ID != "" && m.ID == name) {
			return []localci.Machine{m}, nil
		}
		names = append(names, m.Name)
	}
	return nil, fmt.Errorf("no machine %q: this PC and the linked account PCs are %s", name, strings.Join(names, ", "))
}

func ciMachineList() []localci.Machine {
	host, _ := os.Hostname()
	cfg, _ := store.LoadConfig()
	ms := []localci.Machine{localci.LocalMachine(host, cfg.CIEmulatorOK())}
	if remote, err := CIMachines(); err == nil {
		ms = append(ms, remote...)
	}
	return ms
}

// CheckCIMachine fails with a plain error when name is not this PC or a
// linked account PC, so a run can stop before it starts.
func CheckCIMachine(name string) error {
	_, err := SelectCIMachine(ciMachineList(), name)
	return err
}

// CIDispatcherOn is CIDispatcher limited to one machine; empty means all.
func CIDispatcherOn(machine string) *localci.Dispatcher {
	return &localci.Dispatcher{
		Machines: func() []localci.Machine {
			ms, err := SelectCIMachine(ciMachineList(), machine)
			if err != nil {
				return nil // jobs then end as unsupported with the reason
			}
			return ms
		},
		Remote: func(m localci.Machine) localci.Executor { return CIExecutor{MemberID: m.ID} },
		Locks:  &localci.Locks{Dir: filepath.Join(store.Home(), "ci", "locks")},
	}
}
