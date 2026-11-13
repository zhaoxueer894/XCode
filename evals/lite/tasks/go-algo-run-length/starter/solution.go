package solution

// Pair is one run of equal characters.
type Pair struct {
	Rune  rune
	Count int
}

// Encode folds consecutive equal characters into [rune, count] pairs, in order.
func Encode(text string) []Pair {
	panic("not implemented")
}

// Decode rebuilds the original string from run-length pairs.
func Decode(pairs []Pair) string {
	panic("not implemented")
}
