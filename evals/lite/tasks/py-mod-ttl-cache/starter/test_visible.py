import unittest

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
