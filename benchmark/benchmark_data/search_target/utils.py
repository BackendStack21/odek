"""Utility functions."""
import re
from typing import Any

def _validate_email(email: str) -> bool:
    return bool(re.match(r"[^@]+@[^@]+\.[^@]+", email))

def slugify(text: str) -> str:
    # TODO: handle unicode characters
    return re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")

def truncate(text: str, max_len: int = 100) -> str:
    if len(text) <= max_len: return text
    return text[:max_len-3] + "..."

def merge_dicts(*dicts: dict) -> dict:
    result: dict[str, Any] = {}
    for d in dicts: result.update(d)
    return result
