"""Go lite tasks: single-package algorithms, small modules and bug fixes.

Each task is a standalone Go module (no external dependencies), so `go test`
compiles it in one to three seconds without network access. The visible suite
is overwritten by the hidden overlay at grade time, exactly as for Python.
"""

from __future__ import annotations

ALGO = "algo"
MODULE = "module"
BUGFIX = "bugfix"

GO_MOD = "module lite/{name}\n\ngo 1.21\n"


def go_task(id, kind, prompt, starter_source, solution_source, visible, hidden, acceptance, tags=()):
    """Assemble one Go task's file trees and metadata."""
    module = GO_MOD.format(name=id)
    return {
        "id": id,
        "language": "go",
        "kind": kind,
        "prompt": prompt,
        "acceptance": acceptance,
        "tags": list(tags),
        "starter": {
            "go.mod": module,
            "solution.go": starter_source,
            "solution_test.go": visible,
        },
        "solution": {"solution.go": solution_source},
        "hidden": {"solution_test.go": visible, "hidden_test.go": hidden},
        "verify_commands": [["go", "test", "./..."]],
        "hidden_command": [["go", "test", "-count=1", "./..."]],
    }


# --------------------------------------------------------------------------- #
# algorithms
# --------------------------------------------------------------------------- #

RUN_LENGTH_STUB = '''package solution

// Pair is one run of equal characters.
type Pair struct {
\tRune  rune
\tCount int
}

// Encode folds consecutive equal characters into [rune, count] pairs, in order.
func Encode(text string) []Pair {
\tpanic("not implemented")
}

// Decode rebuilds the original string from run-length pairs.
func Decode(pairs []Pair) string {
\tpanic("not implemented")
}
'''

RUN_LENGTH_SOLUTION = '''package solution

// Pair is one run of equal characters.
type Pair struct {
\tRune  rune
\tCount int
}

// Encode folds consecutive equal characters into [rune, count] pairs, in order.
func Encode(text string) []Pair {
\tvar result []Pair
\tfor _, character := range text {
\t\tif n := len(result); n > 0 && result[n-1].Rune == character {
\t\t\tresult[n-1].Count++
\t\t\tcontinue
\t\t}
\t\tresult = append(result, Pair{Rune: character, Count: 1})
\t}
\treturn result
}

// Decode rebuilds the original string from run-length pairs.
func Decode(pairs []Pair) string {
\tvar builder []rune
\tfor _, pair := range pairs {
\t\tfor index := 0; index < pair.Count; index++ {
\t\t\tbuilder = append(builder, pair.Rune)
\t\t}
\t}
\treturn string(builder)
}
'''

RUN_LENGTH_VISIBLE = '''package solution

import "testing"

func TestEncodeRuns(t *testing.T) {
\tencoded := Encode("aaabbc")
\twant := []Pair{{'a', 3}, {'b', 2}, {'c', 1}}
\tif len(encoded) != len(want) {
\t\tt.Fatalf("got %v, want %v", encoded, want)
\t}
\tfor index := range want {
\t\tif encoded[index] != want[index] {
\t\t\tt.Fatalf("got %v, want %v", encoded, want)
\t\t}
\t}
}

func TestDecodeRoundTrip(t *testing.T) {
\tif got := Decode(Encode("aabbb")); got != "aabbb" {
\t\tt.Fatalf("got %q", got)
\t}
}
'''

RUN_LENGTH_HIDDEN = '''package solution

import "testing"

func TestEncodeEmpty(t *testing.T) {
\tif got := Encode(""); len(got) != 0 {
\t\tt.Fatalf("got %v, want empty", got)
\t}
}

func TestEncodeNonAscii(t *testing.T) {
\tencoded := Encode("\\u4f60\\u4f60\\u597d")
\twant := []Pair{{'\\u4f60', 2}, {'\\u597d', 1}}
\tif len(encoded) != len(want) || encoded[0] != want[0] || encoded[1] != want[1] {
\t\tt.Fatalf("got %v, want %v", encoded, want)
\t}
}

func TestDecodeCountAboveNine(t *testing.T) {
\tif got := Decode([]Pair{{Rune: 'a', Count: 12}}); got != "aaaaaaaaaaaa" {
\t\tt.Fatalf("got %q", got)
\t}
}

func TestRoundTripLong(t *testing.T) {
\ttext := "abcdabcdabcdzzzzzzzzzz"
\tif got := Decode(Encode(text)); got != text {
\t\tt.Fatalf("got %q", got)
\t}
}

func TestDecodeEmpty(t *testing.T) {
\tif got := Decode(nil); got != "" {
\t\tt.Fatalf("got %q", got)
\t}
}
'''

MERGE_STUB = '''package solution

// Merge combines overlapping or touching closed intervals and returns them
// sorted by start. The input slices must not be modified.
func Merge(intervals [][]int) [][]int {
\tpanic("not implemented")
}
'''

