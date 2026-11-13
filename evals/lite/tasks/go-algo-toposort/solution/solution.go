package solution

import (
	"errors"
	"fmt"
	"sort"
)

// ErrCycle is returned when the graph contains a cycle.
var ErrCycle = errors.New("graph contains a cycle")

// TopoOrder orders every node so each edge points forward, breaking ties by the
// alphabetically smaller name. It returns ErrCycle for a cyclic graph and an
// error naming the node when an edge references an unknown node.
func TopoOrder(nodes []string, edges [][2]string) ([]string, error) {
	incoming := make(map[string]int, len(nodes))
	outgoing := make(map[string][]string, len(nodes))
	for _, node := range nodes {
		incoming[node] = 0
		outgoing[node] = nil
	}
	for _, edge := range edges {
		source, target := edge[0], edge[1]
		if _, ok := incoming[source]; !ok {
			return nil, fmt.Errorf("unknown node: %s", source)
		}
		if _, ok := incoming[target]; !ok {
			return nil, fmt.Errorf("unknown node: %s", target)
		}
		outgoing[source] = append(outgoing[source], target)
		incoming[target]++
	}
	ready := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if incoming[node] == 0 {
			ready = append(ready, node)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(nodes))
	for len(ready) > 0 {
		node := ready[0]
		ready = ready[1:]
		order = append(order, node)
		for _, target := range outgoing[node] {
			incoming[target]--
			if incoming[target] == 0 {
				ready = append(ready, target)
			}
		}
		sort.Strings(ready)
	}
	if len(order) != len(nodes) {
		return nil, ErrCycle
	}
	return order, nil
}
