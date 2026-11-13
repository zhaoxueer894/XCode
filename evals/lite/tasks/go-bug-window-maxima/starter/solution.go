package solution

import "errors"

// ErrWidth is returned for a non-positive width.
var ErrWidth = errors.New("width must be positive")

// WindowMaxima returns the maximum of every contiguous window of the given
// width, one value per window.
func WindowMaxima(values []int, width int) ([]int, error) {
	if width <= 0 {
		return nil, ErrWidth
	}
	if width > len(values) {
		return []int{}, nil
	}
	result := make([]int, 0, len(values)-width+1)
	for start := 0; start < len(values)-width; start++ {
		best := values[start]
		for _, value := range values[start : start+width] {
			if value > best {
				best = value
			}
		}
		result = append(result, best)
	}
	return result, nil
}
