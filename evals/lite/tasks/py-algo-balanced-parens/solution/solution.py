"""Balanced parentheses where '*' may stand for '(' , ')' or nothing."""


def is_balanced(text: str) -> bool:
    low = high = 0
    for character in text:
        if character == "(":
            low += 1
            high += 1
        elif character == ")":
            low = max(low - 1, 0)
            high -= 1
            if high < 0:
                return False
        elif character == "*":
            low = max(low - 1, 0)
            high += 1
        else:
            raise ValueError("unsupported character: " + character)
    return low == 0
