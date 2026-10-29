//go:build !windows

package proc

// The process-group kill in Terminate already covers other systems.
type group struct{}

func newGroup() (*group, error) { return nil, nil }

func (*group) add(int) error { return nil }

func (*group) close() error { return nil }
