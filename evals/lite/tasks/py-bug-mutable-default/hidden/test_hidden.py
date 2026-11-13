import unittest

from solution import collect


class TestCollectEdges(unittest.TestCase):
    def test_output_is_not_mutated_after_return(self):
        result = collect([1, 2])
        collect([3])
        self.assertEqual(result, [1, 2])

    def test_explicit_target_is_filled_in_place(self):
        base = [9]
        collect([1, 9], base)
        self.assertEqual(base, [9, 1])

    def test_duplicates_removed_preserving_order(self):
        self.assertEqual(collect([3, 1, 3, 2, 1]), [3, 1, 2])

    def test_many_sequential_calls_stay_independent(self):
        self.assertEqual([collect([n]) for n in range(3)], [[0], [1], [2]])


if __name__ == "__main__":
    unittest.main()
