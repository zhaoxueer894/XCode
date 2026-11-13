package solution

import "sort"

// Merge combines overlapping or touching closed intervals and returns them
// sorted by start. The input slices must not be modified.
func Merge(intervals [][]int) [][]int {
	if len(intervals) == 0 {
		return nil
	}
	ordered := make([][]int, len(intervals))
	for index, interval := range intervals {
		ordered[index] = []int{interval[0], interval[1]}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i][0] != ordered[j][0] {
			return ordered[i][0] < ordered[j][0]
		}
		return ordered[i][1] < ordered[j][1]
	})
	merged := [][]int{ordered[0]}
	for _, interval := range ordered[1:] {
		last := merged[len(merged)-1]
		if interval[0] <= last[1] {
			if interval[1] > last[1] {
				last[1] = interval[1]
			}
			continue
		}
		merged = append(merged, interval)
	}
	return merged
}
