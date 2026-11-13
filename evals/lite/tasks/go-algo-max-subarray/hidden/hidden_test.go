package solution

import (
	"errors"
	"testing"
)

func TestMaxSubarrayEmptyFails(t *testing.T) {
	if _, err := MaxSubarraySum(nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("got %v, want ErrEmpty", err)
	}
}

func TestMaxSubarrayAllPositive(t *testing.T) {
	got, err := MaxSubarraySum([]int{1, 2, 3, 4})
	if err != nil || got != 10 {
		t.Fatalf("got %d, %v", got, err)
	}
}

func TestMaxSubarraySingle(t *testing.T) {
	got, err := MaxSubarraySum([]int{-7})
	if err != nil || got != -7 {
		t.Fatalf("got %d, %v", got, err)
	}
}

func TestMaxSubarrayLong(t *testing.T) {
	values := make([]int, 0, 102)
	values = append(values, 10)
	for index := 0; index < 50; index++ {
		values = append(values, -1, 1)
	}
	values = append(values, 10)
	got, err := MaxSubarraySum(values)
	if err != nil || got != 20 {
		t.Fatalf("got %d, %v", got, err)
	}
}
