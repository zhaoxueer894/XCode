"""Python lite tasks: single-file algorithms, small modules and bug fixes.

Every task is small enough to verify in milliseconds with the standard
library's unittest, so it needs no network, no package install and no build.
Each entry declares four sources: the stub or buggy starter, the reference
solution, the visible tests the agent may run, and the hidden tests used for
grading. ``gen_tasks.py`` materialises them.

Function signatures stay on one line so a stub can be derived mechanically.
"""

from __future__ import annotations

ALGO = "algo"
MODULE = "module"
BUGFIX = "bugfix"


def py_task(id, kind, prompt, stub, solution, visible, hidden, acceptance, tags=()):
    """Assemble one Python task's file trees and metadata."""
    return {
        "id": id,
        "language": "python",
        "kind": kind,
        "prompt": prompt,
        "acceptance": acceptance,
        "tags": list(tags),
        "starter": {"solution.py": stub, "test_visible.py": visible},
        # The grade overlay restates the authoritative visible suite, so an agent
        # that edits or deletes it cannot change the verdict; hidden tests add
        # the edge cases the visible suite deliberately omits.
        "solution": {"solution.py": solution},
        "hidden": {"test_visible.py": visible, "test_hidden.py": hidden},
        "verify_commands": [["{python}", "-m", "unittest", "-v", "test_visible.py"]],
        "hidden_command": [["{python}", "-m", "unittest", "-v", "test_visible.py", "test_hidden.py"]],
    }


STUB_NOT_IMPLEMENTED = "raise NotImplementedError"


def stub_of(solution: str) -> str:
    """Derive a stub that keeps each public signature but implements nothing."""
    lines = ["\"\"\"Implement the functions declared below; keep the signatures unchanged.\"\"\""]
    for line in solution.splitlines():
        if line.startswith("def ") or line.startswith("class "):
            head = line.rstrip()
            if head.endswith(":"):
                lines.append(head)
            else:
                lines.append(head + ":")
            if line.startswith("class "):
                lines.append("    pass")
            else:
                lines.append("    " + STUB_NOT_IMPLEMENTED)
            lines.append("")
    return "\n".join(lines) + "\n"


# --------------------------------------------------------------------------- #
# algorithms
# --------------------------------------------------------------------------- #

RUN_LENGTH_SOLUTION = '''"""Run-length encoding over characters."""


def encode(text: str) -> list[list]:
    if not text:
        return []
    result = []
    current = text[0]
    count = 1
    for character in text[1:]:
        if character == current:
            count += 1
            continue
        result.append([current, count])
        current = character
        count = 1
    result.append([current, count])
    return result


def decode(pairs: list[list]) -> str:
    return "".join(character * count for character, count in pairs)
'''

RUN_LENGTH_VISIBLE = '''import unittest

from solution import decode, encode


class TestRunLength(unittest.TestCase):
    def test_single_character(self):
        self.assertEqual(encode("a"), [["a", 1]])

    def test_runs(self):
        self.assertEqual(encode("aaabbc"), [["a", 3], ["b", 2], ["c", 1]])

    def test_empty(self):
        self.assertEqual(encode(""), [])

    def test_decode(self):
        self.assertEqual(decode([["a", 3], ["b", 2]]), "aaabb")


if __name__ == "__main__":
    unittest.main()
'''

RUN_LENGTH_HIDDEN = '''import unittest

from solution import decode, encode


class TestRunLengthEdges(unittest.TestCase):
    def test_round_trip_long(self):
        text = ("abcd" * 25) + ("z" * 10)
        self.assertEqual(decode(encode(text)), text)

    def test_run_longer_than_nine(self):
        self.assertEqual(encode("a" * 12), [["a", 12]])
        self.assertEqual(decode([["a", 12]]), "a" * 12)

    def test_non_ascii(self):
        self.assertEqual(encode("\\u4f60\\u4f60\\u597d"), [["\\u4f60", 2], ["\\u597d", 1]])
        self.assertEqual(decode(encode("\\u4f60\\u597d\\u597d")), "\\u4f60\\u597d\\u597d")

    def test_decode_empty(self):
        self.assertEqual(decode([]), "")


if __name__ == "__main__":
    unittest.main()
'''

MERGE_INTERVALS_SOLUTION = '''"""Merge overlapping closed integer intervals."""


def merge(intervals: list[list]) -> list[list]:
    if not intervals:
        return []
    ordered = sorted([list(pair) for pair in intervals], key=lambda pair: (pair[0], pair[1]))
    merged = [ordered[0]]
    for start, end in ordered[1:]:
        if start <= merged[-1][1]:
            if end > merged[-1][1]:
                merged[-1][1] = end
            continue
        merged.append([start, end])
    return merged
'''

MERGE_INTERVALS_VISIBLE = '''import unittest

from solution import merge


class TestMergeIntervals(unittest.TestCase):
    def test_overlapping(self):
        self.assertEqual(merge([[1, 3], [2, 6], [8, 10]]), [[1, 6], [8, 10]])

    def test_disjoint(self):
        self.assertEqual(merge([[1, 2], [5, 6]]), [[1, 2], [5, 6]])

    def test_empty(self):
        self.assertEqual(merge([]), [])

    def test_unsorted_input(self):
        self.assertEqual(merge([[8, 10], [1, 3], [2, 6]]), [[1, 6], [8, 10]])


if __name__ == "__main__":
    unittest.main()
'''

