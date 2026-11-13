import unittest

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
