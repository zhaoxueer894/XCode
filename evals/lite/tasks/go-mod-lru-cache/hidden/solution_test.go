package solution

import "testing"

func TestLRUPutGet(t *testing.T) {
	cache, err := NewLRUCache(2)
	if err != nil {
		t.Fatal(err)
	}
	cache.Put("a", 1)
	if value, ok := cache.Get("a"); !ok || value != 1 {
		t.Fatalf("got %d, %v", value, ok)
	}
}

func TestLRURejectsZeroCapacity(t *testing.T) {
	if _, err := NewLRUCache(0); err == nil {
		t.Fatal("expected an error for capacity 0")
	}
}
