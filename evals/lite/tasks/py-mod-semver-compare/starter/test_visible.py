import unittest

from solution import compare


class TestSemverCompare(unittest.TestCase):
    def test_less(self):
        self.assertEqual(compare("1.2.3", "1.2.4"), -1)

    def test_equal(self):
        self.assertEqual(compare("1.2.3", "1.2.3"), 0)

    def test_greater(self):
        self.assertEqual(compare("2.0.0", "1.9.9"), 1)


if __name__ == "__main__":
    unittest.main()
