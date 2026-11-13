package solution

import "errors"

// ErrParameter is returned for a non-positive rate, capacity or request count.
var ErrParameter = errors.New("invalid token bucket parameter")

// TokenBucket refills at rate tokens per second, up to capacity, using clock.
type TokenBucket struct {
	rate     float64
	capacity float64
	clock    func() float64
	tokens   float64
	updated  float64
}

// NewTokenBucket builds a bucket that starts full.
func NewTokenBucket(rate float64, capacity int, clock func() float64) (*TokenBucket, error) {
	if rate <= 0 || capacity < 1 {
		return nil, ErrParameter
	}
	return &TokenBucket{
		rate:     rate,
		capacity: float64(capacity),
		clock:    clock,
		tokens:   float64(capacity),
		updated:  clock(),
	}, nil
}

// Allow removes count tokens and reports whether they were available.
func (b *TokenBucket) Allow(count int) (bool, error) {
	if count < 1 {
		return false, ErrParameter
	}
	b.refill()
	if b.tokens >= float64(count) {
		b.tokens -= float64(count)
		return true, nil
	}
	return false, nil
}

func (b *TokenBucket) refill() {
	now := b.clock()
	if now > b.updated {
		b.tokens += (now - b.updated) * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.updated = now
	}
}

// Tokens reports the current token level.
func (b *TokenBucket) Tokens() float64 {
	b.refill()
	return b.tokens
}
