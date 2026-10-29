package fleet

import (
	"sync/atomic"
	"testing"
	"time"
)

// SendStatus tells the linked PC the new Info at once, not at the next
// status tick (an hour away here).
func TestPeerSendStatusIsImmediate(t *testing.T) {
	g := newFakeGH(t)
	var host atomic.Value
	host.Store("alpha")
	a := newPeerWith(t, g, "alpha", func(p *Peers) {
		p.DialEvery = time.Hour
		p.Info = func() Info { return Info{Hostname: host.Load().(string)} }
	})
	b := newPeer(t, g, "beta")
	waitFor(t, func() bool { return onlineWith(a.Peers, b.ID()) && onlineWith(b.Peers, a.ID()) })
	settled(t, a.Peers, b.Peers)
	host.Store("alpha-paused")
	a.SendStatus()
	waitFor(t, func() bool {
		for _, x := range b.Peers.Peers() {
			if x.ID == a.ID() && x.Info.Hostname == "alpha-paused" {
				return true
			}
		}
		return false
	})
}
