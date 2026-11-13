package solution

import "testing"

func TestBucketBurst(t *testing.T) {
	bucket, err := NewTokenBucket(1, 3, func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := bucket.Allow(3); !ok {
		t.Fatal("expected the initial burst to be allowed")
	}
	if ok, _ := bucket.Allow(1); ok {
		t.Fatal("expected the bucket to be empty")
	}
}

func TestBucketRefill(t *testing.T) {
	now := 0.0
	bucket, _ := NewTokenBucket(2, 2, func() float64 { return now })
	bucket.Allow(2)
	now = 1.0
	if ok, _ := bucket.Allow(2); !ok {
		t.Fatal("expected refilled tokens")
	}
}
