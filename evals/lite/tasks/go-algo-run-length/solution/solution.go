package solution

// Pair is one run of equal characters.
type Pair struct {
	Rune  rune
	Count int
}

// Encode folds consecutive equal characters into [rune, count] pairs, in order.
func Encode(text string) []Pair {
	var result []Pair
	for _, character := range text {
		if n := len(result); n > 0 && result[n-1].Rune == character {
			result[n-1].Count++
			continue
		}
		result = append(result, Pair{Rune: character, Count: 1})
	}
	return result
}

// Decode rebuilds the original string from run-length pairs.
func Decode(pairs []Pair) string {
	var builder []rune
	for _, pair := range pairs {
		for index := 0; index < pair.Count; index++ {
			builder = append(builder, pair.Rune)
		}
	}
	return string(builder)
}
