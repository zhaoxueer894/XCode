package solution

import (
	"reflect"
	"testing"
)

func TestTopoChain(t *testing.T) {
	got, err := TopoOrder([]string{"a", "b", "c"}, [][2]string{{"a", "b"}, {"b", "c"}})
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestTopoTieBreak(t *testing.T) {
	got, err := TopoOrder([]string{"b", "a"}, nil)
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}
