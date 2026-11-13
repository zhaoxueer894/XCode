import unittest

from solution import collect


class TestCollect(unittest.TestCase):
    def test_first_call(self):
        self.assertEqual(collect([1, 2, 1]), [1, 2])

    def test_default_is_not_shared_between_calls(self):
        first = collect([1])
        second = collect([2])
        self.assertEqual(first, [1])
        self.assertEqual(second, [2])


if __name__ == "__main__":
    unittest.main()
