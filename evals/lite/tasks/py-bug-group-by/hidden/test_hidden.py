import unittest

from solution import group_by_amount


class TestGroupByAmountEdges(unittest.TestCase):
    def test_three_rows_in_one_group(self):
        rows = [("a", 5), ("b", 5), ("c", 5)]
        self.assertEqual(group_by_amount(rows), {5: ["a", "b", "c"]})

    def test_interleaved_groups(self):
        rows = [("a", 1), ("b", 2), ("c", 1), ("d", 2)]
        self.assertEqual(group_by_amount(rows), {1: ["a", "c"], 2: ["b", "d"]})

    def test_empty_input(self):
        self.assertEqual(group_by_amount([]), {})

    def test_negative_amounts(self):
        self.assertEqual(group_by_amount([("a", -1), ("b", -1)]), {-1: ["a", "b"]})


if __name__ == "__main__":
    unittest.main()