MERGE_INTERVALS_HIDDEN = '''import unittest

from solution import merge


class TestMergeIntervalsEdges(unittest.TestCase):
    def test_touching_endpoints_merge(self):
        self.assertEqual(merge([[1, 3], [3, 5]]), [[1, 5]])

    def test_contained_interval_is_absorbed(self):
        self.assertEqual(merge([[1, 10], [3, 4]]), [[1, 10]])

    def test_input_is_not_mutated(self):
        original = [[1, 4], [2, 3]]
        merge(original)
        self.assertEqual(original, [[1, 4], [2, 3]])

    def test_duplicate_intervals(self):
        self.assertEqual(merge([[2, 2], [2, 2]]), [[2, 2]])


if __name__ == "__main__":
    unittest.main()
'''

TOPO_SOLUTION = '''"""Deterministic topological order for a directed acyclic graph."""


def topological_order(nodes: list, edges: list):
    incoming = {node: 0 for node in nodes}
    outgoing = {node: [] for node in nodes}
    for source, target in edges:
        if source not in incoming or target not in incoming:
            raise ValueError("edge references an unknown node")
        outgoing[source].append(target)
        incoming[target] += 1
    ready = sorted(node for node, count in incoming.items() if count == 0)
    order = []
    while ready:
        node = ready.pop(0)
        order.append(node)
        for target in sorted(outgoing[node]):
            incoming[target] -= 1
            if incoming[target] == 0:
                ready.append(target)
        ready.sort()
    if len(order) != len(nodes):
        return None
    return order
'''

TOPO_VISIBLE = '''import unittest

from solution import topological_order


class TestTopologicalOrder(unittest.TestCase):
    def test_chain(self):
        self.assertEqual(topological_order(["a", "b", "c"], [("a", "b"), ("b", "c")]), ["a", "b", "c"])

    def test_ties_break_by_name(self):
        self.assertEqual(topological_order(["b", "a"], []), ["a", "b"])

    def test_empty(self):
        self.assertEqual(topological_order([], []), [])


if __name__ == "__main__":
    unittest.main()
'''

TOPO_HIDDEN = '''import unittest

from solution import topological_order


class TestTopologicalOrderEdges(unittest.TestCase):
    def test_cycle_returns_none(self):
        self.assertIsNone(topological_order(["a", "b"], [("a", "b"), ("b", "a")]))

    def test_diamond_respects_all_dependencies(self):
        order = topological_order(["d", "b", "c", "a"], [("a", "b"), ("a", "c"), ("b", "d"), ("c", "d")])
        self.assertEqual(order[0], "a")
        self.assertEqual(order[-1], "d")
        self.assertLess(order.index("b"), order.index("d"))
        self.assertLess(order.index("c"), order.index("d"))

    def test_unknown_node_raises(self):
        with self.assertRaises(ValueError):
            topological_order(["a"], [("a", "z")])

    def test_self_loop_is_a_cycle(self):
        self.assertIsNone(topological_order(["a"], [("a", "a")]))


if __name__ == "__main__":
    unittest.main()
'''

MAX_SUBARRAY_SOLUTION = '''"""Kadane's algorithm: maximum sum of a non-empty contiguous subarray."""


def max_subarray_sum(values: list) -> int:
    if not values:
        raise ValueError("values must not be empty")
    best = running = values[0]
    for value in values[1:]:
        running = value if running < 0 else running + value
        if running > best:
            best = running
    return best
'''

MAX_SUBARRAY_VISIBLE = '''import unittest

from solution import max_subarray_sum


class TestMaxSubarray(unittest.TestCase):
    def test_mixed(self):
        self.assertEqual(max_subarray_sum([-2, 1, -3, 4, -1, 2, 1, -5, 4]), 6)

    def test_all_negative(self):
        self.assertEqual(max_subarray_sum([-3, -1, -2]), -1)

    def test_single(self):
        self.assertEqual(max_subarray_sum([5]), 5)


if __name__ == "__main__":
    unittest.main()
'''

MAX_SUBARRAY_HIDDEN = '''import unittest

from solution import max_subarray_sum


class TestMaxSubarrayEdges(unittest.TestCase):
    def test_empty_raises(self):
        with self.assertRaises(ValueError):
            max_subarray_sum([])

    def test_all_positive(self):
        self.assertEqual(max_subarray_sum([1, 2, 3, 4]), 10)

    def test_zeros_and_negatives(self):
        self.assertEqual(max_subarray_sum([0, -1, 0]), 0)

    def test_long_mixed(self):
        values = [10] + [-1, 1] * 50 + [10]
        self.assertEqual(max_subarray_sum(values), 20)


if __name__ == "__main__":
    unittest.main()
'''

ROTATED_SEARCH_SOLUTION = '''"""Binary search over a rotated sorted array of distinct integers."""


def search(values: list, target: int) -> int:
    low, high = 0, len(values) - 1
    while low <= high:
        middle = (low + high) // 2
        if values[middle] == target:
            return middle
        if values[low] <= values[middle]:
            if values[low] <= target < values[middle]:
                high = middle - 1
            else:
                low = middle + 1
        else:
            if values[middle] < target <= values[high]:
                low = middle + 1
            else:
                high = middle - 1
    return -1
'''

ROTATED_SEARCH_VISIBLE = '''import unittest

from solution import search


class TestRotatedSearch(unittest.TestCase):
    def test_found_right_half(self):
        self.assertEqual(search([4, 5, 6, 7, 0, 1, 2], 0), 4)

    def test_not_found(self):
        self.assertEqual(search([4, 5, 6, 7, 0, 1, 2], 3), -1)

    def test_single(self):
        self.assertEqual(search([1], 1), 0)


if __name__ == "__main__":
    unittest.main()
'''