MERGE_SOLUTION = '''package solution

import "sort"

// Merge combines overlapping or touching closed intervals and returns them
// sorted by start. The input slices must not be modified.
func Merge(intervals [][]int) [][]int {
\tif len(intervals) == 0 {
\t\treturn nil
\t}
\tordered := make([][]int, len(intervals))
\tfor index, interval := range intervals {
\t\tordered[index] = []int{interval[0], interval[1]}
\t}
\tsort.Slice(ordered, func(i, j int) bool {
\t\tif ordered[i][0] != ordered[j][0] {
\t\t\treturn ordered[i][0] < ordered[j][0]
\t\t}
\t\treturn ordered[i][1] < ordered[j][1]
\t})
\tmerged := [][]int{ordered[0]}
\tfor _, interval := range ordered[1:] {
\t\tlast := merged[len(merged)-1]
\t\tif interval[0] <= last[1] {
\t\t\tif interval[1] > last[1] {
\t\t\t\tlast[1] = interval[1]
\t\t\t}
\t\t\tcontinue
\t\t}
\t\tmerged = append(merged, interval)
\t}
\treturn merged
}
'''

MERGE_VISIBLE = '''package solution

import (
\t"reflect"
\t"testing"
)

func TestMergeOverlapping(t *testing.T) {
\tgot := Merge([][]int{{1, 3}, {2, 6}, {8, 10}})
\twant := [][]int{{1, 6}, {8, 10}}
\tif !reflect.DeepEqual(got, want) {
\t\tt.Fatalf("got %v, want %v", got, want)
\t}
}

func TestMergeKeepsInput(t *testing.T) {
\tinput := [][]int{{5, 6}, {1, 2}}
\tMerge(input)
\tif !reflect.DeepEqual(input, [][]int{{5, 6}, {1, 2}}) {
\t\tt.Fatalf("input was modified: %v", input)
\t}
}
'''

MERGE_HIDDEN = '''package solution

import (
\t"reflect"
\t"testing"
)

func TestMergeTouching(t *testing.T) {
\tgot := Merge([][]int{{1, 3}, {3, 5}})
\tif !reflect.DeepEqual(got, [][]int{{1, 5}}) {
\t\tt.Fatalf("got %v", got)
\t}
}

func TestMergeContained(t *testing.T) {
\tgot := Merge([][]int{{1, 10}, {3, 4}})
\tif !reflect.DeepEqual(got, [][]int{{1, 10}}) {
\t\tt.Fatalf("got %v", got)
\t}
}

func TestMergeEmpty(t *testing.T) {
\tif got := Merge(nil); len(got) != 0 {
\t\tt.Fatalf("got %v", got)
\t}
}

func TestMergeDuplicates(t *testing.T) {
\tgot := Merge([][]int{{2, 2}, {2, 2}})
\tif !reflect.DeepEqual(got, [][]int{{2, 2}}) {
\t\tt.Fatalf("got %v", got)
\t}
}

func TestMergeDisjointUnchanged(t *testing.T) {
\tgot := Merge([][]int{{4, 5}, {1, 2}})
\tif !reflect.DeepEqual(got, [][]int{{1, 2}, {4, 5}}) {
\t\tt.Fatalf("got %v", got)
\t}
}
'''

MAX_SUBARRAY_STUB = '''package solution

import "errors"

// ErrEmpty is returned when no values are supplied.
var ErrEmpty = errors.New("values must not be empty")

// MaxSubarraySum returns the largest sum of a non-empty contiguous subarray.
func MaxSubarraySum(values []int) (int, error) {
\tpanic("not implemented")
}
'''

MAX_SUBARRAY_SOLUTION = '''package solution

import "errors"

// ErrEmpty is returned when no values are supplied.
var ErrEmpty = errors.New("values must not be empty")

// MaxSubarraySum returns the largest sum of a non-empty contiguous subarray.
func MaxSubarraySum(values []int) (int, error) {
\tif len(values) == 0 {
\t\treturn 0, ErrEmpty
\t}
\tbest, running := values[0], values[0]
\tfor _, value := range values[1:] {
\t\tif running < 0 {
\t\t\trunning = value
\t\t} else {
\t\t\trunning += value
\t\t}
\t\tif running > best {
\t\t\tbest = running
\t\t}
\t}
\treturn best, nil
}
'''

MAX_SUBARRAY_VISIBLE = '''package solution

import "testing"

func TestMaxSubarrayMixed(t *testing.T) {
\tgot, err := MaxSubarraySum([]int{-2, 1, -3, 4, -1, 2, 1, -5, 4})
\tif err != nil || got != 6 {
\t\tt.Fatalf("got %d, %v", got, err)
\t}
}

func TestMaxSubarrayAllNegative(t *testing.T) {
\tgot, err := MaxSubarraySum([]int{-3, -1, -2})
\tif err != nil || got != -1 {
\t\tt.Fatalf("got %d, %v", got, err)
\t}
}
'''

