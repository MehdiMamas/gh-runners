package localci

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
)

// Machine is one place a job can run: this machine or a fleet member.
type Machine struct {
	ID         string // fleet member id; empty for the local machine
	Name       string
	OS         string // runtime.GOOS value: darwin, windows, linux
	Arch       string
	Tools      []string
	EmulatorOK bool // may run jobs that start the Firebase emulators
	Host       bool // the machine running the command
}

// osFits reports whether a runs-on label can run on the OS.
func osFits(runsOn, goos string) bool {
	l := strings.ToLower(runsOn)
	switch {
	case strings.HasPrefix(l, "windows"):
		return goos == "windows"
	case strings.HasPrefix(l, "macos"):
		return goos == "darwin"
	}
	return true
}

// Route picks the machine for a job. Candidates need the job's OS, tools,
// and for emulator jobs the emulator opt-in. Flutter jobs prefer the host;
// otherwise the least loaded machine wins, ties going to the host. When
// nothing fits the reason says what is missing.
func Route(j *Job, machines []Machine, load func(id string) int) (Machine, string) {
	tools, emu := j.Requires()
	var ok []Machine
	why := map[string]bool{}
	for _, m := range machines {
		switch {
		case !osFits(j.RunsOn, m.OS):
			why["runs-on "+j.RunsOn+" needs a matching OS"] = true
		case len(Missing(m.Tools, tools)) > 0:
			why[fmt.Sprintf("%s lacks %s", m.Name, strings.Join(Missing(m.Tools, tools), ", "))] = true
		case emu && !m.EmulatorOK:
			why[m.Name+" is not set up for emulator jobs"] = true
		default:
			ok = append(ok, m)
		}
	}
	if len(ok) == 0 {
		var parts []string
		for k := range why {
			parts = append(parts, k)
		}
		sortStrings(parts)
		r := "no machine can run it"
		if len(parts) > 0 {
			r += ": " + strings.Join(parts, "; ")
		}
		return Machine{}, r
	}
	wantFlutter := false
	for _, t := range tools {
		wantFlutter = wantFlutter || t == "flutter"
	}
	if wantFlutter {
		for _, m := range ok {
			if m.Host {
				return m, ""
			}
		}
	}
	best := ok[0]
	for _, m := range ok[1:] {
		lb, lm := load(best.ID+"/"+best.Name), load(m.ID+"/"+m.Name)
		if lm < lb || (lm == lb && m.Host && !best.Host) {
			best = m
		}
	}
	return best, ""
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for k := i; k > 0 && s[k] < s[k-1]; k-- {
			s[k], s[k-1] = s[k-1], s[k]
		}
	}
}

// Dispatcher is an Executor that sends each job to the machine that fits.
type Dispatcher struct {
	Machines func() []Machine
	Remote   func(Machine) Executor // runs a job on a fleet member
	Local    Executor               // runs a job on the host; LocalExecutor if nil
	Locks    *Locks                 // the host's own locks, for emulator jobs

	mu   sync.Mutex
	busy map[string]int
}

// LocalMachine describes this machine.
func LocalMachine(name string, emulatorOK bool) Machine {
	return Machine{Name: name, OS: runtime.GOOS, Arch: runtime.GOARCH, Tools: DetectTools(), EmulatorOK: emulatorOK, Host: true}
}

// SelfLocking marks executors that take the emulator lock themselves, so Run
// does not hold one global lock across machines.
func (d *Dispatcher) SelfLocking() bool { return true }

func (d *Dispatcher) load(key string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.busy[key]
}

// RunJob routes the job and runs it there.
func (d *Dispatcher) RunJob(ctx context.Context, spec JobSpec) Result {
	var m Machine
	var reason string
	// Choose and count the job under one lock so parallel jobs spread out.
	d.mu.Lock()
	if d.busy == nil {
		d.busy = map[string]int{}
	}
	d.mu.Unlock()
	m, reason = d.pick(spec.Job)
	if reason != "" {
		return Result{Job: spec.Name, Status: Unsupported, Reason: reason}
	}
	key := m.ID + "/" + m.Name
	defer func() {
		d.mu.Lock()
		d.busy[key]--
		d.mu.Unlock()
	}()
	var res Result
	active := ActiveJob{Repo: spec.RunEnv.Repo, PR: PRFromRef(spec.RunEnv.Ref), Job: spec.Name, Machine: m.Name}
	if m.Host || m.ID == "" {
		local := d.Local
		if local == nil {
			local = LocalExecutor{}
		}
		if _, emu := spec.Job.Requires(); emu && d.Locks != nil {
			unlock, err := d.Locks.Lock("emulator")
			if err != nil {
				return Result{Job: spec.Name, Status: Fail, Reason: "lock: " + err.Error(), Machine: m.Name}
			}
			defer unlock()
		}
		done := StartActive(active)
		res = local.RunJob(ctx, spec)
		done()
	} else {
		done := StartActive(active)
		res = d.Remote(m).RunJob(ctx, spec)
		done()
	}
	res.Machine = m.Name
	return res
}

func (d *Dispatcher) pick(j *Job) (Machine, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, reason := Route(j, d.Machines(), func(k string) int { return d.busy[k] })
	if reason == "" {
		d.busy[m.ID+"/"+m.Name]++
	}
	return m, reason
}
