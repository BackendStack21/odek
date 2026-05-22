"""User validation — refactored into one unified function."""
from typing import Any


def validate_user(data: dict[str, Any], rules: dict) -> bool:
    """Validate *data* against *rules*, a dict of field → validator config.

    Each rule dict may have:
      - required  (bool, default True) — field must be present
      - type      (type, optional)     — isinstance check
      - validator (callable, optional) — called with the field value,
                                         must return True/False

    Optional fields (required=False) are only validated when present.
    """
    for field, cfg in rules.items():
        required = cfg.get("required", True)
        expected_type = cfg.get("type")
        validator = cfg.get("validator")

        if required and field not in data:
            return False

        if field not in data:
            continue  # optional field absent → skip

        value = data[field]

        if expected_type is not None and not isinstance(value, expected_type):
            return False

        if validator is not None and not validator(value):
            return False

    return True


# ── Compatibility aliases (drop-in replacements for v1/v2/v3) ──────────────

def validate_user_v1(data: dict[str, Any]) -> bool:
    return validate_user(data, {
        "name": {"type": str, "validator": lambda v: v.strip()},
        "age":  {"required": False, "type": int, "validator": lambda v: v > 0},
    })


def validate_user_v2(data: dict[str, Any]) -> bool:
    return validate_user(data, {
        "name":  {"type": str, "validator": lambda v: v.strip()},
        "email": {"validator": lambda v: "@" in v},
        "age":   {"required": False, "type": int, "validator": lambda v: v > 0},
    })


def validate_user_v3(data: dict[str, Any]) -> bool:
    return validate_user(data, {
        "name":  {"type": str, "validator": lambda v: v.strip()},
        "email": {"validator": lambda v: "@" in v},
        "role":  {"validator": lambda v: v in ("admin", "user", "guest")},
        "age":   {"required": False, "type": int, "validator": lambda v: v > 0},
    })


def format_user(data: dict[str, Any]) -> str:
    return f"{data.get('name','Unknown')} <{data.get('email','')}>"
