import unittest

from solution import search


class TestRotatedSearch(unittest.TestCase):
    def test_found_right_half(self):
        self.assertEqual(search([4, 5, 6, 7, 0, 1, 2], 0), 4)

    def test_not_found(self):
        self.assertEqual(search([4, 5, 6, 7, 0, 1, 2], 3), -1)

    def test_single(self):
        self.assertEqual(search([1], 1), 0)


if __name__ == "__main__":
    unittest.main()
