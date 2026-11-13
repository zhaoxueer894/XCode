"""A token bucket rate limiter driven by an injected clock."""


class TokenBucket:
    def __init__(self, rate: float, capacity: int, clock):
        if rate <= 0:
            raise ValueError("rate must be positive")
        if capacity < 1:
            raise ValueError("capacity must be positive")
        self._rate = rate
        self._capacity = capacity
        self._clock = clock
        self._tokens = float(capacity)
        self._updated = clock()

    def _refill(self):
        now = self._clock()
        elapsed = now - self._updated
        if elapsed > 0:
            self._tokens = min(self._capacity, self._tokens + elapsed * self._rate)
            self._updated = now

    def allow(self, count: int = 1) -> bool:
        if count < 1:
            raise ValueError("count must be positive")
        self._refill()
        if self._tokens >= count:
            self._tokens -= count
            return True
        return False

    def tokens(self) -> float:
        self._refill()
        return self._tokens