ROTATED_SEARCH_HIDDEN = '''import unittest

from solution import search


class TestRotatedSearchEdges(unittest.TestCase):
    def test_not_rotated(self):
        self.assertEqual(search([1, 2, 3, 4, 5], 3), 2)

    def test_empty(self):
        self.assertEqual(search([], 9), -1)

    def test_pivot_element(self):
        self.assertEqual(search([6, 7, 1, 2, 3], 6), 0)
        self.assertEqual(search([6, 7, 1, 2, 3], 3), 4)

    def test_single_missing(self):
        self.assertEqual(search([1], 0), -1)

    def test_two_elements_rotated(self):
        self.assertEqual(search([2, 1], 1), 1)


if __name__ == "__main__":
    unittest.main()
'''

PARENS_SOLUTION = '''"""Balanced parentheses where '*' may stand for '(' , ')' or nothing."""


def is_balanced(text: str) -> bool:
    low = high = 0
    for character in text:
        if character == "(":
            low += 1
            high += 1
        elif character == ")":
            low = max(low - 1, 0)
            high -= 1
            if high < 0:
                return False
        elif character == "*":
            low = max(low - 1, 0)
            high += 1
        else:
            raise ValueError("unsupported character: " + character)
    return low == 0
'''

PARENS_VISIBLE = '''import unittest

from solution import is_balanced


class TestParens(unittest.TestCase):
    def test_plain(self):
        self.assertTrue(is_balanced("(())"))
        self.assertFalse(is_balanced("(()"))

    def test_star(self):
        self.assertTrue(is_balanced("(*)"))

    def test_empty(self):
        self.assertTrue(is_balanced(""))


if __name__ == "__main__":
    unittest.main()
'''

PARENS_HIDDEN = '''import unittest

from solution import is_balanced


class TestParensEdges(unittest.TestCase):
    def test_too_many_closing(self):
        self.assertFalse(is_balanced("(*)))"))

    def test_only_stars(self):
        self.assertTrue(is_balanced("***"))

    def test_unmatched_close_early(self):
        self.assertFalse(is_balanced(")("))

    def test_unsupported_character(self):
        with self.assertRaises(ValueError):
            is_balanced("(a)")

    def test_long_mixed(self):
        self.assertTrue(is_balanced("(*)(*)(*)"))
        self.assertFalse(is_balanced("(((*("))


if __name__ == "__main__":
    unittest.main()
'''

# --------------------------------------------------------------------------- #
# modules
# --------------------------------------------------------------------------- #

INI_SOLUTION = '''"""A small INI-style configuration reader."""


def parse_ini(text: str) -> dict:
    sections = {}
    current = ""
    for number, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#") or line.startswith(";"):
            continue
        if line.startswith("[") and line.endswith("]"):
            current = line[1:-1].strip()
            sections.setdefault(current, {})
            continue
        if "=" not in line:
            raise ValueError(f"line {number}: expected key=value")
        key, _, value = line.partition("=")
        sections.setdefault(current, {})[key.strip()] = value.strip()
    return sections
'''

INI_VISIBLE = '''import unittest

from solution import parse_ini


class TestIniParser(unittest.TestCase):
    def test_sections(self):
        text = "[a]\\nx = 1\\n[b]\\ny = 2\\n"
        self.assertEqual(parse_ini(text), {"a": {"x": "1"}, "b": {"y": "2"}})

    def test_global_keys(self):
        self.assertEqual(parse_ini("top = 1\\n[s]\\nk = v\\n"), {"": {"top": "1"}, "s": {"k": "v"}})

    def test_comments_and_blanks(self):
        self.assertEqual(parse_ini("# c\\n\\n; d\\n[s]\\nk=v\\n"), {"s": {"k": "v"}})


if __name__ == "__main__":
    unittest.main()
'''

INI_HIDDEN = '''import unittest

from solution import parse_ini


class TestIniParserEdges(unittest.TestCase):
    def test_value_keeps_equals(self):
        self.assertEqual(parse_ini("[s]\\nurl = a=b\\n"), {"s": {"url": "a=b"}})

    def test_duplicate_key_last_wins(self):
        self.assertEqual(parse_ini("[s]\\nk=1\\nk=2\\n"), {"s": {"k": "2"}})

    def test_empty_value(self):
        self.assertEqual(parse_ini("[s]\\nk =\\n"), {"s": {"k": ""}})

    def test_malformed_line_raises(self):
        with self.assertRaises(ValueError):
            parse_ini("[s]\\nnot a pair\\n")

    def test_empty_section_repeated(self):
        self.assertEqual(parse_ini("[s]\\n[s]\\nk=v\\n"), {"s": {"k": "v"}})


if __name__ == "__main__":
    unittest.main()
'''

TTL_CACHE_SOLUTION = '''"""A capacity-bounded cache with per-entry expiry."""


class TTLCache:
    def __init__(self, capacity: int, ttl: float, clock):
        if capacity < 1:
            raise ValueError("capacity must be positive")
        if ttl <= 0:
            raise ValueError("ttl must be positive")
        self._capacity = capacity
        self._ttl = ttl
        self._clock = clock
        # Maps key -> [value, expires_at]; dict order is the recency order.
        self._entries = {}

    def _live(self, key):
        entry = self._entries.get(key)
        if entry is None:
            return None
        if self._clock() >= entry[1]:
            del self._entries[key]
            return None
        return entry

    def get(self, key, default=None):
        entry = self._live(key)
        if entry is None:
            return default
        del self._entries[key]
        self._entries[key] = entry
        return entry[0]

    def set(self, key, value):
        self._entries.pop(key, None)
        self._entries[key] = [value, self._clock() + self._ttl]
        while len(self._entries) > self._capacity:
            del self._entries[next(iter(self._entries))]

    def __len__(self):
        for key in [k for k, entry in self._entries.items() if self._clock() >= entry[1]]:
            del self._entries[key]
        return len(self._entries)
'''

