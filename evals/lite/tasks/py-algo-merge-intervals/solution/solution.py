"""Merge overlapping closed integer intervals."""


def merge(intervals: list[list]) -> list[list]:
    if not intervals:
        return []
    ordered = sorted([list(pair) for pair in intervals], key=lambda pair: (pair[0], pair[1]))
    merged = [ordered[0]]
    for start, end in ordered[1:]:
        if start <= merged[-1][1]:
            if end > merged[-1][1]:
                merged[-1][1] = end
            continue
        merged.append([start, end])
    return merged
