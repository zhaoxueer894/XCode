import unittest

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
