package solution

import (
	"reflect"
	"testing"
)

func TestMergeTouching(t *testing.T) {
	got := Merge([][]int{{1, 3}, {3, 5}})
	if !reflect.DeepEqual(got, [][]int{{1, 5}}) {
		t.Fatalf("got %v", got)
	}
}

func TestMergeContained(t *testing.T) {
	got := Merge([][]int{{1, 10}, {3, 4}})
	if !reflect.DeepEqual(got, [][]int{{1, 10}}) {
		t.Fatalf("got %v", got)
	}
}

func TestMergeEmpty(t *testing.T) {
	if got := Merge(nil); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestMergeDuplicates(t *testing.T) {
	got := Merge([][]int{{2, 2}, {2, 2}})
	if !reflect.DeepEqual(got, [][]int{{2, 2}}) {
		t.Fatalf("got %v", got)
	}
}

func TestMergeDisjointUnchanged(t *testing.T) {
	got := Merge([][]int{{4, 5}, {1, 2}})
	if !reflect.DeepEqual(got, [][]int{{1, 2}, {4, 5}}) {
		t.Fatalf("got %v", got)
	}
}
