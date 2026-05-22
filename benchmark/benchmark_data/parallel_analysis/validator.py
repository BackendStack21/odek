"""Data validation functions."""
import re
from typing import Any, Optional

def validate_email(email: str) -> bool:
    """Basic email format check."""
    return bool(re.match(r"^[\w.+-]+@[\w-]+\.[\w.-]+$", email))

def validate_url(url: str) -> bool:
    """Check if string looks like a URL."""
    return bool(re.match(r"^https?://[\w.-]+(:\d+)?(/.*)?$", url))

def validate_range(value: float, min_val: Optional[float] = None,
                   max_val: Optional[float] = None) -> bool:
    """Check if value falls within [min, max]."""
    if min_val is not None and value < min_val:
        return False
    if max_val is not None and value > max_val:
        return False
    return True
