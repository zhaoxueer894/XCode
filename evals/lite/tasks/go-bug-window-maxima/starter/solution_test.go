package solution

import (
	"reflect"
	"testing"
)

func TestWindowMaximaBasic(t *testing.T) {
	got, err := WindowMaxima([]int{1, 2, 3, 4}, 2)
	if err != nil || !reflect.DeepEqual(got, []int{2, 3, 4}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestWindowMaximaLastWindow(t *testing.T) {
	got, err := WindowMaxima([]int{5, 1, 1, 9}, 3)
	if err != nil || !reflect.DeepEqual(got, []int{5, 9}) {
		t.Fatalf("got %v, %v", got, err)
	}
}
