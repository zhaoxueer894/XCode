package solution

import (
	"errors"
	"testing"
)

func TestMeanEmptyFails(t *testing.T) {
	if _, err := Mean(nil); !errors.Is(err, ErrNoValues) {
		t.Fatalf("got %v", err)
	}
}

func TestMeanSingleValue(t *testing.T) {
	got, err := Mean([]int{7})
	if err != nil || got != 7 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestMeanNegativeValues(t *testing.T) {
	got, err := Mean([]int{-1, -2, -3})
	if err != nil || got != -2 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestMeanRoundingNotTruncated(t *testing.T) {
	got, err := Mean([]int{10, 10, 11})
	if err != nil || got != 31.0/3.0 {
		t.Fatalf("got %v, %v", got, err)
	}
}
