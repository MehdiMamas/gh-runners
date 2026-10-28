// Package fleet lets one machine (the main host) manage the runners of
// other machines (members) from its dashboard.
//
// Only the main host listens beyond 127.0.0.1: on the fleet port, with TLS
// and a self-signed certificate it keeps in its state. A member dials out
// to the host, checks the certificate against the SHA-256 fingerprint it
// got in the join code (never a CA), and keeps that one connection open,
// so members need no open port. Both sides then send requests down it:
//
//   - the host sends dashboard API calls ("api") that the member runs
//     through its own /api/* handlers, limited to the allowlist in allow.go;
//   - the member sends the GitHub API calls it needs ("github"), which the
//     host makes with its own token, limited to the paths gh-runners uses.
//     The host's token never leaves the host.
//
// A join code carries the host's addresses, the fleet port, the certificate
// fingerprint and a one-time pairing secret that expires. Pairing swaps the
// secret for a long-lived member credential; the host keeps only its hash
// and can revoke it.
package fleet

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// DefaultPort is the main host's fleet port.
const DefaultPort = 47322

// PeerPort is where a signed-in machine listens for its account's other
// machines. It differs from DefaultPort so a main host can be a peer too; when
// it is taken any free port is used, since the directory carries the real one.
const PeerPort = 47323

const (
	maxFrame     = 16 << 20
	pingEvery    = 30 * time.Second
	readTimeout  = 90 * time.Second
	writeTimeout = 30 * time.Second
	// handshakeTimeout bounds the TLS handshake and the hello or pair frame.
	handshakeTimeout = 15 * time.Second
)

// Frame types.
const (
	tPair    = "pair"    // member → host: pairing secret and Info
	tPaired  = "paired"  // host → member: member id and credential
	tHello   = "hello"   // member → host: member id, credential and Info
	tWelcome = "welcome" // host → member: accepted; host name and addresses
	tDenied  = "denied"  // host → member: refused (Error says why), then close
	tStatus  = "status"  // member → host: fresh Info
	tLeave   = "leave"   // member → host: forget me
	tRemoved = "removed" // host → member: you were removed
	tReq     = "req"
	tResp    = "resp"
	tPing    = "ping"
	tPong    = "pong"
)

// Request kinds.
const (
	kindAPI    = "api"    // host → member: a dashboard API call
	kindGitHub = "github" // member → host: a GitHub API call
	kindFleet  = "fleet"  // member → host: a fleet dashboard call
)

// frame is one message on the connection. Requests and responses carry an
// id; the other types are one-way.
type frame struct {
	Type string `json:"type"`
	ID   uint64 `json:"id,omitempty"`

	Kind   string            `json:"kind,omitempty"`
	Method string            `json:"method,omitempty"`
	Path   string            `json:"path,omitempty"` // with the query
	Header map[string]string `json:"header,omitempty"`
	Body   []byte            `json:"body,omitempty"`
	Status int               `json:"status,omitempty"`
	Error  string            `json:"error,omitempty"`

	Secret     string   `json:"secret,omitempty"`
	MemberID   string   `json:"member_id,omitempty"`
	Credential string   `json:"credential,omitempty"`
	HostName   string   `json:"host_name,omitempty"`
	Addrs      []string `json:"addrs,omitempty"`
	Info       *Info    `json:"info,omitempty"`
}

// Info is what a member tells the host about itself.
type Info struct {
	Hostname string       `json:"hostname"`
	OS       string       `json:"os"`
	Arch     string       `json:"arch"`
	Version  string       `json:"version"`
	Runners  []RunnerInfo `json:"runners"`
	// Paused means the machine is paused: none of its runners run.
	Paused bool `json:"paused,omitempty"`
	// CI is set when this machine accepts CI jobs from the main host.
	CI *CIInfo `json:"ci,omitempty"`
}

// RunnerInfo is one runner of a member.
type RunnerInfo struct {
	ID     string   `json:"id"`
	Scope  string   `json:"scope"`
	Name   string   `json:"name"`
	State  string   `json:"state"`
	Labels []string `json:"labels"`
}

// conn frames JSON messages (a 4-byte length, then the JSON) and matches
// responses to requests.
type conn struct {
	nc  net.Conn
	wmu sync.Mutex

	mu      sync.Mutex
	next    uint64
	pending map[uint64]chan frame
	done    chan struct{}
	err     error
}

func newConn(nc net.Conn) *conn {
	return &conn{nc: nc, pending: map[uint64]chan frame{}, done: make(chan struct{})}
}

func (c *conn) send(f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(b) > maxFrame {
		return fmt.Errorf("message too large (%d bytes)", len(b))
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.nc.SetWriteDeadline(time.Now().Add(writeTimeout))
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	if _, err := c.nc.Write(append(n[:], b...)); err != nil {
		c.close(err)
		return err
	}
	return nil
}

func (c *conn) recv(timeout time.Duration) (frame, error) {
	_ = c.nc.SetReadDeadline(time.Now().Add(timeout))
	var n [4]byte
	if _, err := io.ReadFull(c.nc, n[:]); err != nil {
		return frame{}, err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > maxFrame {
		return frame{}, fmt.Errorf("message too large (%d bytes)", size)
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(c.nc, b); err != nil {
		return frame{}, err
	}
	var f frame
	if err := json.Unmarshal(b, &f); err != nil {
		return frame{}, fmt.Errorf("bad message: %w", err)
	}
	return f, nil
}

// call sends a request and waits for its response.
func (c *conn) call(ctx context.Context, f frame) (frame, error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return frame{}, c.err
	}
	c.next++
	f.Type, f.ID = tReq, c.next
	ch := make(chan frame, 1)
	c.pending[f.ID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, f.ID)
		c.mu.Unlock()
	}()
	if err := c.send(f); err != nil {
		return frame{}, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-c.done:
		return frame{}, c.closedErr()
	case <-ctx.Done():
		return frame{}, ctx.Err()
	}
}

// serve reads until the connection fails. Responses go to their callers,
// requests to handle (each in its own goroutine), anything else to other.
// It pings every 30 seconds, so a dead peer is noticed within 90.
func (c *conn) serve(handle func(frame) frame, other func(frame) error) error {
	go func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-t.C:
				_ = c.send(frame{Type: tPing})
			}
		}
	}()
	for {
		f, err := c.recv(readTimeout)
		if err != nil {
			c.close(err)
			return c.closedErr()
		}
		switch f.Type {
		case tResp:
			c.mu.Lock()
			ch := c.pending[f.ID]
			c.mu.Unlock()
			if ch != nil {
				ch <- f
			}
		case tReq:
			go func() {
				r := handle(f)
				r.Type, r.ID = tResp, f.ID
				_ = c.send(r)
			}()
		case tPing:
			_ = c.send(frame{Type: tPong})
		case tPong:
		default:
			if err := other(f); err != nil {
				c.close(err)
				return err
			}
		}
	}
}

func (c *conn) close(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	if err == nil {
		err = errClosed
	}
	c.err = err
	close(c.done)
	_ = c.nc.Close()
}

func (c *conn) closedErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

var errClosed = errors.New("connection closed")

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// peerFP is the SHA-256 fingerprint of the certificate of the machine at
// the other end of a TLS connection.
func (c *conn) peerFP() (string, bool) {
	tc, ok := c.nc.(*tls.Conn)
	if !ok {
		return "", false
	}
	cs := tc.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return "", false
	}
	return fingerprint(cs.PeerCertificates[0].Raw), true
}
