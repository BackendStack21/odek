"""Input sanitization helpers."""
import re
import html

def sanitize_html(text: str) -> str:
    """Escape HTML entities."""
    return html.escape(text, quote=True)

def sanitize_sql_identifier(name: str) -> str:
    """Allow only alphanumeric + underscore identifiers."""
    return re.sub(r"[^a-zA-Z0-9_]", "", name)[:64]

def strip_control_chars(s: str) -> str:
    """Remove ASCII control characters (0x00-0x1F, except tab/newline)."""
    return re.sub(r"[\x00-\x08\x0b\x0c\x0e-\x1f]", "", s)
