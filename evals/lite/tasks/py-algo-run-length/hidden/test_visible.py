import unittest

from solution import decode, encode


class TestRunLength(unittest.TestCase):
    def test_single_character(self):
        self.assertEqual(encode("a"), [["a", 1]])

    def test_runs(self):
        self.assertEqual(encode("aaabbc"), [["a", 3], ["b", 2], ["c", 1]])

    def test_empty(self):
        self.assertEqual(encode(""), [])

    def test_decode(self):
        self.assertEqual(decode([["a", 3], ["b", 2]]), "aaabb")


if __name__ == "__main__":
    unittest.main()
