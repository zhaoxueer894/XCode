import unittest

from solution import nearly_equal


class TestNearlyEqual(unittest.TestCase):
    def test_close_values(self):
        self.assertTrue(nearly_equal(0.1 + 0.2, 0.3, tolerance=1e-9))

    def test_exact_tolerance_boundary_is_equal(self):
        self.assertTrue(nearly_equal(1.0, 1.5, tolerance=0.5))


if __name__ == "__main__":
    unittest.main()
