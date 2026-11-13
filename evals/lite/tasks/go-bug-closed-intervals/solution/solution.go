package solution

// Overlaps reports whether two closed integer intervals [aStart, aEnd] and
// [bStart, bEnd] share at least one integer.
func Overlaps(aStart, aEnd, bStart, bEnd int) bool {
	return aStart <= bEnd && bStart <= aEnd
}
