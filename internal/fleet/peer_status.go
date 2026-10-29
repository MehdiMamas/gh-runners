package fleet

// SendStatus sends the other account PCs this machine's status now instead of
// at the next status tick, so a pause or resume shows there at once.
func (p *Peers) SendStatus() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.run == nil {
		return
	}
	select {
	case p.run.statusNow <- struct{}{}:
	default:
	}
}
