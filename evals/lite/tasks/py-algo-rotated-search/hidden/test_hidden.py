import unittest

from solution import search


class TestRotatedSearchEdges(unittest.TestCase):
    def test_not_rotated(self):
        self.assertEqual(search([1, 2, 3, 4, 5], 3), 2)

    def test_empty(self):
        self.assertEqual(search([], 9), -1)

    def test_pivot_element(self):
        self.assertEqual(search([6, 7, 1, 2, 3], 6), 0)
        self.assertEqual(search([6, 7, 1, 2, 3], 3), 4)

    def test_single_missing(self):
        self.assertEqual(search([1], 0), -1)

    def test_two_elements_rotated(self):
        self.assertEqual(search([2, 1], 1), 1)


if __name__ == "__main__":
    unittest.main()
