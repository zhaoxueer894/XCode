import unittest

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