MAX_SUBARRAY_HIDDEN = '''package solution

import (
\t"errors"
\t"testing"
)

func TestMaxSubarrayEmptyFails(t *testing.T) {
\tif _, err := MaxSubarraySum(nil); !errors.Is(err, ErrEmpty) {
\t\tt.Fatalf("got %v, want ErrEmpty", err)
\t}
}

func TestMaxSubarrayAllPositive(t *testing.T) {
\tgot, err := MaxSubarraySum([]int{1, 2, 3, 4})
\tif err != nil || got != 10 {
\t\tt.Fatalf("got %d, %v", got, err)
\t}
}

func TestMaxSubarraySingle(t *testing.T) {
\tgot, err := MaxSubarraySum([]int{-7})
\tif err != nil || got != -7 {
\t\tt.Fatalf("got %d, %v", got, err)
\t}
}

func TestMaxSubarrayLong(t *testing.T) {
\tvalues := make([]int, 0, 102)
\tvalues = append(values, 10)
\tfor index := 0; index < 50; index++ {
\t\tvalues = append(values, -1, 1)
\t}
\tvalues = append(values, 10)
\tgot, err := MaxSubarraySum(values)
\tif err != nil || got != 20 {
\t\tt.Fatalf("got %d, %v", got, err)
\t}
}
'''

ROTATED_STUB = '''package solution

// Search returns the index of target in a sorted, rotated slice of distinct
// integers, or -1 when the target is absent.
func Search(values []int, target int) int {
\tpanic("not implemented")
}
'''

ROTATED_SOLUTION = '''package solution

// Search returns the index of target in a sorted, rotated slice of distinct
// integers, or -1 when the target is absent.
func Search(values []int, target int) int {
\tlow, high := 0, len(values)-1
\tfor low <= high {
\t\tmiddle := (low + high) / 2
\t\tif values[middle] == target {
\t\t\treturn middle
\t\t}
\t\tif values[low] <= values[middle] {
\t\t\tif values[low] <= target && target < values[middle] {
\t\t\t\thigh = middle - 1
\t\t\t} else {
\t\t\t\tlow = middle + 1
\t\t\t}
\t\t\tcontinue
\t\t}
\t\tif values[middle] < target && target <= values[high] {
\t\t\tlow = middle + 1
\t\t} else {
\t\t\thigh = middle - 1
\t\t}
\t}
\treturn -1
}
'''

ROTATED_VISIBLE = '''package solution

import "testing"

func TestSearchRotated(t *testing.T) {
\tif got := Search([]int{4, 5, 6, 7, 0, 1, 2}, 0); got != 4 {
\t\tt.Fatalf("got %d", got)
\t}
}

func TestSearchMissing(t *testing.T) {
\tif got := Search([]int{4, 5, 6, 7, 0, 1, 2}, 3); got != -1 {
\t\tt.Fatalf("got %d", got)
\t}
}
'''

ROTATED_HIDDEN = '''package solution

import "testing"

func TestSearchNotRotated(t *testing.T) {
\tif got := Search([]int{1, 2, 3, 4, 5}, 3); got != 2 {
\t\tt.Fatalf("got %d", got)
\t}
}

func TestSearchPivot(t *testing.T) {
\tvalues := []int{6, 7, 1, 2, 3}
\tif got := Search(values, 6); got != 0 {
\t\tt.Fatalf("got %d", got)
\t}
\tif got := Search(values, 3); got != 4 {
\t\tt.Fatalf("got %d", got)
\t}
}

func TestSearchTwoElements(t *testing.T) {
\tif got := Search([]int{2, 1}, 1); got != 1 {
\t\tt.Fatalf("got %d", got)
\t}
}

func TestSearchEmpty(t *testing.T) {
\tif got := Search(nil, 5); got != -1 {
\t\tt.Fatalf("got %d", got)
\t}
}
'''

TOPO_STUB = '''package solution

import "errors"

// ErrCycle is returned when the graph contains a cycle.
var ErrCycle = errors.New("graph contains a cycle")

// TopoOrder orders every node so each edge points forward, breaking ties by the
// alphabetically smaller name. It returns ErrCycle for a cyclic graph and an
// error naming the node when an edge references an unknown node.
func TopoOrder(nodes []string, edges [][2]string) ([]string, error) {
\tpanic("not implemented")
}
'''

TOPO_SOLUTION = '''package solution

import (
\t"errors"
\t"fmt"
\t"sort"
)

// ErrCycle is returned when the graph contains a cycle.
var ErrCycle = errors.New("graph contains a cycle")

// TopoOrder orders every node so each edge points forward, breaking ties by the
// alphabetically smaller name. It returns ErrCycle for a cyclic graph and an
// error naming the node when an edge references an unknown node.
func TopoOrder(nodes []string, edges [][2]string) ([]string, error) {
\tincoming := make(map[string]int, len(nodes))
\toutgoing := make(map[string][]string, len(nodes))
\tfor _, node := range nodes {
\t\tincoming[node] = 0
\t\toutgoing[node] = nil
\t}
\tfor _, edge := range edges {
\t\tsource, target := edge[0], edge[1]
\t\tif _, ok := incoming[source]; !ok {
\t\t\treturn nil, fmt.Errorf("unknown node: %s", source)
\t\t}
\t\tif _, ok := incoming[target]; !ok {
\t\t\treturn nil, fmt.Errorf("unknown node: %s", target)
\t\t}
\t\toutgoing[source] = append(outgoing[source], target)
\t\tincoming[target]++
\t}
\tready := make([]string, 0, len(nodes))
\tfor _, node := range nodes {
\t\tif incoming[node] == 0 {
\t\t\tready = append(ready, node)
\t\t}
\t}
\tsort.Strings(ready)
\torder := make([]string, 0, len(nodes))
\tfor len(ready) > 0 {
\t\tnode := ready[0]
\t\tready = ready[1:]
\t\torder = append(order, node)
\t\tfor _, target := range outgoing[node] {
\t\t\tincoming[target]--
\t\t\tif incoming[target] == 0 {
\t\t\t\tready = append(ready, target)
\t\t\t}
\t\t}
\t\tsort.Strings(ready)
\t}
\tif len(order) != len(nodes) {
\t\treturn nil, ErrCycle
\t}
\treturn order, nil
}
'''

