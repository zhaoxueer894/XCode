import unittest

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
