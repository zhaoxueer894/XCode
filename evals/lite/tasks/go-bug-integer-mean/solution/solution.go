package solution

import "errors"

// ErrNoValues is returned for an empty slice.
var ErrNoValues = errors.New("values must not be empty")

// Mean returns the arithmetic mean of the values.
func Mean(values []int) (float64, error) {
	if len(values) == 0 {
		return 0, ErrNoValues
	}
	sum := 0
	for _, value := range values {
		sum += value
	}
	return float64(sum) / float64(len(values)), nil
}
