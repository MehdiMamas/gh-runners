package fleet

import (
	"net"
	"strconv"
	"sync"
	"time"
)

// Package vars so tests can replace them.
var (
	listenRefresh = 30 * time.Second
	listenIPs     = tailscaleIPs
)

// ListenScoped opens the machines port. With lan it listens on every
// interface (":port"). Without, it listens on loopback and on each
// Tailscale address, and adds Tailscale addresses that appear later.
// fallback means a taken port is replaced by a free one; the port in use is
// then the one of the returned listener's Addr.
func ListenScoped(port int, lan, fallback bool, logf func(string, ...any)) (net.Listener, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	host := ""
	if !lan {
		host = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil && fallback {
		ln, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	if err != nil {
		return nil, err
	}
	if lan {
		return ln, nil
	}
	m := newMultiListener(ln, logf)
	m.addIPs(listenIPs())
	m.wg.Add(1)
	go m.refreshLoop()
	return m, nil
}

// multiListener merges the connections of several listeners that share
// one port. Addr is the first (loopback) one.
type multiListener struct {
	logf  func(string, ...any)
	port  int
	conns chan net.Conn
	done  chan struct{}

	mu   sync.Mutex
	lns  []net.Listener
	have map[string]bool
	shut bool
	wg   sync.WaitGroup
}

func newMultiListener(first net.Listener, logf func(string, ...any)) *multiListener {
	m := &multiListener{logf: logf, conns: make(chan net.Conn), done: make(chan struct{}), have: map[string]bool{}}
	m.port = first.Addr().(*net.TCPAddr).Port
	m.add(first, "")
	return m
}

// add serves ln; ip is its Tailscale address ("" for loopback). When ln
// fails outside Close, ip is forgotten so the next refresh listens again.
func (m *multiListener) add(ln net.Listener, ip string) {
	m.mu.Lock()
	if m.shut {
		m.mu.Unlock()
		_ = ln.Close()
		return
	}
	m.lns = append(m.lns, ln)
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				select {
				case <-m.done:
				default:
					m.logf("machines port: listener on %s stopped: %v", ln.Addr(), err)
					_ = ln.Close()
					if ip != "" {
						m.mu.Lock()
						delete(m.have, ip)
						m.mu.Unlock()
					}
				}
				return
			}
			select {
			case m.conns <- c:
			case <-m.done:
				_ = c.Close()
				return
			}
		}
	}()
}

// addIPs listens on port at each address not yet listened on; a failure is
// logged and skipped.
func (m *multiListener) addIPs(ips []string) {
	for _, ip := range ips {
		m.mu.Lock()
		skip := m.have[ip] || m.shut
		m.mu.Unlock()
		if skip {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(m.port)))
		if err != nil {
			m.logf("machines port: not listening on %s: %v", ip, err)
			continue
		}
		m.mu.Lock()
		m.have[ip] = true
		m.mu.Unlock()
		m.add(ln, ip)
	}
}

func (m *multiListener) refreshLoop() {
	defer m.wg.Done()
	t := time.NewTicker(listenRefresh)
	defer t.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-t.C:
			m.addIPs(listenIPs())
		}
	}
}

func (m *multiListener) Accept() (net.Conn, error) {
	select {
	case c := <-m.conns:
		return c, nil
	case <-m.done:
		return nil, net.ErrClosed
	}
}

func (m *multiListener) Addr() net.Addr {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lns[0].Addr()
}

func (m *multiListener) Close() error {
	m.mu.Lock()
	if m.shut {
		m.mu.Unlock()
		return nil
	}
	m.shut = true
	close(m.done)
	lns := m.lns
	m.mu.Unlock()
	for _, ln := range lns {
		_ = ln.Close()
	}
	m.wg.Wait()
	return nil
}

// listenPort is the port the peer listener asks for.
func (p *Peers) listenPort() int {
	if p.Port != nil {
		return p.Port()
	}
	return PeerPort
}
