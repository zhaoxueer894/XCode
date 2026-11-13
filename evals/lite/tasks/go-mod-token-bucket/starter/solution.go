package solution

import "errors"

// ErrParameter is returned for a non-positive rate, capacity or request count.
var ErrParameter = errors.New("invalid token bucket parameter")

// TokenBucket refills at rate tokens per second, up to capacity, using clock.
type TokenBucket struct {
}

// NewTokenBucket builds a bucket that starts full.
func NewTokenBucket(rate float64, capacity int, clock func() float64) (*TokenBucket, error) {
	panic("not implemented")
}

// Allow removes count tokens and reports whether they were available.
func (b *TokenBucket) Allow(count int) (bool, error) {
	panic("not implemented")
}

// Tokens reports the current token level.
func (b *TokenBucket) Tokens() float64 {
	panic("not implemented")
}
