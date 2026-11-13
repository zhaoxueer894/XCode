"""Deterministic topological order for a directed acyclic graph."""


def topological_order(nodes: list, edges: list):
    incoming = {node: 0 for node in nodes}
    outgoing = {node: [] for node in nodes}
    for source, target in edges:
        if source not in incoming or target not in incoming:
            raise ValueError("edge references an unknown node")
        outgoing[source].append(target)
        incoming[target] += 1
    ready = sorted(node for node, count in incoming.items() if count == 0)
    order = []
    while ready:
        node = ready.pop(0)
        order.append(node)
        for target in sorted(outgoing[node]):
            incoming[target] -= 1
            if incoming[target] == 0:
                ready.append(target)
        ready.sort()
    if len(order) != len(nodes):
        return None
    return order
