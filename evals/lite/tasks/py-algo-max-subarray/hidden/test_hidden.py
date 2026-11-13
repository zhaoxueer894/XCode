import unittest

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
