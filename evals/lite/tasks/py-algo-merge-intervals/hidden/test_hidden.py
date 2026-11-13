import unittest

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