TTL_CACHE_VISIBLE = '''import unittest

from solution import TTLCache


class FakeClock:
    def __init__(self):
        self.now = 0.0

    def __call__(self):
        return self.now


class TestTTLCache(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock()
        self.cache = TTLCache(capacity=2, ttl=10, clock=self.clock)

    def test_store_and_read(self):
        self.cache.set("a", 1)
        self.assertEqual(self.cache.get("a"), 1)

    def test_expiry(self):
        self.cache.set("a", 1)
        self.clock.now = 10
        self.assertIsNone(self.cache.get("a"))

    def test_default_when_absent(self):
        self.assertEqual(self.cache.get("missing", 7), 7)


if __name__ == "__main__":
    unittest.main()
'''

TTL_CACHE_HIDDEN = '''import unittest

from solution import TTLCache


class FakeClock:
    def __init__(self):
        self.now = 0.0

    def __call__(self):
        return self.now


class TestTTLCacheEdges(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock()
        self.cache = TTLCache(capacity=2, ttl=10, clock=self.clock)

    def test_capacity_evicts_least_recently_used(self):
        self.cache.set("a", 1)
        self.cache.set("b", 2)
        self.cache.get("a")
        self.cache.set("c", 3)
        self.assertEqual(self.cache.get("b"), None)
        self.assertEqual(self.cache.get("a"), 1)
        self.assertEqual(self.cache.get("c"), 3)

    def test_overwrite_resets_ttl(self):
        self.cache.set("a", 1)
        self.clock.now = 5
        self.cache.set("a", 2)
        self.clock.now = 12
        self.assertEqual(self.cache.get("a"), 2)

    def test_len_drops_expired(self):
        self.cache.set("a", 1)
        self.cache.set("b", 2)
        self.clock.now = 10
        self.assertEqual(len(self.cache), 0)

    def test_invalid_parameters(self):
        with self.assertRaises(ValueError):
            TTLCache(capacity=0, ttl=1, clock=self.clock)
        with self.assertRaises(ValueError):
            TTLCache(capacity=1, ttl=0, clock=self.clock)

    def test_value_none_is_readable(self):
        self.cache.set("a", None)
        self.assertIsNone(self.cache.get("a", "fallback"))


if __name__ == "__main__":
    unittest.main()
'''

TOKEN_BUCKET_SOLUTION = '''"""A token bucket rate limiter driven by an injected clock."""


class TokenBucket:
    def __init__(self, rate: float, capacity: int, clock):
        if rate <= 0:
            raise ValueError("rate must be positive")
        if capacity < 1:
            raise ValueError("capacity must be positive")
        self._rate = rate
        self._capacity = capacity
        self._clock = clock
        self._tokens = float(capacity)
        self._updated = clock()

    def _refill(self):
        now = self._clock()
        elapsed = now - self._updated
        if elapsed > 0:
            self._tokens = min(self._capacity, self._tokens + elapsed * self._rate)
            self._updated = now

    def allow(self, count: int = 1) -> bool:
        if count < 1:
            raise ValueError("count must be positive")
        self._refill()
        if self._tokens >= count:
            self._tokens -= count
            return True
        return False

    def tokens(self) -> float:
        self._refill()
        return self._tokens
'''

TOKEN_BUCKET_VISIBLE = '''import unittest

from solution import TokenBucket


class FakeClock:
    def __init__(self):
        self.now = 0.0

    def __call__(self):
        return self.now


class TestTokenBucket(unittest.TestCase):
    def test_burst_limited_by_capacity(self):
        bucket = TokenBucket(rate=1, capacity=3, clock=FakeClock())
        self.assertTrue(bucket.allow(3))
        self.assertFalse(bucket.allow(1))

    def test_refill_over_time(self):
        clock = FakeClock()
        bucket = TokenBucket(rate=2, capacity=2, clock=clock)
        self.assertTrue(bucket.allow(2))
        clock.now = 1.0
        self.assertTrue(bucket.allow(2))


if __name__ == "__main__":
    unittest.main()
'''

TOKEN_BUCKET_HIDDEN = '''import unittest

from solution import TokenBucket


class FakeClock:
    def __init__(self):
        self.now = 0.0

    def __call__(self):
        return self.now


class TestTokenBucketEdges(unittest.TestCase):
    def test_refill_capped_at_capacity(self):
        clock = FakeClock()
        bucket = TokenBucket(rate=10, capacity=2, clock=clock)
        bucket.allow(2)
        clock.now = 100.0
        self.assertEqual(bucket.tokens(), 2)

    def test_partial_refill(self):
        clock = FakeClock()
        bucket = TokenBucket(rate=1, capacity=4, clock=clock)
        bucket.allow(4)
        clock.now = 2.0
        self.assertTrue(bucket.allow(2))
        self.assertFalse(bucket.allow(1))

    def test_invalid_arguments(self):
        clock = FakeClock()
        with self.assertRaises(ValueError):
            TokenBucket(rate=0, capacity=1, clock=clock)
        with self.assertRaises(ValueError):
            TokenBucket(rate=1, capacity=0, clock=clock)
        with self.assertRaises(ValueError):
            TokenBucket(rate=1, capacity=1, clock=clock).allow(0)

    def test_clock_not_moving_does_not_refill(self):
        clock = FakeClock()
        bucket = TokenBucket(rate=5, capacity=1, clock=clock)
        bucket.allow(1)
        self.assertFalse(bucket.allow(1))


if __name__ == "__main__":
    unittest.main()
'''

