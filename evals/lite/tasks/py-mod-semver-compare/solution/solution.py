"""Compare dotted numeric version strings."""


def _parts(version: str) -> list:
    text = version.strip()
    if text[:1] in ("v", "V"):
        text = text[1:]
    if not text:
        raise ValueError("empty version")
    parts = text.split(".")
    numbers = []
    for part in parts:
        if not part.isdigit():
            raise ValueError("invalid version component: " + part)
        numbers.append(int(part))
    return numbers


def compare(left: str, right: str) -> int:
    a, b = _parts(left), _parts(right)
    for index in range(max(len(a), len(b))):
        first = a[index] if index < len(a) else 0
        second = b[index] if index < len(b) else 0
        if first != second:
            return -1 if first < second else 1
    return 0
