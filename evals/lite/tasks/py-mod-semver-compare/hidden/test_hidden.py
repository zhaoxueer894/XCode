import unittest

from solution import compare


class TestSemverCompareEdges(unittest.TestCase):
    def test_leading_v_is_ignored(self):
        self.assertEqual(compare("v1.2.0", "1.2.0"), 0)

    def test_missing_components_are_zero(self):
        self.assertEqual(compare("1.2", "1.2.0"), 0)
        self.assertEqual(compare("1", "1.0.1"), -1)

    def test_numeric_not_lexical(self):
        self.assertEqual(compare("1.10.0", "1.9.0"), 1)

    def test_invalid_raises(self):
        with self.assertRaises(ValueError):
            compare("1.x", "1.0")
        with self.assertRaises(ValueError):
            compare("", "1.0")


if __name__ == "__main__":
    unittest.main()
