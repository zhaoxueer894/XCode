package solution

import (
	"container/list"
	"errors"
)

// ErrCapacity is returned for a non-positive capacity.
var ErrCapacity = errors.New("capacity must be positive")

type lruEntry struct {
	key   string
	value int
}

// LRUCache is a fixed-capacity cache that evicts the least recently used entry.
type LRUCache struct {
	capacity int
	order    *list.List
	entries  map[string]*list.Element
}

// NewLRUCache builds a cache holding at most capacity entries.
func NewLRUCache(capacity int) (*LRUCache, error) {
	if capacity < 1 {
		return nil, ErrCapacity
	}
	return &LRUCache{capacity: capacity, order: list.New(), entries: map[string]*list.Element{}}, nil
}

// Get returns the stored value and whether the key was present.
func (c *LRUCache) Get(key string) (int, bool) {
	element, ok := c.entries[key]
	if !ok {
		return 0, false
	}
	c.order.MoveToBack(element)
	return element.Value.(*lruEntry).value, true
}

// Put stores a value, marking the key most recently used.
func (c *LRUCache) Put(key string, value int) {
	if element, ok := c.entries[key]; ok {
		element.Value.(*lruEntry).value = value
		c.order.MoveToBack(element)
		return
	}
	c.entries[key] = c.order.PushBack(&lruEntry{key: key, value: value})
	for c.order.Len() > c.capacity {
		oldest := c.order.Front()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*lruEntry).key)
	}
}

// Len reports how many entries are currently stored.
func (c *LRUCache) Len() int {
	return c.order.Len()
}
