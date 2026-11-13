package solution

import "testing"

func TestMeanWholeResult(t *testing.T) {
	got, err := Mean([]int{2, 4})
	if err != nil || got != 3 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestMeanKeepsFraction(t *testing.T) {
	got, err := Mean([]int{1, 2})
	if err != nil || got != 1.5 {
		t.Fatalf("got %v, %v", got, err)
	}
}
