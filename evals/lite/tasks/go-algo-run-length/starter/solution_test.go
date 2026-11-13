package solution

import "testing"

func TestEncodeRuns(t *testing.T) {
	encoded := Encode("aaabbc")
	want := []Pair{{'a', 3}, {'b', 2}, {'c', 1}}
	if len(encoded) != len(want) {
		t.Fatalf("got %v, want %v", encoded, want)
	}
	for index := range want {
		if encoded[index] != want[index] {
			t.Fatalf("got %v, want %v", encoded, want)
		}
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	if got := Decode(Encode("aabbb")); got != "aabbb" {
		t.Fatalf("got %q", got)
	}
}
