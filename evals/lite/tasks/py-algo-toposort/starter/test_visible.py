import unittest

from solution import topological_order


class TestTopologicalOrder(unittest.TestCase):
    def test_chain(self):
        self.assertEqual(topological_order(["a", "b", "c"], [("a", "b"), ("b", "c")]), ["a", "b", "c"])

    def test_ties_break_by_name(self):
        self.assertEqual(topological_order(["b", "a"], []), ["a", "b"])

    def test_empty(self):
        self.assertEqual(topological_order([], []), [])


if __name__ == "__main__":
    unittest.main()
