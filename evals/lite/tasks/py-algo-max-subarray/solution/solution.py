"""Kadane's algorithm: maximum sum of a non-empty contiguous subarray."""


def max_subarray_sum(values: list) -> int:
    if not values:
        raise ValueError("values must not be empty")
    best = running = values[0]
    for value in values[1:]:
        running = value if running < 0 else running + value
        if running > best:
            best = running
    return best
