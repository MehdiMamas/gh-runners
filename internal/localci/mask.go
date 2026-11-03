package localci

import (
	"io"
	"sort"
	"strings"
	"sync"
)

// minMask is the shortest secret value that is masked: shorter strings would
// blank out ordinary text.
const minMask = 4

// maskValues keeps the values worth masking, longest first.
func maskValues(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		if len(v) >= minMask && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, k int) bool { return len(out[i]) > len(out[k]) })
	return out
}

// MaskString replaces each secret value in s with ***.
func MaskString(s string, values []string) string {
	for _, v := range maskValues(values) {
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

// maskWriter hides secret values in what is written through it, also when a
// value is split across two writes: it holds back the last maxLen-1 bytes
// until the next write or Flush.
type maskWriter struct {
	mu   sync.Mutex
	w    io.Writer
	vals []string
	keep int
	buf  []byte
}

func newMaskWriter(w io.Writer, values []string) *maskWriter {
	m := &maskWriter{w: w, vals: maskValues(values)}
	for _, v := range m.vals {
		if len(v)-1 > m.keep {
			m.keep = len(v) - 1
		}
	}
	return m
}

func (m *maskWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buf = append(m.buf, p...)
	b := []byte(MaskString(string(m.buf), m.vals))
	cut := len(b) - m.keep
	if cut < 0 {
		cut = 0
	}
	m.buf = append(m.buf[:0], b[cut:]...)
	if _, err := m.w.Write(b[:cut]); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush writes what is held back.
func (m *maskWriter) Flush() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.buf) == 0 {
		return nil
	}
	b := []byte(MaskString(string(m.buf), m.vals))
	m.buf = m.buf[:0]
	_, err := m.w.Write(b)
	return err
}
