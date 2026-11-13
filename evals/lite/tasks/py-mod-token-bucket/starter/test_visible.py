import unittest

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
