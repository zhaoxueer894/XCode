package solution

import "testing"

func TestEncodeEmpty(t *testing.T) {
	if got := Encode(""); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestEncodeNonAscii(t *testing.T) {
	encoded := Encode("\u4f60\u4f60\u597d")
	want := []Pair{{'\u4f60', 2}, {'\u597d', 1}}
	if len(encoded) != len(want) || encoded[0] != want[0] || encoded[1] != want[1] {
		t.Fatalf("got %v, want %v", encoded, want)
	}
}

func TestDecodeCountAboveNine(t *testing.T) {
	if got := Decode([]Pair{{Rune: 'a', Count: 12}}); got != "aaaaaaaaaaaa" {
		t.Fatalf("got %q", got)
	}
}

func TestRoundTripLong(t *testing.T) {
	text := "abcdabcdabcdzzzzzzzzzz"
	if got := Decode(Encode(text)); got != text {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeEmpty(t *testing.T) {
	if got := Decode(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}
