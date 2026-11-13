"""Sliding-window maximum over a list of numbers."""


def window_maxima(values: list, width: int) -> list:
    """Return the maximum of every contiguous window of the given width."""
    if width <= 0:
        raise ValueError("width must be positive")
    if width > len(values):
        return []
    result = []
    for start in range(len(values) - width + 1):
        result.append(max(values[start:start + width]))
    return result
