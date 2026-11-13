"""Split one CSV line into fields, honouring double-quoted values."""


def split_line(line: str) -> list:
    fields = []
    buffer = []
    quoted = False
    index = 0
    while index < len(line):
        character = line[index]
        if quoted:
            if character == '"':
                if index + 1 < len(line) and line[index + 1] == '"':
                    buffer.append('"')
                    index += 2
                    continue
                quoted = False
                index += 1
                continue
            buffer.append(character)
            index += 1
            continue
        if character == '"' and not buffer:
            quoted = True
            index += 1
            continue
        if character == ",":
            fields.append("".join(buffer))
            buffer = []
            index += 1
            continue
        buffer.append(character)
        index += 1
    if quoted:
        raise ValueError("unterminated quoted field")
    fields.append("".join(buffer))
    return fields
