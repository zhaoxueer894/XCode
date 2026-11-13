"""Append helpers that accumulate state across calls."""


def collect(items, target=None):
    """Append the items that are not already present and return the target list."""
    if target is None:
        target = []
    for item in items:
        if item not in target:
            target.append(item)
    return target
