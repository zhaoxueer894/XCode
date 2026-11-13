"""A small INI-style configuration reader."""


def parse_ini(text: str) -> dict:
    sections = {}
    current = ""
    for number, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#") or line.startswith(";"):
            continue
        if line.startswith("[") and line.endswith("]"):
            current = line[1:-1].strip()
            sections.setdefault(current, {})
            continue
        if "=" not in line:
            raise ValueError(f"line {number}: expected key=value")
        key, _, value = line.partition("=")
        sections.setdefault(current, {})[key.strip()] = value.strip()
    return sections
