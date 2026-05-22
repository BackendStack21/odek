"""User validation — needs refactoring."""
from typing import Any

def validate_user_v1(data: dict[str, Any]) -> bool:
    if "name" not in data: return False
    if not isinstance(data.get("name"), str) or not data["name"].strip(): return False
    if "age" in data:
        if not isinstance(data["age"], int) or data["age"] <= 0: return False
    return True

def validate_user_v2(data: dict[str, Any]) -> bool:
    if "name" not in data or "email" not in data: return False
    if not isinstance(data["name"], str) or not data["name"].strip(): return False
    if "@" not in data.get("email", ""): return False
    if "age" in data:
        if not isinstance(data["age"], int) or data["age"] <= 0: return False
    return True

def validate_user_v3(data: dict[str, Any]) -> bool:
    if "name" not in data or "email" not in data or "role" not in data: return False
    if not isinstance(data["name"], str) or not data["name"].strip(): return False
    if "@" not in data["email"]: return False
    if data["role"] not in ("admin", "user", "guest"): return False
    if "age" in data:
        if not isinstance(data["age"], int) or data["age"] <= 0: return False
    return True

def format_user(data: dict[str, Any]) -> str:
    return f"{data.get('name','Unknown')} <{data.get('email','')}>"
