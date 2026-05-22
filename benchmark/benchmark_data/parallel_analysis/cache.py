"""LRU cache with TTL expiration."""
from collections import OrderedDict
from time import time
from typing import Any, Optional

class TTLCache:
    def __init__(self, maxsize: int = 128, ttl: int = 300):
        self.maxsize = maxsize
        self.ttl = ttl
        self._store: OrderedDict = OrderedDict()
        self._expires: dict = {}

    def get(self, key: str) -> Optional[Any]:
        if key not in self._store:
            return None
        if time() > self._expires.get(key, 0):
            del self._store[key]
            del self._expires[key]
            return None
        self._store.move_to_end(key)
        return self._store[key]

    def set(self, key: str, value: Any):
        if key in self._store:
            self._store.move_to_end(key)
        self._store[key] = value
        self._expires[key] = time() + self.ttl
        if len(self._store) > self.maxsize:
            old = self._store.popitem(last=False)
            self._expires.pop(old[0], None)
