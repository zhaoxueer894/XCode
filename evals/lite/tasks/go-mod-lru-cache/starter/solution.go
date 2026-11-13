package solution

import "errors"

// ErrCapacity is returned for a non-positive capacity.
var ErrCapacity = errors.New("capacity must be positive")

// LRUCache is a fixed-capacity cache that evicts the least recently used entry.
type LRUCache struct {
}

// NewLRUCache builds a cache holding at most capacity entries.
func NewLRUCache(capacity int) (*LRUCache, error) {
	panic("not implemented")
}

// Get returns the stored value and whether the key was present.
func (c *LRUCache) Get(key string) (int, bool) {
	panic("not implemented")
}

// Put stores a value, marking the key most recently used.
func (c *LRUCache) Put(key string, value int) {
	panic("not implemented")
}

// Len reports how many entries are currently stored.
func (c *LRUCache) Len() int {
	panic("not implemented")
}