CSV_SOLUTION = '''"""Split one CSV line into fields, honouring double-quoted values."""


def split_line(line: str) -> list:
    fields = []
    buffer = []
    quoted = False
    index = 0
    while index < len(line):
        character = line[index]
        if quoted:
            if character == '"':
                if index + 1 < len(line) and line[index + 1] == '"':
                    buffer.append('"')
                    index += 2
                    continue
                quoted = False
                index += 1
                continue
            buffer.append(character)
            index += 1
            continue
        if character == '"' and not buffer:
            quoted = True
            index += 1
            continue
        if character == ",":
            fields.append("".join(buffer))
            buffer = []
            index += 1
            continue
        buffer.append(character)
        index += 1
    if quoted:
        raise ValueError("unterminated quoted field")
    fields.append("".join(buffer))
    return fields
'''

CSV_VISIBLE = '''import unittest

from solution import split_line


class TestCsvSplit(unittest.TestCase):
    def test_plain(self):
        self.assertEqual(split_line("a,b,c"), ["a", "b", "c"])

    def test_quoted_comma(self):
        self.assertEqual(split_line('"a,b",c'), ["a,b", "c"])

    def test_empty_fields(self):
        self.assertEqual(split_line("a,,c"), ["a", "", "c"])


if __name__ == "__main__":
    unittest.main()
'''

CSV_HIDDEN = '''import unittest

from solution import split_line


class TestCsvSplitEdges(unittest.TestCase):
    def test_escaped_quote(self):
        self.assertEqual(split_line('"say ""hi""",x'), ['say "hi"', "x"])

    def test_single_empty_field(self):
        self.assertEqual(split_line(""), [""])

    def test_trailing_comma(self):
        self.assertEqual(split_line("a,"), ["a", ""])

    def test_unterminated_quote(self):
        with self.assertRaises(ValueError):
            split_line('"abc')

    def test_quotes_in_middle_of_field(self):
        self.assertEqual(split_line('a"b",c'), ['a"b"', "c"])


if __name__ == "__main__":
    unittest.main()
'''

SEMVER_SOLUTION = '''"""Compare dotted numeric version strings."""


def _parts(version: str) -> list:
    text = version.strip()
    if text[:1] in ("v", "V"):
        text = text[1:]
    if not text:
        raise ValueError("empty version")
    parts = text.split(".")
    numbers = []
    for part in parts:
        if not part.isdigit():
            raise ValueError("invalid version component: " + part)
        numbers.append(int(part))
    return numbers


def compare(left: str, right: str) -> int:
    a, b = _parts(left), _parts(right)
    for index in range(max(len(a), len(b))):
        first = a[index] if index < len(a) else 0
        second = b[index] if index < len(b) else 0
        if first != second:
            return -1 if first < second else 1
    return 0
'''

SEMVER_VISIBLE = '''import unittest

from solution import compare


class TestSemverCompare(unittest.TestCase):
    def test_less(self):
        self.assertEqual(compare("1.2.3", "1.2.4"), -1)

    def test_equal(self):
        self.assertEqual(compare("1.2.3", "1.2.3"), 0)

    def test_greater(self):
        self.assertEqual(compare("2.0.0", "1.9.9"), 1)


if __name__ == "__main__":
    unittest.main()
'''

SEMVER_HIDDEN = '''import unittest

from solution import compare


class TestSemverCompareEdges(unittest.TestCase):
    def test_leading_v_is_ignored(self):
        self.assertEqual(compare("v1.2.0", "1.2.0"), 0)

    def test_missing_components_are_zero(self):
        self.assertEqual(compare("1.2", "1.2.0"), 0)
        self.assertEqual(compare("1", "1.0.1"), -1)

    def test_numeric_not_lexical(self):
        self.assertEqual(compare("1.10.0", "1.9.0"), 1)

    def test_invalid_raises(self):
        with self.assertRaises(ValueError):
            compare("1.x", "1.0")
        with self.assertRaises(ValueError):
            compare("", "1.0")


if __name__ == "__main__":
    unittest.main()
'''

# --------------------------------------------------------------------------- #
# bug fixes (starter is real code with a defect)
# --------------------------------------------------------------------------- #

WINDOW_BUGGY = '''"""Sliding-window maximum over a list of numbers."""


def window_maxima(values: list, width: int) -> list:
    """Return the maximum of every contiguous window of the given width."""
    if width <= 0:
        raise ValueError("width must be positive")
    if width > len(values):
        return []
    result = []
    for start in range(len(values) - width):
        result.append(max(values[start:start + width]))
    return result
'''

WINDOW_FIXED = '''"""Sliding-window maximum over a list of numbers."""


def window_maxima(values: list, width: int) -> list:
    """Return the maximum of every contiguous window of the given width."""
    if width <= 0:
        raise ValueError("width must be positive")
    if width > len(values):
        return []
    result = []
    for start in range(len(values) - width + 1):
        result.append(max(values[start:start + width]))
    return result
'''

WINDOW_VISIBLE = '''import unittest

from solution import window_maxima


class TestWindowMaxima(unittest.TestCase):
    def test_short_input(self):
        self.assertEqual(window_maxima([1, 2, 3, 4], 2), [2, 3, 4])

    def test_last_window_is_reported(self):
        self.assertEqual(window_maxima([5, 1, 1, 9], 3), [5, 9])


if __name__ == "__main__":
    unittest.main()
'''