TOPO_VISIBLE = '''package solution

import (
\t"reflect"
\t"testing"
)

func TestTopoChain(t *testing.T) {
\tgot, err := TopoOrder([]string{"a", "b", "c"}, [][2]string{{"a", "b"}, {"b", "c"}})
\tif err != nil || !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}

func TestTopoTieBreak(t *testing.T) {
\tgot, err := TopoOrder([]string{"b", "a"}, nil)
\tif err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}
'''

TOPO_HIDDEN = '''package solution

import (
\t"errors"
\t"testing"
)

func TestTopoCycle(t *testing.T) {
\tif _, err := TopoOrder([]string{"a", "b"}, [][2]string{{"a", "b"}, {"b", "a"}}); !errors.Is(err, ErrCycle) {
\t\tt.Fatalf("got %v, want ErrCycle", err)
\t}
}

func TestTopoSelfLoop(t *testing.T) {
\tif _, err := TopoOrder([]string{"a"}, [][2]string{{"a", "a"}}); !errors.Is(err, ErrCycle) {
\t\tt.Fatalf("got %v, want ErrCycle", err)
\t}
}

func TestTopoUnknownNode(t *testing.T) {
\tif _, err := TopoOrder([]string{"a"}, [][2]string{{"a", "z"}}); err == nil {
\t\tt.Fatal("expected an error for an unknown node")
\t}
}

func TestTopoDiamondOrder(t *testing.T) {
\torder, err := TopoOrder([]string{"d", "b", "c", "a"}, [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}})
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif order[0] != "a" || order[len(order)-1] != "d" {
\t\tt.Fatalf("got %v", order)
\t}
\tposition := map[string]int{}
\tfor index, node := range order {
\t\tposition[node] = index
\t}
\tif !(position["b"] < position["d"] && position["c"] < position["d"]) {
\t\tt.Fatalf("got %v", order)
\t}
}

func TestTopoEmpty(t *testing.T) {
\torder, err := TopoOrder(nil, nil)
\tif err != nil || len(order) != 0 {
\t\tt.Fatalf("got %v, %v", order, err)
\t}
}
'''

# --------------------------------------------------------------------------- #
# modules
# --------------------------------------------------------------------------- #

LRU_STUB = '''package solution

import "errors"

// ErrCapacity is returned for a non-positive capacity.
var ErrCapacity = errors.New("capacity must be positive")

// LRUCache is a fixed-capacity cache that evicts the least recently used entry.
type LRUCache struct {
}

// NewLRUCache builds a cache holding at most capacity entries.
func NewLRUCache(capacity int) (*LRUCache, error) {
\tpanic("not implemented")
}

// Get returns the stored value and whether the key was present.
func (c *LRUCache) Get(key string) (int, bool) {
\tpanic("not implemented")
}

// Put stores a value, marking the key most recently used.
func (c *LRUCache) Put(key string, value int) {
\tpanic("not implemented")
}

// Len reports how many entries are currently stored.
func (c *LRUCache) Len() int {
\tpanic("not implemented")
}
'''

LRU_SOLUTION = '''package solution

import (
\t"container/list"
\t"errors"
)

// ErrCapacity is returned for a non-positive capacity.
var ErrCapacity = errors.New("capacity must be positive")

type lruEntry struct {
\tkey   string
\tvalue int
}

// LRUCache is a fixed-capacity cache that evicts the least recently used entry.
type LRUCache struct {
\tcapacity int
\torder    *list.List
\tentries  map[string]*list.Element
}

// NewLRUCache builds a cache holding at most capacity entries.
func NewLRUCache(capacity int) (*LRUCache, error) {
\tif capacity < 1 {
\t\treturn nil, ErrCapacity
\t}
\treturn &LRUCache{capacity: capacity, order: list.New(), entries: map[string]*list.Element{}}, nil
}

// Get returns the stored value and whether the key was present.
func (c *LRUCache) Get(key string) (int, bool) {
\telement, ok := c.entries[key]
\tif !ok {
\t\treturn 0, false
\t}
\tc.order.MoveToBack(element)
\treturn element.Value.(*lruEntry).value, true
}

// Put stores a value, marking the key most recently used.
func (c *LRUCache) Put(key string, value int) {
\tif element, ok := c.entries[key]; ok {
\t\telement.Value.(*lruEntry).value = value
\t\tc.order.MoveToBack(element)
\t\treturn
\t}
\tc.entries[key] = c.order.PushBack(&lruEntry{key: key, value: value})
\tfor c.order.Len() > c.capacity {
\t\toldest := c.order.Front()
\t\tc.order.Remove(oldest)
\t\tdelete(c.entries, oldest.Value.(*lruEntry).key)
\t}
}

// Len reports how many entries are currently stored.
func (c *LRUCache) Len() int {
\treturn c.order.Len()
}
'''

