"""Token bucket rate limiter."""
import time
from collections import defaultdict

class TokenBucket:
    def __init__(self, rate: float = 10.0, burst: int = 20):
        self.rate = rate
        self.burst = burst
        self._buckets: dict[str, tuple[float, float]] = defaultdict(
            lambda: (burst, time.monotonic())
        )

    def allow(self, key: str) -> bool:
        tokens, last = self._buckets[key]
        now = time.monotonic()
        tokens = min(self.burst, tokens + (now - last) * self.rate)
        self._buckets[key] = (tokens, now)
        if tokens >= 1.0:
            self._buckets[key] = (tokens - 1.0, now)
            return True
        return False
