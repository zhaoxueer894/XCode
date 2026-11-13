package solution

import "testing"

func TestSearchRotated(t *testing.T) {
	if got := Search([]int{4, 5, 6, 7, 0, 1, 2}, 0); got != 4 {
		t.Fatalf("got %d", got)
	}
}

func TestSearchMissing(t *testing.T) {
	if got := Search([]int{4, 5, 6, 7, 0, 1, 2}, 3); got != -1 {
		t.Fatalf("got %d", got)
	}
}
