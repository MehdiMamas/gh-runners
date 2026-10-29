package proc

import "testing"

func TestGroupNilSafe(t *testing.T) {
	var g *Group
	g.Add(nil)
	g.Close()
	g = NewGroup()
	g.Add(nil)
	g.Close()
	g.Close()
}
