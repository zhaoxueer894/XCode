"""Append helpers that accumulate state across calls."""


def collect(items, target=[]):
    """Append the items that are not already present and return the target list."""
    for item in items:
        if item not in target:
            target.append(item)
    return target