WINDOW_HIDDEN = '''import unittest

from solution import window_maxima


class TestWindowMaximaEdges(unittest.TestCase):
    def test_last_window_included(self):
        self.assertEqual(window_maxima([5, 1, 1, 9], 3), [5, 9])

    def test_exact_width(self):
        self.assertEqual(window_maxima([4, 2, 7], 3), [7])

    def test_width_larger_than_input(self):
        self.assertEqual(window_maxima([1, 2], 5), [])

    def test_invalid_width(self):
        with self.assertRaises(ValueError):
            window_maxima([1], 0)


if __name__ == "__main__":
    unittest.main()
'''

MUTABLE_DEFAULT_BUGGY = '''"""Append helpers that accumulate state across calls."""


def collect(items, target=None):
    """Append the items that are not already present and return the target list."""
    if target is None:
        target = []
    for item in items:
        if item not in target:
            target.append(item)
    return target
'''

MUTABLE_DEFAULT_FIXED = '''"""Append helpers that accumulate state across calls."""


def collect(items, target=None):
    """Append the items that are not already present and return the target list."""
    if target is None:
        target = []
    for item in items:
        if item not in target:
            target.append(item)
    return target
'''

MUTABLE_DEFAULT_BUGGY_V2 = '''"""Append helpers that accumulate state across calls."""


def collect(items, target=[]):
    """Append the items that are not already present and return the target list."""
    for item in items:
        if item not in target:
            target.append(item)
    return target
'''

MUTABLE_DEFAULT_VISIBLE = '''import unittest

from solution import collect


class TestCollect(unittest.TestCase):
    def test_first_call(self):
        self.assertEqual(collect([1, 2, 1]), [1, 2])

    def test_default_is_not_shared_between_calls(self):
        first = collect([1])
        second = collect([2])
        self.assertEqual(first, [1])
        self.assertEqual(second, [2])


if __name__ == "__main__":
    unittest.main()
'''

MUTABLE_DEFAULT_HIDDEN = '''import unittest

from solution import collect


class TestCollectEdges(unittest.TestCase):
    def test_output_is_not_mutated_after_return(self):
        result = collect([1, 2])
        collect([3])
        self.assertEqual(result, [1, 2])

    def test_explicit_target_is_filled_in_place(self):
        base = [9]
        collect([1, 9], base)
        self.assertEqual(base, [9, 1])

    def test_duplicates_removed_preserving_order(self):
        self.assertEqual(collect([3, 1, 3, 2, 1]), [3, 1, 2])

    def test_many_sequential_calls_stay_independent(self):
        self.assertEqual([collect([n]) for n in range(3)], [[0], [1], [2]])


if __name__ == "__main__":
    unittest.main()
'''

SORT_KEY_BUGGY = '''"""Group rows by a numeric column, preserving input order inside a group."""


def group_by_amount(rows: list) -> dict:
    """Return amount -> list of row names, in first-seen group order."""
    groups = {}
    for name, amount in rows:
        groups.setdefault(amount, []).append(name)
    return groups
'''

SORT_KEY_FIXED = '''"""Group rows by a numeric column, preserving input order inside a group."""


def group_by_amount(rows: list) -> dict:
    """Return amount -> list of row names, in first-seen group order."""
    groups = {}
    for name, amount in rows:
        groups.setdefault(amount, []).append(name)
    return groups
'''

SORT_KEY_BUGGY_V2 = '''"""Group rows by a numeric column, preserving input order inside a group."""


def group_by_amount(rows: list) -> dict:
    """Return amount -> list of row names, in first-seen group order."""
    groups = {}
    for name, amount in rows:
        groups[amount] = [name]
    return groups
'''

SORT_KEY_VISIBLE = '''import unittest

from solution import group_by_amount


class TestGroupByAmount(unittest.TestCase):
    def test_one_group_keeps_all_rows(self):
        self.assertEqual(group_by_amount([("a", 1), ("b", 1)]), {1: ["a", "b"]})

    def test_distinct_groups(self):
        self.assertEqual(group_by_amount([("a", 1), ("b", 2)]), {1: ["a"], 2: ["b"]})


if __name__ == "__main__":
    unittest.main()
'''

SORT_KEY_HIDDEN = '''import unittest

from solution import group_by_amount


class TestGroupByAmountEdges(unittest.TestCase):
    def test_three_rows_in_one_group(self):
        rows = [("a", 5), ("b", 5), ("c", 5)]
        self.assertEqual(group_by_amount(rows), {5: ["a", "b", "c"]})

    def test_interleaved_groups(self):
        rows = [("a", 1), ("b", 2), ("c", 1), ("d", 2)]
        self.assertEqual(group_by_amount(rows), {1: ["a", "c"], 2: ["b", "d"]})

    def test_empty_input(self):
        self.assertEqual(group_by_amount([]), {})

    def test_negative_amounts(self):
        self.assertEqual(group_by_amount([("a", -1), ("b", -1)]), {-1: ["a", "b"]})


if __name__ == "__main__":
    unittest.main()
'''

FLOAT_EQ_BUGGY = '''"""Compare floating point values within an absolute tolerance."""


def nearly_equal(left: float, right: float, tolerance: float = 1e-9) -> bool:
    """Return True when the two values differ by no more than the tolerance."""
    return abs(left - right) < tolerance
'''

FLOAT_EQ_FIXED = '''"""Compare floating point values within an absolute tolerance."""


def nearly_equal(left: float, right: float, tolerance: float = 1e-9) -> bool:
    """Return True when the two values differ by no more than the tolerance."""
    return abs(left - right) <= tolerance
'''

