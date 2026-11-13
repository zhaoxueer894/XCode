"""Compare floating point values within an absolute tolerance."""


def nearly_equal(left: float, right: float, tolerance: float = 1e-9) -> bool:
    """Return True when the two values differ by no more than the tolerance."""
    return abs(left - right) < tolerance
