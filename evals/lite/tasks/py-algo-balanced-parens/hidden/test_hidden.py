import unittest

from solution import is_balanced


class TestParensEdges(unittest.TestCase):
    def test_too_many_closing(self):
        self.assertFalse(is_balanced("(*)))"))

    def test_only_stars(self):
        self.assertTrue(is_balanced("***"))

    def test_unmatched_close_early(self):
        self.assertFalse(is_balanced(")("))

    def test_unsupported_character(self):
        with self.assertRaises(ValueError):
            is_balanced("(a)")

    def test_long_mixed(self):
        self.assertTrue(is_balanced("(*)(*)(*)"))
        self.assertFalse(is_balanced("(((*("))


if __name__ == "__main__":
    unittest.main()
