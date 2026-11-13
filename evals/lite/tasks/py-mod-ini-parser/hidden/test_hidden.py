import unittest

from solution import parse_ini


class TestIniParserEdges(unittest.TestCase):
    def test_value_keeps_equals(self):
        self.assertEqual(parse_ini("[s]\nurl = a=b\n"), {"s": {"url": "a=b"}})

    def test_duplicate_key_last_wins(self):
        self.assertEqual(parse_ini("[s]\nk=1\nk=2\n"), {"s": {"k": "2"}})

    def test_empty_value(self):
        self.assertEqual(parse_ini("[s]\nk =\n"), {"s": {"k": ""}})

    def test_malformed_line_raises(self):
        with self.assertRaises(ValueError):
            parse_ini("[s]\nnot a pair\n")

    def test_empty_section_repeated(self):
        self.assertEqual(parse_ini("[s]\n[s]\nk=v\n"), {"s": {"k": "v"}})


if __name__ == "__main__":
    unittest.main()
