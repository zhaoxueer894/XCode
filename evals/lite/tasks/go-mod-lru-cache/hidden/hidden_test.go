package solution

import "testing"

func TestLRUEvictsLeastRecentlyUsed(t *testing.T) {
	cache, _ := NewLRUCache(2)
	cache.Put("a", 1)
	cache.Put("b", 2)
	cache.Get("a")
	cache.Put("c", 3)
	if _, ok := cache.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if value, ok := cache.Get("a"); !ok || value != 1 {
		t.Fatalf("got %d, %v", value, ok)
	}
	if value, ok := cache.Get("c"); !ok || value != 3 {
		t.Fatalf("got %d, %v", value, ok)
	}
}

func TestLRUOverwriteKeepsSize(t *testing.T) {
	cache, _ := NewLRUCache(1)
	cache.Put("a", 1)
	cache.Put("a", 2)
	if cache.Len() != 1 {
		t.Fatalf("got %d", cache.Len())
	}
	if value, _ := cache.Get("a"); value != 2 {
		t.Fatalf("got %d", value)
	}
}

func TestLRUMissingKey(t *testing.T) {
	cache, _ := NewLRUCache(1)
	if _, ok := cache.Get("missing"); ok {
		t.Fatal("unexpected hit")
	}
}
