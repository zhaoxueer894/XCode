import unittest

from solution import group_by_amount


class TestGroupByAmount(unittest.TestCase):
    def test_one_group_keeps_all_rows(self):
        self.assertEqual(group_by_amount([("a", 1), ("b", 1)]), {1: ["a", "b"]})

    def test_distinct_groups(self):
        self.assertEqual(group_by_amount([("a", 1), ("b", 2)]), {1: ["a"], 2: ["b"]})


if __name__ == "__main__":
    unittest.main()
