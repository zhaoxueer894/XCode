package solution

import "testing"

func TestSearchNotRotated(t *testing.T) {
	if got := Search([]int{1, 2, 3, 4, 5}, 3); got != 2 {
		t.Fatalf("got %d", got)
	}
}

func TestSearchPivot(t *testing.T) {
	values := []int{6, 7, 1, 2, 3}
	if got := Search(values, 6); got != 0 {
		t.Fatalf("got %d", got)
	}
	if got := Search(values, 3); got != 4 {
		t.Fatalf("got %d", got)
	}
}

func TestSearchTwoElements(t *testing.T) {
	if got := Search([]int{2, 1}, 1); got != 1 {
		t.Fatalf("got %d", got)
	}
}

func TestSearchEmpty(t *testing.T) {
	if got := Search(nil, 5); got != -1 {
		t.Fatalf("got %d", got)
	}
}
