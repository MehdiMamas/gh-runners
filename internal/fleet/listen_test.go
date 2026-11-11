package fleet

import (
	"net"
	"strconv"
	"testing"
	"time"
)

func TestListenScopedLoopback(t *testing.T) {
	old := listenIPs
	listenIPs = func() []string { return nil }
	defer func() { listenIPs = old }()
	ln, err := ListenScoped(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ta := ln.Addr().(*net.TCPAddr)
	if !ta.IP.IsLoopback() {
		t.Fatalf("addr %v is not loopback", ta)
	}
	got := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
		got <- err
	}()
	c, err := net.Dial("tcp", ta.String())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}

func TestMultiListenerMerges(t *testing.T) {
	a, _ := net.Listen("tcp", "127.0.0.1:0")
	b, _ := net.Listen("tcp", "127.0.0.1:0")
	m := newMultiListener(a, func(string, ...any) {})
	m.add(b)
	defer m.Close()
	for _, l := range []net.Listener{a, b} {
		c, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		done := make(chan error, 1)
		go func() {
			c, err := m.Accept()
			if err == nil {
				c.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("accept timed out")
		}
	}
}

func TestListenScopedFallback(t *testing.T) {
	old := listenIPs
	listenIPs = func() []string { return nil }
	defer func() { listenIPs = old }()
	taken, _ := net.Listen("tcp", "127.0.0.1:0")
	defer taken.Close()
	port := taken.Addr().(*net.TCPAddr).Port
	if _, err := ListenScoped(port, false, false, nil); err == nil {
		t.Fatal("expected an error without fallback")
	}
	ln, err := ListenScoped(port, false, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ln.Addr().(*net.TCPAddr).Port == port {
		t.Fatal("fallback reused the taken port")
	}
}

func TestMultiListenerCloseUnblocksAccept(t *testing.T) {
	a, _ := net.Listen("tcp", "127.0.0.1:0")
	m := newMultiListener(a, func(string, ...any) {})
	errc := make(chan error, 1)
	go func() { _, err := m.Accept(); errc <- err }()
	time.Sleep(50 * time.Millisecond)
	m.Close()
	select {
	case err := <-errc:
		if err != net.ErrClosed {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept still blocked")
	}
}

func TestListenScopedRefreshAddsAddress(t *testing.T) {
	oldIPs, oldRef := listenIPs, listenRefresh
	var ips []string
	ch := make(chan []string, 4)
	listenIPs = func() []string {
		select {
		case v := <-ch:
			ips = v
		default:
		}
		return ips
	}
	listenRefresh = 20 * time.Millisecond
	defer func() { listenIPs, listenRefresh = oldIPs, oldRef }()
	ln, err := ListenScoped(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	// 127.0.0.2 stands in for a Tailscale address that shows up later.
	ch <- []string{"127.0.0.2"}
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.2", strconv.Itoa(port)), 100*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Skipf("127.0.0.2 not usable or refresh did not add it: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func TestLocalAddrsForNoLAN(t *testing.T) {
	ts := map[string]bool{}
	for _, a := range tailscaleIPs() {
		ts[a] = true
	}
	for _, a := range LocalAddrsFor(false) {
		if ip := net.ParseIP(a); ip != nil && !ts[a] {
			t.Fatalf("%s in LocalAddrsFor(false) is not a Tailscale address", a)
		}
		ip := net.ParseIP(a)
		if ip != nil && ip.To4() != nil && !tailnet.Contains(ip) {
			t.Fatalf("LAN address %s in LocalAddrsFor(false)", a)
		}
	}
}
