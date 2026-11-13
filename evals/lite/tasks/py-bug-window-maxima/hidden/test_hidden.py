import unittest

from solution import window_maxima


class TestWindowMaximaEdges(unittest.TestCase):
    def test_last_window_included(self):
        self.assertEqual(window_maxima([5, 1, 1, 9], 3), [5, 9])

    def test_exact_width(self):
        self.assertEqual(window_maxima([4, 2, 7], 3), [7])

    def test_width_larger_than_input(self):
        self.assertEqual(window_maxima([1, 2], 5), [])

    def test_invalid_width(self):
        with self.assertRaises(ValueError):
            window_maxima([1], 0)


if __name__ == "__main__":
    unittest.main()
