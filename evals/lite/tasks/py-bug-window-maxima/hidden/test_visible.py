import unittest

from solution import window_maxima


class TestWindowMaxima(unittest.TestCase):
    def test_short_input(self):
        self.assertEqual(window_maxima([1, 2, 3, 4], 2), [2, 3, 4])

    def test_last_window_is_reported(self):
        self.assertEqual(window_maxima([5, 1, 1, 9], 3), [5, 9])


if __name__ == "__main__":
    unittest.main()
