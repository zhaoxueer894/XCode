package solution

import (
	"errors"
	"testing"
)

func TestTopoCycle(t *testing.T) {
	if _, err := TopoOrder([]string{"a", "b"}, [][2]string{{"a", "b"}, {"b", "a"}}); !errors.Is(err, ErrCycle) {
		t.Fatalf("got %v, want ErrCycle", err)
	}
}

func TestTopoSelfLoop(t *testing.T) {
	if _, err := TopoOrder([]string{"a"}, [][2]string{{"a", "a"}}); !errors.Is(err, ErrCycle) {
		t.Fatalf("got %v, want ErrCycle", err)
	}
}

func TestTopoUnknownNode(t *testing.T) {
	if _, err := TopoOrder([]string{"a"}, [][2]string{{"a", "z"}}); err == nil {
		t.Fatal("expected an error for an unknown node")
	}
}

func TestTopoDiamondOrder(t *testing.T) {
	order, err := TopoOrder([]string{"d", "b", "c", "a"}, [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}})
	if err != nil {
		t.Fatal(err)
	}
	if order[0] != "a" || order[len(order)-1] != "d" {
		t.Fatalf("got %v", order)
	}
	position := map[string]int{}
	for index, node := range order {
		position[node] = index
	}
	if !(position["b"] < position["d"] && position["c"] < position["d"]) {
		t.Fatalf("got %v", order)
	}
}

func TestTopoEmpty(t *testing.T) {
	order, err := TopoOrder(nil, nil)
	if err != nil || len(order) != 0 {
		t.Fatalf("got %v, %v", order, err)
	}
}
