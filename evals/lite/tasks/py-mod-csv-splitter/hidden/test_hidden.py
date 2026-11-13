import unittest

from solution import split_line


class TestCsvSplitEdges(unittest.TestCase):
    def test_escaped_quote(self):
        self.assertEqual(split_line('"say ""hi""",x'), ['say "hi"', "x"])

    def test_single_empty_field(self):
        self.assertEqual(split_line(""), [""])

    def test_trailing_comma(self):
        self.assertEqual(split_line("a,"), ["a", ""])

    def test_unterminated_quote(self):
        with self.assertRaises(ValueError):
            split_line('"abc')

    def test_quotes_in_middle_of_field(self):
        self.assertEqual(split_line('a"b",c'), ['a"b"', "c"])


if __name__ == "__main__":
    unittest.main()
