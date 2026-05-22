"""Config parser — needs tests."""
import json
from typing import Any

def parse_config(raw: str) -> dict[str, Any]:
    if not raw or not raw.strip():
        raise ValueError("empty config")
    try:
        config = json.loads(raw)
    except json.JSONDecodeError as e:
        raise ValueError(f"malformed JSON: {e}") from e
    for field in ["host", "port"]:
        if field not in config:
            raise ValueError(f"missing required field: {field}")
    if not isinstance(config["port"], int) or config["port"] <= 0:
        raise ValueError(f"invalid port: {config['port']}")
    return config