LRU_VISIBLE = '''package solution

import "testing"

func TestLRUPutGet(t *testing.T) {
\tcache, err := NewLRUCache(2)
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tcache.Put("a", 1)
\tif value, ok := cache.Get("a"); !ok || value != 1 {
\t\tt.Fatalf("got %d, %v", value, ok)
\t}
}

func TestLRURejectsZeroCapacity(t *testing.T) {
\tif _, err := NewLRUCache(0); err == nil {
\t\tt.Fatal("expected an error for capacity 0")
\t}
}
'''

LRU_HIDDEN = '''package solution

import "testing"

func TestLRUEvictsLeastRecentlyUsed(t *testing.T) {
\tcache, _ := NewLRUCache(2)
\tcache.Put("a", 1)
\tcache.Put("b", 2)
\tcache.Get("a")
\tcache.Put("c", 3)
\tif _, ok := cache.Get("b"); ok {
\t\tt.Fatal("b should have been evicted")
\t}
\tif value, ok := cache.Get("a"); !ok || value != 1 {
\t\tt.Fatalf("got %d, %v", value, ok)
\t}
\tif value, ok := cache.Get("c"); !ok || value != 3 {
\t\tt.Fatalf("got %d, %v", value, ok)
\t}
}

func TestLRUOverwriteKeepsSize(t *testing.T) {
\tcache, _ := NewLRUCache(1)
\tcache.Put("a", 1)
\tcache.Put("a", 2)
\tif cache.Len() != 1 {
\t\tt.Fatalf("got %d", cache.Len())
\t}
\tif value, _ := cache.Get("a"); value != 2 {
\t\tt.Fatalf("got %d", value)
\t}
}

func TestLRUMissingKey(t *testing.T) {
\tcache, _ := NewLRUCache(1)
\tif _, ok := cache.Get("missing"); ok {
\t\tt.Fatal("unexpected hit")
\t}
}
'''

BUCKET_STUB = '''package solution

import "errors"

// ErrParameter is returned for a non-positive rate, capacity or request count.
var ErrParameter = errors.New("invalid token bucket parameter")

// TokenBucket refills at rate tokens per second, up to capacity, using clock.
type TokenBucket struct {
}

// NewTokenBucket builds a bucket that starts full.
func NewTokenBucket(rate float64, capacity int, clock func() float64) (*TokenBucket, error) {
\tpanic("not implemented")
}

// Allow removes count tokens and reports whether they were available.
func (b *TokenBucket) Allow(count int) (bool, error) {
\tpanic("not implemented")
}

// Tokens reports the current token level.
func (b *TokenBucket) Tokens() float64 {
\tpanic("not implemented")
}
'''

BUCKET_SOLUTION = '''package solution

import "errors"

// ErrParameter is returned for a non-positive rate, capacity or request count.
var ErrParameter = errors.New("invalid token bucket parameter")

// TokenBucket refills at rate tokens per second, up to capacity, using clock.
type TokenBucket struct {
\trate     float64
\tcapacity float64
\tclock    func() float64
\ttokens   float64
\tupdated  float64
}

// NewTokenBucket builds a bucket that starts full.
func NewTokenBucket(rate float64, capacity int, clock func() float64) (*TokenBucket, error) {
\tif rate <= 0 || capacity < 1 {
\t\treturn nil, ErrParameter
\t}
\treturn &TokenBucket{
\t\trate:     rate,
\t\tcapacity: float64(capacity),
\t\tclock:    clock,
\t\ttokens:   float64(capacity),
\t\tupdated:  clock(),
\t}, nil
}

// Allow removes count tokens and reports whether they were available.
func (b *TokenBucket) Allow(count int) (bool, error) {
\tif count < 1 {
\t\treturn false, ErrParameter
\t}
\tb.refill()
\tif b.tokens >= float64(count) {
\t\tb.tokens -= float64(count)
\t\treturn true, nil
\t}
\treturn false, nil
}

func (b *TokenBucket) refill() {
\tnow := b.clock()
\tif now > b.updated {
\t\tb.tokens += (now - b.updated) * b.rate
\t\tif b.tokens > b.capacity {
\t\t\tb.tokens = b.capacity
\t\t}
\t\tb.updated = now
\t}
}

// Tokens reports the current token level.
func (b *TokenBucket) Tokens() float64 {
\tb.refill()
\treturn b.tokens
}
'''

BUCKET_VISIBLE = '''package solution

import "testing"

func TestBucketBurst(t *testing.T) {
\tbucket, err := NewTokenBucket(1, 3, func() float64 { return 0 })
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif ok, _ := bucket.Allow(3); !ok {
\t\tt.Fatal("expected the initial burst to be allowed")
\t}
\tif ok, _ := bucket.Allow(1); ok {
\t\tt.Fatal("expected the bucket to be empty")
\t}
}

func TestBucketRefill(t *testing.T) {
\tnow := 0.0
\tbucket, _ := NewTokenBucket(2, 2, func() float64 { return now })
\tbucket.Allow(2)
\tnow = 1.0
\tif ok, _ := bucket.Allow(2); !ok {
\t\tt.Fatal("expected refilled tokens")
\t}
}
'''

