package solution

import (
	"reflect"
	"testing"
)

func TestMergeOverlapping(t *testing.T) {
	got := Merge([][]int{{1, 3}, {2, 6}, {8, 10}})
	want := [][]int{{1, 6}, {8, 10}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMergeKeepsInput(t *testing.T) {
	input := [][]int{{5, 6}, {1, 2}}
	Merge(input)
	if !reflect.DeepEqual(input, [][]int{{5, 6}, {1, 2}}) {
		t.Fatalf("input was modified: %v", input)
	}
}