FLOAT_EQ_VISIBLE = '''import unittest

from solution import nearly_equal


class TestNearlyEqual(unittest.TestCase):
    def test_close_values(self):
        self.assertTrue(nearly_equal(0.1 + 0.2, 0.3, tolerance=1e-9))

    def test_exact_tolerance_boundary_is_equal(self):
        self.assertTrue(nearly_equal(1.0, 1.5, tolerance=0.5))


if __name__ == "__main__":
    unittest.main()
'''

FLOAT_EQ_HIDDEN = '''import unittest

from solution import nearly_equal


class TestNearlyEqualEdges(unittest.TestCase):
    def test_just_outside_tolerance(self):
        self.assertFalse(nearly_equal(1.0, 1.5000001, tolerance=0.5))

    def test_zero_tolerance_accepts_identical_values(self):
        self.assertTrue(nearly_equal(3.3, 3.3, tolerance=0))

    def test_negative_values(self):
        self.assertTrue(nearly_equal(-1.0, -1.4, tolerance=0.5))

    def test_large_values(self):
        self.assertFalse(nearly_equal(1000.0, 1000.5, tolerance=0.1))


if __name__ == "__main__":
    unittest.main()
'''


def tasks() -> list:
    """Return every Python task in the lite set."""
    entries = [
        py_task(
            "py-algo-run-length", ALGO,
            "Implement run-length encoding in solution.py. `encode(text)` returns a list of "
            "[character, count] pairs in order, and returns [] for empty input. "
            "`decode(pairs)` rebuilds the original string. Keep both signatures unchanged.",
            stub_of(RUN_LENGTH_SOLUTION), RUN_LENGTH_SOLUTION, RUN_LENGTH_VISIBLE, RUN_LENGTH_HIDDEN,
            ["encode folds consecutive equal characters into [char, count] pairs",
             "decode inverts encode exactly, including counts above nine and non-ASCII text",
             "empty input returns an empty list"],
            ["algorithm", "string"],
        ),
        py_task(
            "py-algo-merge-intervals", ALGO,
            "Implement `merge(intervals)` in solution.py: given closed integer intervals as "
            "[start, end] pairs in any order, return the merged, sorted list. Overlapping and "
            "touching intervals merge; the input must not be modified.",
            stub_of(MERGE_INTERVALS_SOLUTION), MERGE_INTERVALS_SOLUTION,
            MERGE_INTERVALS_VISIBLE, MERGE_INTERVALS_HIDDEN,
            ["overlapping and touching intervals merge into one",
             "output is sorted by start and the caller's input list is left unchanged",
             "contained intervals are absorbed"],
            ["algorithm", "intervals"],
        ),
        py_task(
            "py-algo-toposort", ALGO,
            "Implement `topological_order(nodes, edges)` in solution.py. Return a list ordering "
            "every node so that each edge (source, target) puts source first; break ties by "
            "choosing the alphabetically smallest available node. Return None when the graph has "
            "a cycle. Raise ValueError when an edge names an unknown node.",
            stub_of(TOPO_SOLUTION), TOPO_SOLUTION, TOPO_VISIBLE, TOPO_HIDDEN,
            ["every edge is respected in the returned order",
             "ties are broken by node name",
             "cycles return None; unknown nodes raise ValueError"],
            ["algorithm", "graph"],
        ),
        py_task(
            "py-algo-max-subarray", ALGO,
            "Implement `max_subarray_sum(values)` in solution.py using a single linear pass: "
            "return the largest sum of a non-empty contiguous subarray. Raise ValueError for "
            "empty input.",
            stub_of(MAX_SUBARRAY_SOLUTION), MAX_SUBARRAY_SOLUTION, MAX_SUBARRAY_VISIBLE, MAX_SUBARRAY_HIDDEN,
            ["the maximum is over non-empty contiguous subarrays only",
             "all-negative input returns the largest single value",
             "empty input raises ValueError"],
            ["algorithm", "dynamic-programming"],
        ),
        py_task(
            "py-algo-rotated-search", ALGO,
            "Implement `search(values, target)` in solution.py: values is a sorted list of "
            "distinct integers rotated by an unknown amount. Return the index of target, or -1 "
            "when absent, in logarithmic time.",
            stub_of(ROTATED_SEARCH_SOLUTION), ROTATED_SEARCH_SOLUTION,
            ROTATED_SEARCH_VISIBLE, ROTATED_SEARCH_HIDDEN,
            ["the function works on rotated and unrotated input",
             "absent targets return -1",
             "the search stays logarithmic"],
            ["algorithm", "binary-search"],
        ),
        py_task(
            "py-algo-balanced-parens", ALGO,
            "Implement `is_balanced(text)` in solution.py. text contains only '(', ')' and '*'; "
            "'*' may count as '(' , ')' or as nothing. Return True when some choice of the "
            "wildcards yields balanced parentheses. Raise ValueError for any other character.",
            stub_of(PARENS_SOLUTION), PARENS_SOLUTION, PARENS_VISIBLE, PARENS_HIDDEN,
            ["every wildcard assignment is considered, not just one",
             "an early unmatched ')' is rejected",
             "unsupported characters raise ValueError"],
            ["algorithm", "greedy"],
        ),
        py_task(
            "py-mod-ini-parser", MODULE,
            "Implement `parse_ini(text)` in solution.py: parse a small INI-style configuration "
            "into {section: {key: value}}. Section headers are [name]; keys before any header "
            "belong to section \"\". Trim whitespace around keys and values, ignore blank lines "
            "and lines starting with '#' or ';', keep '=' inside values, let a repeated key win "
            "with its last value, and raise ValueError for a non-empty line that is neither a "
            "comment, a header nor a key=value pair.",
            stub_of(INI_SOLUTION), INI_SOLUTION, INI_VISIBLE, INI_HIDDEN,
            ["sectionless keys land in the \"\" section",
             "only the first '=' splits key and value",
             "malformed lines raise ValueError"],
            ["module", "parser"],
        ),
        py_task(
            "py-mod-ttl-cache", MODULE,
            "Implement `TTLCache(capacity, ttl, clock)` in solution.py. set(key, value) stores a "
            "value that expires ttl seconds later (per the injected clock); get(key, default) "
            "returns it, or default when missing or expired. When a set exceeds capacity, evict "
            "the least recently used entry, counting both get and set as use. len() counts only "
            "live entries. Raise ValueError for a non-positive capacity or ttl.",
            stub_of(TTL_CACHE_SOLUTION), TTL_CACHE_SOLUTION, TTL_CACHE_VISIBLE, TTL_CACHE_HIDDEN,
            ["expired entries read as missing and are dropped",
             "capacity eviction is least-recently-used and reads refresh recency",
             "re-setting a key refreshes its expiry"],
            ["module", "cache"],
        ),
        py_task(
            "py-mod-token-bucket", MODULE,
            "Implement `TokenBucket(rate, capacity, clock)` in solution.py. The bucket starts "
            "full and refills at `rate` tokens per second according to the injected clock, never "
            "exceeding `capacity`. allow(count) removes count tokens and returns True when "
            "enough are available, otherwise leaves the bucket untouched and returns False. "
            "tokens() reports the current level. Raise ValueError for a non-positive rate or "
            "capacity, or a count below one.",
            stub_of(TOKEN_BUCKET_SOLUTION), TOKEN_BUCKET_SOLUTION,
            TOKEN_BUCKET_VISIBLE, TOKEN_BUCKET_HIDDEN,
            ["refill is proportional to elapsed time and capped at capacity",
             "a refused request consumes nothing",
             "invalid parameters raise ValueError"],
            ["module", "rate-limiting"],
        ),
        py_task(
            "py-mod-csv-splitter", MODULE,
            "Implement `split_line(line)` in solution.py: split one CSV record into fields. "
            "Commas separate fields, a double-quoted field may contain commas and doubled quotes "
            "(\"\" meaning one literal quote), and an empty field yields \"\". Raise ValueError "
            "when a quoted field is never closed.",
            stub_of(CSV_SOLUTION), CSV_SOLUTION, CSV_VISIBLE, CSV_HIDDEN,
            ["quoted fields keep commas and unescape doubled quotes",
             "empty and trailing fields are preserved",
             "an unterminated quote raises ValueError"],
            ["module", "parser"],
        ),
        py_task(
            "py-mod-semver-compare", MODULE,
            "Implement `compare(left, right)` in solution.py: compare two dotted numeric version "
            "strings and return -1, 0 or 1. A leading 'v' or 'V' is ignored, missing trailing "
            "components count as zero, and components compare as integers rather than strings. "
            "Raise ValueError for an empty string or a non-numeric component.",
            stub_of(SEMVER_SOLUTION), SEMVER_SOLUTION, SEMVER_VISIBLE, SEMVER_HIDDEN,
            ["missing components are treated as zero",
             "comparison is numeric, not lexical",
             "invalid input raises ValueError"],
            ["module", "comparison"],
        ),
        py_task(
            "py-bug-window-maxima", BUGFIX,
            "solution.py computes the maximum of every sliding window but drops the final "
            "window. Fix `window_maxima` in solution.py so it returns one maximum per window, "
            "including the last. Run test_visible.py to see the failure.",
            WINDOW_BUGGY, WINDOW_FIXED, WINDOW_VISIBLE, WINDOW_HIDDEN,
            ["every window, including the last, contributes one maximum",
             "the width validation behaviour is preserved"],
            ["bugfix", "off-by-one"],
        ),
        py_task(
            "py-bug-mutable-default", BUGFIX,
            "solution.py keeps state between unrelated calls because a mutable default is "
            "shared. Fix `collect` in solution.py so results no longer leak between calls, "
            "while still appending into an explicitly supplied target list.",
            MUTABLE_DEFAULT_BUGGY_V2, MUTABLE_DEFAULT_FIXED,
            MUTABLE_DEFAULT_VISIBLE, MUTABLE_DEFAULT_HIDDEN,
            ["no state leaks between calls that use the default",
             "an explicit target list is still filled in place"],
            ["bugfix", "python-semantics"],
        ),
        py_task(
            "py-bug-group-by", BUGFIX,
            "solution.py groups rows by amount but keeps only the last row of each group. Fix "
            "`group_by_amount` in solution.py so every row is kept, preserving first-seen group "
            "order and input order within a group.",
            SORT_KEY_BUGGY_V2, SORT_KEY_FIXED, SORT_KEY_VISIBLE, SORT_KEY_HIDDEN,
            ["every row of a group is retained in input order",
             "the result is keyed by amount"],
            ["bugfix", "aggregation"],
        ),
        py_task(
            "py-bug-float-tolerance", BUGFIX,
            "solution.py compares floats with a strict inequality, so a difference exactly equal "
            "to the tolerance is reported as different. Fix `nearly_equal` in solution.py so the "
            "boundary counts as equal.",
            FLOAT_EQ_BUGGY, FLOAT_EQ_FIXED, FLOAT_EQ_VISIBLE, FLOAT_EQ_HIDDEN,
            ["a difference of exactly the tolerance is equal",
             "a difference just above the tolerance is not equal"],
            ["bugfix", "floating-point"],
        ),
    ]
    return entries
