"""Chain of Responsibility base."""
from abc import ABC, abstractmethod
from typing import Any

class BaseHandler(ABC):
    def __init__(self): self._next: BaseHandler | None = None
    def set_next(self, h: "BaseHandler") -> "BaseHandler": self._next = h; return h
    @abstractmethod
    def handle(self, r: dict[str, Any]) -> dict[str, Any] | None: ...
    def _pass(self, r: dict[str, Any]) -> dict[str, Any] | None:
        return self._next.handle(r) if self._next else None
