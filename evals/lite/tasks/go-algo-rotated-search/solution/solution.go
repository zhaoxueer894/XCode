package solution

// Search returns the index of target in a sorted, rotated slice of distinct
// integers, or -1 when the target is absent.
func Search(values []int, target int) int {
	low, high := 0, len(values)-1
	for low <= high {
		middle := (low + high) / 2
		if values[middle] == target {
			return middle
		}
		if values[low] <= values[middle] {
			if values[low] <= target && target < values[middle] {
				high = middle - 1
			} else {
				low = middle + 1
			}
			continue
		}
		if values[middle] < target && target <= values[high] {
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return -1
}
