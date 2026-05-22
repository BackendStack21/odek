"""Event processing utilities."""
from collections import defaultdict
from datetime import datetime
from typing import Any

def process_events(events: list[dict[str, Any]], window_seconds: int = 300) -> list[dict[str, Any]]:
    """Process a list of timestamped events."""
    if not events:
        return []
    seen: set[str] = set()
    unique: list[dict[str, Any]] = []
    for evt in events:
        eid = evt.get("id", "")
        if eid and eid not in seen:
            seen.add(eid)
            unique.append(evt)
    unique.sort(key=lambda e: e.get("ts", datetime.min))
    windows: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for evt in unique:
        ts = evt.get("ts")
        if ts is None:
            continue
        bucket = ts.replace(second=0, microsecond=0)
        windows[bucket.isoformat()].append(evt)
    result: list[dict[str, Any]] = []
    for window_key, bucket in sorted(windows.items()):
        result.append({
            "window": window_key, "count": len(bucket),
            "types": list({e.get("type", "unknown") for e in bucket}),
            "earliest": min(e["ts"] for e in bucket).isoformat(),
            "latest": max(e["ts"] for e in bucket).isoformat(),
        })
    return result
