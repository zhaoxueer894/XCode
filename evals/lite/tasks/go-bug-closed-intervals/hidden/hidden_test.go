package solution

import "testing"

func TestOverlapsSeparated(t *testing.T) {
	if Overlaps(1, 2, 3, 4) {
		t.Fatal("separated ranges must not overlap")
	}
}

func TestOverlapsContained(t *testing.T) {
	if !Overlaps(1, 10, 4, 6) {
		t.Fatal("a contained range must overlap")
	}
}

func TestOverlapsSinglePoints(t *testing.T) {
	if !Overlaps(2, 2, 2, 2) {
		t.Fatal("equal single points must overlap")
	}
}

func TestOverlapsReversedOrder(t *testing.T) {
	if !Overlaps(7, 9, 1, 7) {
		t.Fatal("closed ranges sharing 7 must overlap regardless of order")
	}
}
