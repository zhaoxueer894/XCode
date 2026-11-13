"""Group rows by a numeric column, preserving input order inside a group."""


def group_by_amount(rows: list) -> dict:
    """Return amount -> list of row names, in first-seen group order."""
    groups = {}
    for name, amount in rows:
        groups[amount] = [name]
    return groups
