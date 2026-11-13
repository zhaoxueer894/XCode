package solution

import (
	"errors"
	"reflect"
	"testing"
)

func TestWindowMaximaExactWidth(t *testing.T) {
	got, err := WindowMaxima([]int{4, 2, 7}, 3)
	if err != nil || !reflect.DeepEqual(got, []int{7}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestWindowMaximaWidthTooLarge(t *testing.T) {
	got, err := WindowMaxima([]int{1, 2}, 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestWindowMaximaInvalidWidth(t *testing.T) {
	if _, err := WindowMaxima([]int{1}, 0); !errors.Is(err, ErrWidth) {
		t.Fatal("expected ErrWidth")
	}
}

func TestWindowMaximaWidthOne(t *testing.T) {
	got, err := WindowMaxima([]int{3, 1, 2}, 1)
	if err != nil || !reflect.DeepEqual(got, []int{3, 1, 2}) {
		t.Fatalf("got %v, %v", got, err)
	}
}
