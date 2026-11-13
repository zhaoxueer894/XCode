import unittest

from solution import topological_order


class TestTopologicalOrderEdges(unittest.TestCase):
    def test_cycle_returns_none(self):
        self.assertIsNone(topological_order(["a", "b"], [("a", "b"), ("b", "a")]))

    def test_diamond_respects_all_dependencies(self):
        order = topological_order(["d", "b", "c", "a"], [("a", "b"), ("a", "c"), ("b", "d"), ("c", "d")])
        self.assertEqual(order[0], "a")
        self.assertEqual(order[-1], "d")
        self.assertLess(order.index("b"), order.index("d"))
        self.assertLess(order.index("c"), order.index("d"))

    def test_unknown_node_raises(self):
        with self.assertRaises(ValueError):
            topological_order(["a"], [("a", "z")])

    def test_self_loop_is_a_cycle(self):
        self.assertIsNone(topological_order(["a"], [("a", "a")]))


if __name__ == "__main__":
    unittest.main()
