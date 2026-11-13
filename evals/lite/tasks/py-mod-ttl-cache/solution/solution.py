"""A capacity-bounded cache with per-entry expiry."""


class TTLCache:
    def __init__(self, capacity: int, ttl: float, clock):
        if capacity < 1:
            raise ValueError("capacity must be positive")
        if ttl <= 0:
            raise ValueError("ttl must be positive")
        self._capacity = capacity
        self._ttl = ttl
        self._clock = clock
        # Maps key -> [value, expires_at]; dict order is the recency order.
        self._entries = {}

    def _live(self, key):
        entry = self._entries.get(key)
        if entry is None:
            return None
        if self._clock() >= entry[1]:
            del self._entries[key]
            return None
        return entry

    def get(self, key, default=None):
        entry = self._live(key)
        if entry is None:
            return default
        del self._entries[key]
        self._entries[key] = entry
        return entry[0]

    def set(self, key, value):
        self._entries.pop(key, None)
        self._entries[key] = [value, self._clock() + self._ttl]
        while len(self._entries) > self._capacity:
            del self._entries[next(iter(self._entries))]

    def __len__(self):
        for key in [k for k, entry in self._entries.items() if self._clock() >= entry[1]]:
            del self._entries[key]
        return len(self._entries)
