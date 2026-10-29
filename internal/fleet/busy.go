package fleet

// CIBusy says why a remote CI job running here should not be cut off, or "".
func (m *Member) CIBusy() string {
	if m == nil || m.ciRun().busy() == 0 {
		return ""
	}
	return "a remote CI job is running"
}

// CIBusy says why a CI job sent here by another PC is running, or "".
func (p *Peers) CIBusy() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	run := p.run
	p.mu.Unlock()
	if run == nil || run.ci == nil || run.ci.busy() == 0 {
		return ""
	}
	return "a remote CI job is running"
}
