import unittest

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