BUCKET_HIDDEN = '''package solution

import (
\t"errors"
\t"testing"
)

func TestBucketRefillCapped(t *testing.T) {
\tnow := 0.0
\tbucket, _ := NewTokenBucket(10, 2, func() float64 { return now })
\tbucket.Allow(2)
\tnow = 100.0
\tif got := bucket.Tokens(); got != 2 {
\t\tt.Fatalf("got %v", got)
\t}
}

func TestBucketRefusedRequestConsumesNothing(t *testing.T) {
\tbucket, _ := NewTokenBucket(1, 1, func() float64 { return 0 })
\tbucket.Allow(1)
\tif ok, _ := bucket.Allow(1); ok {
\t\tt.Fatal("unexpected allowance")
\t}
\tif got := bucket.Tokens(); got != 0 {
\t\tt.Fatalf("got %v", got)
\t}
}

func TestBucketInvalidParameters(t *testing.T) {
\tif _, err := NewTokenBucket(0, 1, func() float64 { return 0 }); !errors.Is(err, ErrParameter) {
\t\tt.Fatal("expected ErrParameter for rate 0")
\t}
\tif _, err := NewTokenBucket(1, 0, func() float64 { return 0 }); !errors.Is(err, ErrParameter) {
\t\tt.Fatal("expected ErrParameter for capacity 0")
\t}
\tbucket, _ := NewTokenBucket(1, 1, func() float64 { return 0 })
\tif _, err := bucket.Allow(0); !errors.Is(err, ErrParameter) {
\t\tt.Fatal("expected ErrParameter for count 0")
\t}
}
'''

# --------------------------------------------------------------------------- #
# bug fixes
# --------------------------------------------------------------------------- #

WINDOW_BUGGY = '''package solution

import "errors"

// ErrWidth is returned for a non-positive width.
var ErrWidth = errors.New("width must be positive")

// WindowMaxima returns the maximum of every contiguous window of the given
// width, one value per window.
func WindowMaxima(values []int, width int) ([]int, error) {
\tif width <= 0 {
\t\treturn nil, ErrWidth
\t}
\tif width > len(values) {
\t\treturn []int{}, nil
\t}
\tresult := make([]int, 0, len(values)-width+1)
\tfor start := 0; start < len(values)-width; start++ {
\t\tbest := values[start]
\t\tfor _, value := range values[start : start+width] {
\t\t\tif value > best {
\t\t\t\tbest = value
\t\t\t}
\t\t}
\t\tresult = append(result, best)
\t}
\treturn result, nil
}
'''

WINDOW_FIXED = '''package solution

import "errors"

// ErrWidth is returned for a non-positive width.
var ErrWidth = errors.New("width must be positive")

// WindowMaxima returns the maximum of every contiguous window of the given
// width, one value per window.
func WindowMaxima(values []int, width int) ([]int, error) {
\tif width <= 0 {
\t\treturn nil, ErrWidth
\t}
\tif width > len(values) {
\t\treturn []int{}, nil
\t}
\tresult := make([]int, 0, len(values)-width+1)
\tfor start := 0; start+width <= len(values); start++ {
\t\tbest := values[start]
\t\tfor _, value := range values[start : start+width] {
\t\t\tif value > best {
\t\t\t\tbest = value
\t\t\t}
\t\t}
\t\tresult = append(result, best)
\t}
\treturn result, nil
}
'''

WINDOW_GO_VISIBLE = '''package solution

import (
\t"reflect"
\t"testing"
)

func TestWindowMaximaBasic(t *testing.T) {
\tgot, err := WindowMaxima([]int{1, 2, 3, 4}, 2)
\tif err != nil || !reflect.DeepEqual(got, []int{2, 3, 4}) {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}

func TestWindowMaximaLastWindow(t *testing.T) {
\tgot, err := WindowMaxima([]int{5, 1, 1, 9}, 3)
\tif err != nil || !reflect.DeepEqual(got, []int{5, 9}) {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}
'''

WINDOW_GO_HIDDEN = '''package solution

import (
\t"errors"
\t"reflect"
\t"testing"
)

func TestWindowMaximaExactWidth(t *testing.T) {
\tgot, err := WindowMaxima([]int{4, 2, 7}, 3)
\tif err != nil || !reflect.DeepEqual(got, []int{7}) {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}

func TestWindowMaximaWidthTooLarge(t *testing.T) {
\tgot, err := WindowMaxima([]int{1, 2}, 5)
\tif err != nil || len(got) != 0 {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}

func TestWindowMaximaInvalidWidth(t *testing.T) {
\tif _, err := WindowMaxima([]int{1}, 0); !errors.Is(err, ErrWidth) {
\t\tt.Fatal("expected ErrWidth")
\t}
}

func TestWindowMaximaWidthOne(t *testing.T) {
\tgot, err := WindowMaxima([]int{3, 1, 2}, 1)
\tif err != nil || !reflect.DeepEqual(got, []int{3, 1, 2}) {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}
'''

