"""Concrete handlers."""
from typing import Any
from .base import BaseHandler

class AuthHandler(BaseHandler):
    def handle(self, r: dict[str, Any]) -> dict[str, Any] | None:
        if r.get("auth") == "valid": r["authenticated"] = True; return self._pass(r)
        return {"error": "unauthorized"}

class RateLimitHandler(BaseHandler):
    def __init__(self, max_rps=100): super().__init__(); self.max_rps = max_rps
    def handle(self, r: dict[str, Any]) -> dict[str, Any] | None:
        return self._pass(r) if r.get("rate",0) <= self.max_rps else {"error":"rate_limited"}

class BusinessLogicHandler(BaseHandler):
    def handle(self, r: dict[str, Any]) -> dict[str, Any] | None:
        return self._pass(r) or {"status":"processed","data":r}
