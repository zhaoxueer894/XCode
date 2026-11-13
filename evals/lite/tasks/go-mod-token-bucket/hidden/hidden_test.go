package solution

import (
	"errors"
	"testing"
)

func TestBucketRefillCapped(t *testing.T) {
	now := 0.0
	bucket, _ := NewTokenBucket(10, 2, func() float64 { return now })
	bucket.Allow(2)
	now = 100.0
	if got := bucket.Tokens(); got != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestBucketRefusedRequestConsumesNothing(t *testing.T) {
	bucket, _ := NewTokenBucket(1, 1, func() float64 { return 0 })
	bucket.Allow(1)
	if ok, _ := bucket.Allow(1); ok {
		t.Fatal("unexpected allowance")
	}
	if got := bucket.Tokens(); got != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestBucketInvalidParameters(t *testing.T) {
	if _, err := NewTokenBucket(0, 1, func() float64 { return 0 }); !errors.Is(err, ErrParameter) {
		t.Fatal("expected ErrParameter for rate 0")
	}
	if _, err := NewTokenBucket(1, 0, func() float64 { return 0 }); !errors.Is(err, ErrParameter) {
		t.Fatal("expected ErrParameter for capacity 0")
	}
	bucket, _ := NewTokenBucket(1, 1, func() float64 { return 0 })
	if _, err := bucket.Allow(0); !errors.Is(err, ErrParameter) {
		t.Fatal("expected ErrParameter for count 0")
	}
}