OVERLAP_BUGGY = '''package solution

// Overlaps reports whether two closed integer intervals [aStart, aEnd] and
// [bStart, bEnd] share at least one integer.
func Overlaps(aStart, aEnd, bStart, bEnd int) bool {
\treturn aStart <= bEnd && bStart < aEnd
}
'''

OVERLAP_FIXED = '''package solution

// Overlaps reports whether two closed integer intervals [aStart, aEnd] and
// [bStart, bEnd] share at least one integer.
func Overlaps(aStart, aEnd, bStart, bEnd int) bool {
\treturn aStart <= bEnd && bStart <= aEnd
}
'''

OVERLAP_VISIBLE = '''package solution

import "testing"

func TestOverlapsSharedRange(t *testing.T) {
\tif !Overlaps(1, 5, 3, 8) {
\t\tt.Fatal("ranges sharing 3..5 must overlap")
\t}
}

func TestOverlapsTouchingEndpoints(t *testing.T) {
\tif !Overlaps(1, 3, 3, 5) {
\t\tt.Fatal("closed ranges sharing the endpoint 3 must overlap")
\t}
}
'''

OVERLAP_HIDDEN = '''package solution

import "testing"

func TestOverlapsSeparated(t *testing.T) {
\tif Overlaps(1, 2, 3, 4) {
\t\tt.Fatal("separated ranges must not overlap")
\t}
}

func TestOverlapsContained(t *testing.T) {
\tif !Overlaps(1, 10, 4, 6) {
\t\tt.Fatal("a contained range must overlap")
\t}
}

func TestOverlapsSinglePoints(t *testing.T) {
\tif !Overlaps(2, 2, 2, 2) {
\t\tt.Fatal("equal single points must overlap")
\t}
}

func TestOverlapsReversedOrder(t *testing.T) {
\tif !Overlaps(7, 9, 1, 7) {
\t\tt.Fatal("closed ranges sharing 7 must overlap regardless of order")
\t}
}
'''

MEAN_BUGGY = '''package solution

import "errors"

// ErrNoValues is returned for an empty slice.
var ErrNoValues = errors.New("values must not be empty")

// Mean returns the arithmetic mean of the values.
func Mean(values []int) (float64, error) {
\tif len(values) == 0 {
\t\treturn 0, ErrNoValues
\t}
\tsum := 0
\tfor _, value := range values {
\t\tsum += value
\t}
\treturn float64(sum / len(values)), nil
}
'''

MEAN_FIXED = '''package solution

import "errors"

// ErrNoValues is returned for an empty slice.
var ErrNoValues = errors.New("values must not be empty")

// Mean returns the arithmetic mean of the values.
func Mean(values []int) (float64, error) {
\tif len(values) == 0 {
\t\treturn 0, ErrNoValues
\t}
\tsum := 0
\tfor _, value := range values {
\t\tsum += value
\t}
\treturn float64(sum) / float64(len(values)), nil
}
'''

MEAN_VISIBLE = '''package solution

import "testing"

func TestMeanWholeResult(t *testing.T) {
\tgot, err := Mean([]int{2, 4})
\tif err != nil || got != 3 {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}

func TestMeanKeepsFraction(t *testing.T) {
\tgot, err := Mean([]int{1, 2})
\tif err != nil || got != 1.5 {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}
'''

MEAN_HIDDEN = '''package solution

import (
\t"errors"
\t"testing"
)

func TestMeanEmptyFails(t *testing.T) {
\tif _, err := Mean(nil); !errors.Is(err, ErrNoValues) {
\t\tt.Fatalf("got %v", err)
\t}
}

func TestMeanSingleValue(t *testing.T) {
\tgot, err := Mean([]int{7})
\tif err != nil || got != 7 {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}

func TestMeanNegativeValues(t *testing.T) {
\tgot, err := Mean([]int{-1, -2, -3})
\tif err != nil || got != -2 {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}

func TestMeanRoundingNotTruncated(t *testing.T) {
\tgot, err := Mean([]int{10, 10, 11})
\tif err != nil || got != 31.0/3.0 {
\t\tt.Fatalf("got %v, %v", got, err)
\t}
}
'''


