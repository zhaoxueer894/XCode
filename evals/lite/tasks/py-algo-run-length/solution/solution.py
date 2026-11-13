"""Run-length encoding over characters."""


def encode(text: str) -> list[list]:
    if not text:
        return []
    result = []
    current = text[0]
    count = 1
    for character in text[1:]:
        if character == current:
            count += 1
            continue
        result.append([current, count])
        current = character
        count = 1
    result.append([current, count])
    return result


def decode(pairs: list[list]) -> str:
    return "".join(character * count for character, count in pairs)
