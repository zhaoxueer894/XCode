package solution

import "testing"

func TestOverlapsSharedRange(t *testing.T) {
	if !Overlaps(1, 5, 3, 8) {
		t.Fatal("ranges sharing 3..5 must overlap")
	}
}

func TestOverlapsTouchingEndpoints(t *testing.T) {
	if !Overlaps(1, 3, 3, 5) {
		t.Fatal("closed ranges sharing the endpoint 3 must overlap")
	}
}
