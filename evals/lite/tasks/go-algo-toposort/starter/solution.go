package solution

import "errors"

// ErrCycle is returned when the graph contains a cycle.
var ErrCycle = errors.New("graph contains a cycle")

// TopoOrder orders every node so each edge points forward, breaking ties by the
// alphabetically smaller name. It returns ErrCycle for a cyclic graph and an
// error naming the node when an edge references an unknown node.
func TopoOrder(nodes []string, edges [][2]string) ([]string, error) {
	panic("not implemented")
}
