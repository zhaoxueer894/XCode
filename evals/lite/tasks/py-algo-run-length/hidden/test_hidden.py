import unittest

from solution import decode, encode


class TestRunLengthEdges(unittest.TestCase):
    def test_round_trip_long(self):
        text = ("abcd" * 25) + ("z" * 10)
        self.assertEqual(decode(encode(text)), text)

    def test_run_longer_than_nine(self):
        self.assertEqual(encode("a" * 12), [["a", 12]])
        self.assertEqual(decode([["a", 12]]), "a" * 12)

    def test_non_ascii(self):
        self.assertEqual(encode("\u4f60\u4f60\u597d"), [["\u4f60", 2], ["\u597d", 1]])
        self.assertEqual(decode(encode("\u4f60\u597d\u597d")), "\u4f60\u597d\u597d")

    def test_decode_empty(self):
        self.assertEqual(decode([]), "")


if __name__ == "__main__":
    unittest.main()
