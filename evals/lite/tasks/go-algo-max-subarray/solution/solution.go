package solution

import "errors"

// ErrEmpty is returned when no values are supplied.
var ErrEmpty = errors.New("values must not be empty")

// MaxSubarraySum returns the largest sum of a non-empty contiguous subarray.
func MaxSubarraySum(values []int) (int, error) {
	if len(values) == 0 {
		return 0, ErrEmpty
	}
	best, running := values[0], values[0]
	for _, value := range values[1:] {
		if running < 0 {
			running = value
		} else {
			running += value
		}
		if running > best {
			best = running
		}
	}
	return best, nil
}