def tasks() -> list:
    """Return every Go task in the lite set."""
    return [
        go_task(
            "go-algo-run-length", ALGO,
            "Implement Encode and Decode in solution.go. Encode folds consecutive equal "
            "characters into a []Pair of {Rune, Count} in order and returns an empty result for "
            "empty input. Decode rebuilds the original string. Keep the Pair type and both "
            "signatures unchanged.",
            RUN_LENGTH_STUB, RUN_LENGTH_SOLUTION, RUN_LENGTH_VISIBLE, RUN_LENGTH_HIDDEN,
            ["encode folds consecutive equal runes and handles empty input",
             "decode inverts encode exactly, including counts above nine and non-ASCII text"],
            ["algorithm", "string"],
        ),
        go_task(
            "go-algo-merge-intervals", ALGO,
            "Implement Merge in solution.go: given closed integer intervals as [][]int in any "
            "order, return them merged and sorted by start. Overlapping and touching intervals "
            "merge into one. Do not modify the caller's input slices.",
            MERGE_STUB, MERGE_SOLUTION, MERGE_VISIBLE, MERGE_HIDDEN,
            ["overlapping and touching intervals merge",
             "output is sorted by start and the input is left unchanged"],
            ["algorithm", "intervals"],
        ),
        go_task(
            "go-algo-max-subarray", ALGO,
            "Implement MaxSubarraySum in solution.go using a single linear pass: return the "
            "largest sum of a non-empty contiguous subarray, and ErrEmpty for an empty slice.",
            MAX_SUBARRAY_STUB, MAX_SUBARRAY_SOLUTION, MAX_SUBARRAY_VISIBLE, MAX_SUBARRAY_HIDDEN,
            ["the maximum is over non-empty contiguous subarrays",
             "all-negative input returns the largest single value",
             "empty input returns ErrEmpty"],
            ["algorithm", "dynamic-programming"],
        ),
        go_task(
            "go-algo-rotated-search", ALGO,
            "Implement Search in solution.go: values is sorted ascending and rotated by an "
            "unknown amount with distinct elements. Return the index of target, or -1 when "
            "absent, in logarithmic time.",
            ROTATED_STUB, ROTATED_SOLUTION, ROTATED_VISIBLE, ROTATED_HIDDEN,
            ["works on rotated and unrotated input",
             "absent targets and empty input return -1"],
            ["algorithm", "binary-search"],
        ),
        go_task(
            "go-algo-toposort", ALGO,
            "Implement TopoOrder in solution.go: return a slice ordering every node so each edge "
            "points forward, choosing the alphabetically smallest available node at each step. "
            "Return ErrCycle for a cyclic graph, and an error for an edge naming an unknown node.",
            TOPO_STUB, TOPO_SOLUTION, TOPO_VISIBLE, TOPO_HIDDEN,
            ["every edge points forward in the returned order",
             "ties are broken by node name",
             "cycles return ErrCycle; unknown nodes return an error"],
            ["algorithm", "graph"],
        ),
        go_task(
            "go-mod-lru-cache", MODULE,
            "Implement LRUCache in solution.go: NewLRUCache(capacity) returns ErrCapacity for a "
            "non-positive capacity. Get returns the value and whether the key was present; Put "
            "stores a value and marks it most recently used; when a Put exceeds capacity, evict "
            "the least recently used entry. Len reports the number of stored entries.",
            LRU_STUB, LRU_SOLUTION, LRU_VISIBLE, LRU_HIDDEN,
            ["reads and writes both refresh recency",
             "eviction removes the least recently used entry",
             "a non-positive capacity returns ErrCapacity"],
            ["module", "cache"],
        ),
        go_task(
            "go-mod-token-bucket", MODULE,
            "Implement TokenBucket in solution.go: NewTokenBucket(rate, capacity, clock) starts "
            "full and returns ErrParameter for a non-positive rate or capacity. Allow(count) "
            "refills proportionally to elapsed clock time (capped at capacity), removes count "
            "tokens when available and reports success, otherwise changes nothing; a count below "
            "one returns ErrParameter. Tokens reports the current level.",
            BUCKET_STUB, BUCKET_SOLUTION, BUCKET_VISIBLE, BUCKET_HIDDEN,
            ["refill is proportional to elapsed time and capped at capacity",
             "a refused request consumes nothing",
             "invalid parameters return ErrParameter"],
            ["module", "rate-limiting"],
        ),
        go_task(
            "go-bug-window-maxima", BUGFIX,
            "solution.go computes the maximum of every sliding window but omits the final "
            "window. Fix WindowMaxima in solution.go so it returns one maximum per window, "
            "including the last, while keeping the width validation. Run `go test ./...` to see "
            "the failure.",
            WINDOW_BUGGY, WINDOW_FIXED, WINDOW_GO_VISIBLE, WINDOW_GO_HIDDEN,
            ["every window, including the last, contributes one maximum",
             "invalid widths still return ErrWidth"],
            ["bugfix", "off-by-one"],
        ),
        go_task(
            "go-bug-closed-intervals", BUGFIX,
            "solution.go treats closed intervals as half-open, so intervals that merely share an "
            "endpoint are reported as disjoint. Fix Overlaps in solution.go so two closed "
            "intervals sharing any integer, including a single endpoint, overlap.",
            OVERLAP_BUGGY, OVERLAP_FIXED, OVERLAP_VISIBLE, OVERLAP_HIDDEN,
            ["intervals sharing an endpoint overlap",
             "separated and contained intervals are classified correctly"],
            ["bugfix", "boundary"],
        ),
        go_task(
            "go-bug-integer-mean", BUGFIX,
            "solution.go computes the mean with integer division, truncating any fraction. Fix "
            "Mean in solution.go so it returns the exact arithmetic mean while still returning "
            "ErrNoValues for an empty slice.",
            MEAN_BUGGY, MEAN_FIXED, MEAN_VISIBLE, MEAN_HIDDEN,
            ["the mean keeps its fractional part",
             "empty input still returns ErrNoValues"],
            ["bugfix", "integer-division"],
        ),
    ]
