import unittest

from solution import split_line


class TestCsvSplit(unittest.TestCase):
    def test_plain(self):
        self.assertEqual(split_line("a,b,c"), ["a", "b", "c"])

    def test_quoted_comma(self):
        self.assertEqual(split_line('"a,b",c'), ["a,b", "c"])

    def test_empty_fields(self):
        self.assertEqual(split_line("a,,c"), ["a", "", "c"])


if __name__ == "__main__":
    unittest.main()
