package localci

import (
	"context"
	"fmt"
	"os"
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
// nothing fits the reason says what is missing. With localOnly (a job that
// holds secrets) only the machine running the command may be picked.
func Route(j *Job, machines []Machine, load func(id string) int, localOnly bool) (Machine, string) {
	tools, emu := j.Requires()
	var ok []Machine
	why := map[string]bool{}
	for _, m := range machines {
		switch {
		case localOnly && !m.Host:
			why["needs secrets, which stay on this machine"] = true
		case !osFits(j.RunsOn, m.OS):
			why["runs-on "+j.RunsOn+" needs a matching OS"] = true
		case len(Missing(m.Tools, tools)) > 0:
			why[fmt.Sprintf("%s lacks %s (not found on this machine's PATH; check with gh-runners ci doctor)", m.Name, strings.Join(Missing(m.Tools, tools), ", "))] = true
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

// RunJob routes the job and runs it there. When a fleet machine drops mid-job
// and nothing is secret, the job runs once more on another machine; if that
// is not possible the result is Blocked, never a test failure.
func (d *Dispatcher) RunJob(ctx context.Context, spec JobSpec) Result {
	secret := len(spec.SecretEnv) > 0 || spec.SecretValues != nil
	m, reason := d.pick(spec.Job, secret, nil)
	if reason != "" {
		if secret {
			return Result{Job: spec.Name, Status: Blocked, Reason: "needs secrets, which stay on this machine: " + reason}
		}
		return Result{Job: spec.Name, Status: Unsupported, Reason: reason}
	}
	res := d.runOn(ctx, spec, m)
	if !res.Offline || ctx.Err() != nil || secret {
		return res
	}
	m2, reason := d.pick(spec.Job, secret, map[string]bool{m.ID + "/" + m.Name: true})
	if reason != "" {
		return Result{Job: spec.Name, Status: Blocked, Log: res.Log, Machine: m.Name,
			Reason: fmt.Sprintf("machine %s went offline; no other machine can run it", m.Name)}
	}
	res = d.runOn(ctx, spec, m2)
	prependLine(spec.Log, fmt.Sprintf("::retry machine %s went offline; running on %s\n", m.Name, m2.Name))
	if res.Offline && ctx.Err() == nil {
		return Result{Job: spec.Name, Status: Blocked, Log: res.Log, Machine: m2.Name,
			Reason: fmt.Sprintf("machines %s, %s went offline", m.Name, m2.Name)}
	}
	return res
}

// prependLine puts a line at the top of the log; the second run's executor
// starts the file afresh, so the note goes in after it.
func prependLine(path, line string) {
	if path == "" {
		return
	}
	old, _ := os.ReadFile(path)
	_ = os.WriteFile(path, append([]byte(line), old...), 0o644)
}

// runOn runs the job on m, whose busy count pick already raised, and
// releases that count when the job ends.
func (d *Dispatcher) runOn(ctx context.Context, spec JobSpec, m Machine) Result {
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
		if spec.Job.StartsServers() && d.Locks != nil {
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

// pick routes the job, skipping the excluded machine keys (ID/Name), and
// counts it on the chosen machine.
func (d *Dispatcher) pick(j *Job, localOnly bool, exclude map[string]bool) (Machine, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.busy == nil {
		d.busy = map[string]int{}
	}
	var ms []Machine
	for _, m := range d.Machines() {
		if !exclude[m.ID+"/"+m.Name] {
			ms = append(ms, m)
		}
	}
	m, reason := Route(j, ms, func(k string) int { return d.busy[k] }, localOnly)
	if reason == "" {
		d.busy[m.ID+"/"+m.Name]++
	}
	return m, reason
}
