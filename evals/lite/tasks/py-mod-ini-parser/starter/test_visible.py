import unittest

from solution import parse_ini


class TestIniParser(unittest.TestCase):
    def test_sections(self):
        text = "[a]\nx = 1\n[b]\ny = 2\n"
        self.assertEqual(parse_ini(text), {"a": {"x": "1"}, "b": {"y": "2"}})

    def test_global_keys(self):
        self.assertEqual(parse_ini("top = 1\n[s]\nk = v\n"), {"": {"top": "1"}, "s": {"k": "v"}})

    def test_comments_and_blanks(self):
        self.assertEqual(parse_ini("# c\n\n; d\n[s]\nk=v\n"), {"s": {"k": "v"}})


if __name__ == "__main__":
    unittest.main()
