import unittest

from solution import is_balanced


class TestParens(unittest.TestCase):
    def test_plain(self):
        self.assertTrue(is_balanced("(())"))
        self.assertFalse(is_balanced("(()"))

    def test_star(self):
        self.assertTrue(is_balanced("(*)"))

    def test_empty(self):
        self.assertTrue(is_balanced(""))


if __name__ == "__main__":
    unittest.main()
