package solution

import "testing"

func TestMaxSubarrayMixed(t *testing.T) {
	got, err := MaxSubarraySum([]int{-2, 1, -3, 4, -1, 2, 1, -5, 4})
	if err != nil || got != 6 {
		t.Fatalf("got %d, %v", got, err)
	}
}

func TestMaxSubarrayAllNegative(t *testing.T) {
	got, err := MaxSubarraySum([]int{-3, -1, -2})
	if err != nil || got != -1 {
		t.Fatalf("got %d, %v", got, err)
	}
}
