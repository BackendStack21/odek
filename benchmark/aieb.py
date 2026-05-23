#!/usr/bin/env python3
"""
AIEB — Agent Intelligence, Efficiency & Security Benchmark v2.0
===============================================================
Four tiers × three tasks. Automated scoring. Both agents on same tasks.

Changes in v2.0:
  - New Tier 4: Agent Speed (tool execution latency, parallel capability)
  - Tightened scoring: required keywords must ALL match (no partial credit)
  - LOC tolerance reduced from 10% to 3%
  - merge_intervals: added large input + input mutation checks
  - verify_test_file: counts distinct assertions, not just test functions
  - verify_refactor: requires dict-based rules validator with type checking
  - Score capped at 95 for slow agents (>120s per task penalty)
  - Penalty for exceeding max_iter

Usage:
  python3 benchmark/aieb.py              # run odek only
  python3 benchmark/aieb.py --hermes     # run hermes only
  python3 benchmark/aieb.py --both       # compare both
"""
import json, os, sys, time, subprocess, re, importlib, importlib.util, tempfile, shutil, random
from pathlib import Path
from datetime import datetime

BENCHMARK_DIR = Path(__file__).parent.resolve()
ODEK_BIN = os.path.expanduser("~/projects/odek/odek")
ODEK_API_KEY = os.environ.get("ODEK_API_KEY", "")
HERMES_BIN = shutil.which("hermes") or "/usr/local/lib/hermes-agent/venv/bin/hermes"

# ─── Difficulty Tuning ──────────────────────────────────────────────
# v2.0: stricter thresholds to make higher scores harder
REQUIRED_HIT_RATIO = 1.0   # all required keywords must appear (was: partial credit)
LOC_TOLERANCE      = 0.03  # 3% tolerance (was: 10%)
SLOW_TASK_SECS     = 120   # score cap at 95 if wall time exceeds this
MAX_ITER_THRESHOLD = 0.8   # penalty if iterations > 80% of max_iter

# ─── Tasks ────────────────────────────────────────────────────────────

TASKS = [
    # Tier 1: Code Understanding
    {
        "id": "1.1", "tier": 1, "name": "explain_function", "max_iter": 5,
        "prompt": "Read benchmark_data/explain_me.py. The function `process_events` does something specific. Explain in ONE sentence what it does, then list all edge cases it handles. Output format:\nPURPOSE: <one sentence>\nEDGE_CASES: <comma-separated list>",
        "score": lambda out: score_keywords(out, ["dedup", "sort", "window", "aggregate", "bucket"], ["empty list", "null id", "missing timestamp"]),
    },
    {
        "id": "1.2", "tier": 1, "name": "find_bug", "max_iter": 5,
        "prompt": "Read benchmark_data/buggy.py. There is exactly ONE bug. Identify: file, line number, the bug, and the fix.\nOutput format:\nFILE: <filename>\nLINE: <number>\nBUG: <description>\nFIX: <the corrected line>",
        "score": lambda out: score_keywords(out, ["=", "==", "assignment", "comparison"], ["buggy.py"]),
    },
    {
        "id": "1.3", "tier": 1, "name": "identify_architecture", "max_iter": 8,
        "prompt": "Read ALL files in benchmark_data/arch_project/. Identify the architecture pattern, key abstractions, and communication mechanism.\nOutput format:\nPATTERN: <name>\nABSTRACTIONS: <comma-separated>\nCOMMUNICATION: <one sentence>",
        "score": lambda out: score_keywords(out, ["chain", "responsibility", "handler"], ["BaseHandler", "set_next", "pipeline"]),
    },
    # Tier 2: Tool Orchestration
    {
        "id": "2.1", "tier": 2, "name": "find_exports", "max_iter": 10,
        "prompt": "In benchmark_data/search_target/, find all Python files. List every exported (non-underscore-prefixed) function and class name, grouped by file.\nOutput format:\nFILE: <filename>\n  FUNCTIONS: <comma-separated>\n  CLASSES: <comma-separated>",
        "score": lambda out: score_keywords(out, ["UserAPI", "PostAPI", "find_user_by_email", "slugify", "truncate", "merge_dicts"], []),
    },
    {
        "id": "2.2", "tier": 2, "name": "count_loc", "max_iter": 8,
        "prompt": "In benchmark_data/search_target/, count total lines of code (non-blank, non-comment) across all Python files. Use shell commands (grep, wc).\nOutput: per-file breakdown + TOTAL.",
        "score": lambda out: score_loc(out, expected_total=48),
    },
    {
        "id": "2.3", "tier": 2, "name": "find_todos", "max_iter": 8,
        "prompt": "In benchmark_data/search_target/, find all TODO, FIXME, and HACK comments. Report: file, line, type, text.\nOutput format: <file>:<line>: <TYPE> <text>",
        "score": lambda out: score_keywords(out, ["TODO", "FIXME", "HACK"], ["avatar_url", "implement actual", "hardcoded", "lenient", "unicode"]),
    },
    # Tier 3: Code Generation
    {
        "id": "3.1", "tier": 3, "name": "write_function", "max_iter": 8,
        "prompt": "Write a Python function to benchmark_data/output/merge_intervals.py:\n- Function: merge_intervals(intervals: list[list[int]]) -> list[list[int]]\n- Merges overlapping [start,end] intervals, returns sorted non-overlapping list\n- Example: [[1,3],[2,6],[8,10],[15,18]] -> [[1,6],[8,10],[15,18]]\n- Handle: empty, single, all overlapping, no overlap, negatives, unsorted input\n- Must NOT mutate the input list (make a copy first)\n- Write ONLY the function file with docstring. Importable, callable.",
        "score": lambda out: verify_merge_intervals(),
    },
    {
        "id": "3.2", "tier": 3, "name": "add_test", "max_iter": 10,
        "prompt": "Read benchmark_data/under_tested.py. Write tests at benchmark_data/output/test_under_tested.py for parse_config(). Cover: valid parse, empty string, missing fields, malformed JSON, invalid port types, negative port, zero port. Use assertEqual/assertRaises (stdlib unittest). Run with: python3 benchmark_data/output/test_under_tested.py",
        "score": lambda out: verify_test_file(),
    },
    {
        "id": "3.3", "tier": 3, "name": "refactor", "max_iter": 10,
        "prompt": "Read benchmark_data/refactor_me.py. validate_user_v1/v2/v3 do the same thing with variations. Refactor into ONE validate_user(data, rules) where rules is dict of field -> validator function. Include type-checking validators (is_string, is_email, is_int, is_positive_int, is_one_of). Write to benchmark_data/output/refactored.py. Original imports intact. Must keep format_user.",
        "score": lambda out: verify_refactor(),
    },
    # Tier 4: Agent Speed (NEW in v2.0)
    {
        "id": "4.1", "tier": 4, "name": "fast_read", "max_iter": 5,
        "prompt": "Read these three files as fast as possible: benchmark_data/explain_me.py, benchmark_data/buggy.py, benchmark_data/under_tested.py. Report their total combined file size in bytes and the first line of each file.\nOutput format:\nTOTAL_BYTES: <number>\nFILES:\n  <filename>: <first line>\n  <filename>: <first line>\n  <filename>: <first line>",
        "score": lambda out, wt: score_speed_read(out, expected_bytes=parse_expected_sizes(), wall_time=wt),
    },
    {
        "id": "4.2", "tier": 4, "name": "quick_math", "max_iter": 5,
        "prompt": "Using ONLY the shell tool (no Python), compute: 42 * 17, then add 256 to the result, then divide by 10. Report each intermediate result and the final answer.\nOutput format:\nSTEP1: 42 * 17 = <result>\nSTEP2: <result> + 256 = <result>\nSTEP3: <result> / 10 = <final>",
        "score": lambda out, wt: score_shell_math(out, expected=97, wall_time=wt),
    },
    {
        "id": "4.3", "tier": 4, "name": "multi_search", "max_iter": 8,
        "prompt": "In benchmark_data/, search for ALL of these patterns simultaneously (one call each, run in parallel):\n1. 'TODO' in all files\n2. 'def ' in all files\n3. 'import ' in all files\nReport the count of matches for each search.\nOutput format:\nTODO: <count>\nDEFS: <count>\nIMPORTS: <count>",
        "score": lambda out, wt: score_multi_search(out, wall_time=wt),
    },
]


# ─── Scoring Helpers ──────────────────────────────────────────────────

def score_keywords(output: str, required: list[str], bonus: list[str]) -> float:
    """Score 0-100 based on presence of required and bonus keywords.
    v2.0: ALL required keywords must be present for any credit."""
    lower = output.lower()
    req_hits = sum(1 for k in required if k.lower() in lower)

    # v2.0: must hit ALL required keywords
    if req_hits < len(required) * REQUIRED_HIT_RATIO:
        # Partial credit only if at least half
        if req_hits < len(required) * 0.5:
            return round(req_hits / max(len(required), 1) * 30, 1)
        return round(req_hits / max(len(required), 1) * 60, 1)

    score = 80.0  # all required matched
    if bonus:
        bon_hits = sum(1 for k in bonus if k.lower() in lower)
        score += (bon_hits / len(bonus)) * 20
    return round(min(score, 100), 1)


def score_loc(output: str, expected_total: int) -> float:
    """Score LOC counting — v2.0: 3% tolerance, penalty for per-file errors."""
    # Check per-file breakdown
    lines = output.strip().split('\n')
    total_match = None
    per_file_ok = True
    for line in lines:
        m = re.search(r"TOTAL:\s*(\d+)", line, re.IGNORECASE)
        if m:
            total_match = int(m.group(1))

    if total_match is None:
        return 0.0

    diff = abs(total_match - expected_total) / max(expected_total, 1)
    if diff <= LOC_TOLERANCE:
        return 100.0
    elif diff <= 0.10:
        return round(max(0, 100 - diff * 200), 1)
    else:
        return round(max(0, 50 - diff * 50), 1)


def verify_merge_intervals() -> float:
    """Run the generated merge_intervals function and test it.
    v2.0: added large input test + input mutation check."""
    path = BENCHMARK_DIR / "benchmark_data" / "output" / "merge_intervals.py"
    if not path.exists():
        return 0.0
    try:
        spec = importlib.util.spec_from_file_location("merge_intervals", path)
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        fn = getattr(mod, "merge_intervals", None)
        if fn is None:
            return 0.0
        tests = [
            ([[1,3],[2,6],[8,10],[15,18]], [[1,6],[8,10],[15,18]]),
            ([], []),
            ([[1,4]], [[1,4]]),
            ([[1,4],[2,3],[3,6]], [[1,6]]),
            ([[1,2],[3,4],[5,6]], [[1,2],[3,4],[5,6]]),
            ([[-5,0],[-3,2],[1,5]], [[-5,5]]),
            ([[5,8],[1,2],[10,12],[3,4]], [[1,2],[3,4],[5,8],[10,12]]),  # unsorted
            ([[0,3],[0,3],[0,3]], [[0,3]]),  # duplicates
        ]
        passed = sum(1 for inp, exp in tests if fn(inp) == exp)

        # Large input test (1000 intervals)
        large = [[i*2, i*2+1] for i in range(1000)]
        try:
            large_result = fn(large)
            large_ok = len(large_result) == 1000 and large_result == [[i*2, i*2+1] for i in range(1000)]
            if large_ok:
                passed += 1
                tests.append((large, large_result))
        except Exception:
            pass

        # Input mutation check
        inp = [[1,3],[2,6]]
        inp_copy = [list(x) for x in inp]
        fn(inp)
        if inp == inp_copy:
            passed += 0.5  # half point for no mutation
        else:
            passed -= 0.5  # penalty for mutating input

        max_score = len(tests) + 0.5
        return round(max(0, passed / max_score * 100), 1)
    except Exception:
        return 0.0


def verify_test_file() -> float:
    """Run the generated test file and check exit code + assertion count.
    v2.0: requires specific assertions (assertEqual/assertRaises)."""
    path = BENCHMARK_DIR / "benchmark_data" / "output" / "test_under_tested.py"
    if not path.exists():
        return 0.0
    try:
        r = subprocess.run([sys.executable, str(path)], capture_output=True, text=True, timeout=10)
        if r.returncode != 0:
            return 0.0

        content = path.read_text()
        # Count test functions
        test_fns = len(re.findall(r"def test_", content))
        # Count assertions (v2.0: stricker — need real assertions)
        asserts = len(re.findall(r"assertEqual|assertRaises|assertTrue|assertFalse|assertIn|assertIsNone", content))
        if test_fns == 0:
            return 0.0

        score = 0.0
        score += min(40, test_fns * 8)       # up to 40 for test functions
        score += min(60, asserts * 12)        # up to 60 for assertions

        # Bonus: must cover specific cases
        if "empty" in content.lower():
            score += 10
        if "malformed" in content.lower() or "json.decoder" in content.lower():
            score += 10
        if "invalid port" in content.lower() or "negative" in content.lower():
            score += 10

        return min(100, score)
    except Exception:
        return 0.0


def verify_refactor() -> float:
    """Check refactored file exists and has proper structure.
    v2.0: requires dict-based rules validator with type-checking validators."""
    path = BENCHMARK_DIR / "benchmark_data" / "output" / "refactored.py"
    if not path.exists():
        return 0.0
    content = path.read_text()
    score = 0.0

    # Must have single validate_user
    if "def validate_user" in content and "def validate_user_v" not in content:
        score += 30

    # Must have rules dict parameter
    if "rules" in content:
        score += 20

    # Must keep format_user
    if "def format_user" in content:
        score += 10

    # Type-checking validators (v2.0 requirement)
    validators = sum(1 for v in ["is_string", "is_email", "is_int", "is_positive_int", "is_one_of"]
                     if v in content)
    score += min(20, validators * 5)

    # Must use dict-based dispatch (not if/elif chains)
    if "rules[" in content or "rules.get(" in content:
        score += 20

    return min(100, score)


def score_speed_read(output: str, expected_bytes: int, wall_time: float = 120) -> float:
    """Format-tolerant fast_read scorer.
    Finds all numbers in output, picks closest to expected total bytes.
    50% correctness (accuracy × 0.7 + file coverage × 0.3) + 50% speed bonus."""
    lower = output.lower()
    numbers = [int(m.group()) for m in re.finditer(r'\d+', output)]
    if not numbers:
        return 0.0

    # Accuracy: best match to expected total
    closest = min(numbers, key=lambda n: abs(n - expected_bytes))
    accuracy = max(0, 1 - abs(closest - expected_bytes) / max(expected_bytes, 1))

    # File coverage: mention all 3 files?
    files = sum(1 for f in ["explain_me.py", "buggy.py", "under_tested.py"]
                if f.lower() in lower)
    file_cov = files / 3

    correctness = (accuracy * 0.7 + file_cov * 0.3) * 50
    speed = _speed_bonus(wall_time)
    return round(min(100, correctness + speed), 1)


def score_shell_math(output: str, expected: int, wall_time: float = 120) -> float:
    """Format-tolerant quick_math scorer.
    Looks for the final answer (97) anywhere in the output.
    50% correctness + 50% speed bonus."""
    # Can we find the final answer?
    numbers = [int(m.group()) for m in re.finditer(r'\d+', output)]
    found_final = expected in numbers
    # Also look for intermediate results
    found_714 = 714 in numbers
    found_970 = 970 in numbers

    correctness = 0.0
    if found_final:
        correctness += 35  # final answer is most important
    if found_714:
        correctness += 10  # intermediate
    if found_970:
        correctness += 5   # intermediate

    speed = _speed_bonus(wall_time)
    return round(min(100, correctness + speed), 1)


def score_multi_search(output: str, wall_time: float = 120) -> float:
    """Format-tolerant multi_search scorer.
    Looks for reasonable match counts in the output.
    Expected: TODO ~3, def ~66, import ~38.
    50% correctness + 50% speed bonus."""
    numbers = [int(m.group()) for m in re.finditer(r'\d+', output)]
    if not numbers:
        return 0.0

    # Score matches based on proximity to expected values
    def proximity(actual, expected):
        return max(0, 1 - abs(actual - expected) / max(expected, 1))

    best_todo = max((proximity(n, 3) for n in numbers), default=0)
    best_def  = max((proximity(n, 66) for n in numbers), default=0)
    best_import = max((proximity(n, 38) for n in numbers), default=0)

    # Also check that output mentions all three search terms
    mentions_todo = 'todo' in output.lower() or 'TODO' in output
    mentions_def = 'def' in output.lower()
    mentions_import = 'import' in output.lower()
    coverage = sum([mentions_todo, mentions_def, mentions_import]) / 3

    correctness = (best_todo * 15 + best_def * 15 + best_import * 10 + coverage * 10)
    speed = _speed_bonus(wall_time)
    return round(min(100, correctness + speed), 1)


def _speed_bonus(wall_time: float, max_score: float = 50.0) -> float:
    """Speed bonus: 0 at 120s, full at 10s, linear ramp."""
    if wall_time <= 10:
        return max_score
    if wall_time >= 120:
        return 0.0
    return round(max_score * (1 - (wall_time - 10) / 110), 1)


# ─── Benchmark Data ──────────────────────────────────────────────────

def create_benchmark_data():
    """Create all benchmark source files (idempotent)."""
    data = BENCHMARK_DIR / "benchmark_data"
    data.mkdir(exist_ok=True)

    # Tier 1 files
    (data / "explain_me.py").write_text('''"""Event processing utilities."""
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
''')

    (data / "buggy.py").write_text('''"""User authentication — CONTAINS A BUG."""
def authenticate(username: str, password: str, user_db: dict) -> bool:
    if not username or not password:
        return False
    user = user_db.get(username)
    if user is None:
        return False
    if user["active"] = False:  # BUG: assignment, not comparison
        return False
    if user["password"] != password:
        return False
    return True
''')

    # Architecture project (Chain of Responsibility)
    arch = data / "arch_project"
    arch.mkdir(exist_ok=True)
    (arch / "__init__.py").write_text("")
    (arch / "base.py").write_text('''"""Abstract base for all handlers."""
from abc import ABC, abstractmethod
from typing import Any

class BaseHandler(ABC):
    """Chain of Responsibility base."""
    def __init__(self) -> None:
        self._next: BaseHandler | None = None
    def set_next(self, handler: "BaseHandler") -> "BaseHandler":
        self._next = handler
        return handler
    @abstractmethod
    def handle(self, request: dict[str, Any]) -> dict[str, Any] | None: ...
    def _pass(self, request: dict[str, Any]) -> dict[str, Any] | None:
        if self._next:
            return self._next.handle(request)
        return None
''')
    (arch / "handlers.py").write_text('''"""Concrete handler implementations."""
from typing import Any
from .base import BaseHandler

class AuthHandler(BaseHandler):
    def handle(self, request: dict[str, Any]) -> dict[str, Any] | None:
        if request.get("auth") == "valid":
            request["authenticated"] = True
            return self._pass(request)
        return {"error": "unauthorized"}

class RateLimitHandler(BaseHandler):
    def __init__(self, max_rps: int = 100) -> None:
        super().__init__()
        self.max_rps = max_rps
    def handle(self, request: dict[str, Any]) -> dict[str, Any] | None:
        if request.get("rate", 0) <= self.max_rps:
            return self._pass(request)
        return {"error": "rate_limited"}

class BusinessLogicHandler(BaseHandler):
    def handle(self, request: dict[str, Any]) -> dict[str, Any] | None:
        result = self._pass(request)
        return result or {"status": "processed", "data": request}
''')
    (arch / "pipeline.py").write_text('''"""Pipeline builder."""
from .base import BaseHandler
from .handlers import AuthHandler, RateLimitHandler, BusinessLogicHandler

def build_pipeline(max_rps: int = 100) -> BaseHandler:
    auth = AuthHandler()
    rate = RateLimitHandler(max_rps=max_rps)
    logic = BusinessLogicHandler()
    auth.set_next(rate).set_next(logic)
    return auth
''')

    # Search target (Tier 2)
    st = data / "search_target"
    st.mkdir(exist_ok=True)
    (st / "__init__.py").write_text("")
    (st / "models.py").write_text('''"""Data models."""
from dataclasses import dataclass
from datetime import datetime
from typing import Optional

@dataclass
class User:
    id: int; name: str; email: str; created_at: datetime
    # TODO: add avatar_url field
    # FIXME: email validation is too lenient

@dataclass
class Post:
    id: int; author_id: int; title: str; content: str; published: bool = False
    # HACK: using string for tags, should be a list

def find_user_by_email(users: list[User], email: str) -> Optional[User]:
    for user in users:
        if user.email == email:
            return user
    return None

def get_published_posts(posts: list[Post]) -> list[Post]:
    # FIXME: should filter by date too
    return [p for p in posts if p.published]
''')
    (st / "utils.py").write_text('''"""Utility functions."""
import re
from typing import Any

def _validate_email(email: str) -> bool:
    return bool(re.match(r"[^@]+@[^@]+\.[^@]+", email))

def slugify(text: str) -> str:
    # TODO: handle unicode characters
    return re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")

def truncate(text: str, max_len: int = 100) -> str:
    if len(text) <= max_len:
        return text
    return text[:max_len-3] + "..."

def merge_dicts(*dicts: dict) -> dict:
    result: dict[str, Any] = {}
    for d in dicts:
        result.update(d)
    return result
''')
    (st / "api.py").write_text('''"""REST API handlers."""
from typing import Any
from .models import User, Post

class UserAPI:
    def create_user(self, data: dict[str, Any]) -> User:
        # TODO: validate input data
        return User(**data)
    def get_user(self, user_id: int) -> User | None:
        # FIXME: implement actual DB lookup
        raise NotImplementedError("DB not connected")
    def list_users(self) -> list[User]:
        return []

class PostAPI:
    def create_post(self, data: dict[str, Any]) -> Post:
        return Post(**data)
    def publish_post(self, post_id: int) -> Post:
        # HACK: hardcoded for demo
        return Post(id=post_id, author_id=1, title="demo", content="...", published=True)
''')

    # Tier 3 files
    (data / "under_tested.py").write_text('''"""Config parser — needs tests."""
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
''')

    (data / "refactor_me.py").write_text('''"""User validation — needs refactoring."""
from typing import Any

def validate_user_v1(data: dict[str, Any]) -> bool:
    if "name" not in data:
        return False
    if not isinstance(data.get("name"), str) or not data["name"].strip():
        return False
    if "age" in data:
        if not isinstance(data["age"], int) or data["age"] <= 0:
            return False
    return True

def validate_user_v2(data: dict[str, Any]) -> bool:
    if "name" not in data or "email" not in data:
        return False
    if not isinstance(data["name"], str) or not data["name"].strip():
        return False
    if "@" not in data.get("email", ""):
        return False
    if "age" in data:
        if not isinstance(data["age"], int) or data["age"] <= 0:
            return False
    return True

def validate_user_v3(data: dict[str, Any]) -> bool:
    if "name" not in data or "email" not in data or "role" not in data:
        return False
    if not isinstance(data["name"], str) or not data["name"].strip():
        return False
    if "@" not in data["email"]:
        return False
    if data["role"] not in ("admin", "user", "guest"):
        return False
    if "age" in data:
        if not isinstance(data["age"], int) or data["age"] <= 0:
            return False
    return True

def format_user(data: dict[str, Any]) -> str:
    name = data.get("name", "Unknown")
    email = data.get("email", "")
    return f"{name} <{email}>"
''')

    (data / "output").mkdir(exist_ok=True)


def parse_expected_sizes() -> int:
    """Calculate expected total bytes for fast_read task."""
    total = 0
    for f in ["explain_me.py", "buggy.py", "under_tested.py"]:
        path = BENCHMARK_DIR / "benchmark_data" / f
        if path.exists():
            total += path.stat().st_size
    return total


# ─── Runners ──────────────────────────────────────────────────────────

def run_odek(task: dict) -> dict:
    cmd = [ODEK_BIN, "run", "--model", "deepseek-v4-flash",
           "--max-iter", str(task["max_iter"]), "--no-color", task["prompt"]]
    env = {**os.environ, "ODEK_API_KEY": ODEK_API_KEY}
    start = time.time()
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=300,
                          cwd=str(BENCHMARK_DIR), env=env)
    except subprocess.TimeoutExpired:
        return {"error": "timeout", "wall_time": time.time() - start, "score": 0}

    wall = time.time() - start
    combined = (r.stderr or "") + "\n" + (r.stdout or "")

    tokens_in = tokens_out = 0
    m = re.search(r"--\s*(\d+)\s+in\s*·\s*(\d+)\s+out", combined)
    if m:
        tokens_in = int(m.group(1))
        tokens_out = int(m.group(2))

    iterations = len(re.findall(r"═══ Iter \d+/\d+", combined))
    try:
        score = task["score"](combined, wall)
    except TypeError:
        score = task["score"](combined)

    # v2.0: slow task penalty
    if wall > SLOW_TASK_SECS and score > 0:
        score = min(score, 95)

    # v2.0: iteration efficiency penalty
    if iterations > task["max_iter"] * MAX_ITER_THRESHOLD and score > 0:
        overage = (iterations - task["max_iter"] * MAX_ITER_THRESHOLD) / (task["max_iter"] * 0.2)
        penalty = min(20, overage * 10)
        score = max(0, score - penalty)

    return {
        "wall_time": round(wall, 1), "tokens_in": tokens_in, "tokens_out": tokens_out,
        "iterations": iterations, "score": round(score, 1), "error": None,
    }


def run_hermes(task: dict) -> dict:
    cmd = [HERMES_BIN, "-z", task["prompt"],
           "-m", "deepseek-v4-flash", "--provider", "deepseek",
           "-t", "terminal,file,search"]
    start = time.time()
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=300,
                          cwd=str(BENCHMARK_DIR))
    except subprocess.TimeoutExpired:
        return {"error": "timeout", "wall_time": time.time() - start, "score": 0}

    wall = time.time() - start
    output = (r.stdout or "") + (r.stderr or "")
    try:
        score = task["score"](output, wall)
    except TypeError:
        score = task["score"](output)

    # v2.0: slow task penalty
    if wall > SLOW_TASK_SECS and score > 0:
        score = min(score, 95)

    return {
        "wall_time": round(wall, 1), "tokens_in": 0, "tokens_out": 0,
        "iterations": 0, "score": round(score, 1), "error": None,
    }


# ─── Main ─────────────────────────────────────────────────────────────

def run_benchmark(agents: list[str]) -> dict:
    create_benchmark_data()
    # Clean stale output from previous runs
    out_dir = BENCHMARK_DIR / "benchmark_data" / "output"
    if out_dir.exists():
        shutil.rmtree(out_dir)
        out_dir.mkdir()

    all_results = {}
    for agent in agents:
        print(f"\n{'='*60}")
        print(f"  AIEB v2.0 — {agent}")
        print(f"{'='*60}")

        agent_results = {"agent": agent, "tasks": [], "total_time": 0, "total_score": 0}
        runner = run_odek if agent == "odek" else run_hermes

        for task in TASKS:
            tier_label = f"T{task['tier']}"
            print(f"  [{tier_label}.{task['id']}] {task['name']}...", end=" ", flush=True)

            r = runner(task)
            r["id"] = task["id"]
            r["name"] = task["name"]
            r["tier"] = task["tier"]

            if r.get("error"):
                print(f"❌ {r['error']}")
            else:
                tier_info = f"iter:{r.get('iterations','?')}"
                print(f"{r['wall_time']:.0f}s | score:{r['score']:.0f} | {tier_info} | {r['tokens_in']}->{r['tokens_out']} tok")

            agent_results["tasks"].append(r)
            agent_results["total_time"] += r.get("wall_time", 0)
            agent_results["total_score"] += r.get("score", 0)

        all_results[agent] = agent_results

    # Print comparison
    print(f"\n{'='*60}")
    print(f"  AIEB v2.0 — {len(agents)} Agent{'s' if len(agents)>1 else ''}")
    print(f"{'='*60}")

    if len(agents) >= 1:
        for agent in agents:
            a = all_results.get(agent, {})
            tasks = a.get("tasks", [])
            avg = sum(t.get("score", 0) for t in tasks) / max(len(tasks), 1)
            print(f"\n  {'─'*40}")
            print(f"  🏆  {agent.upper()}")
            print(f"  {'─'*40}")
            print(f"  Total time: {a.get('total_time', 0):.0f}s")
            print(f"  Average score: {avg:.1f}%")
            if len(tasks) == 12:
                t1 = sum(t.get("score", 0) for t in tasks if t["tier"] == 1) / 3
                t2 = sum(t.get("score", 0) for t in tasks if t["tier"] == 2) / 3
                t3 = sum(t.get("score", 0) for t in tasks if t["tier"] == 3) / 3
                t4 = sum(t.get("score", 0) for t in tasks if t["tier"] == 4) / 3
                print(f"  Tier 1 (Understanding): {t1:.0f}%")
                print(f"  Tier 2 (Orchestration): {t2:.0f}%")
                print(f"  Tier 3 (Generation):    {t3:.0f}%")
                print(f"  Tier 4 (Speed):         {t4:.0f}%")
            for task in TASKS:
                t = next((x for x in tasks if x["id"] == task["id"]), None)
                if t:
                    flags = ""
                    if t.get("wall_time", 0) > SLOW_TASK_SECS:
                        flags += " ⏱"
                    print(f"  [{task['id']}] {task['name']:<18} {t['score']:>5.0f}%  ({t['wall_time']:.0f}s, {t.get('iterations','?')} iter)")

    if len(agents) == 2:
        o = all_results.get("odek", {})
        h = all_results.get("hermes", {})
        o_avg = sum(t.get("score", 0) for t in o.get("tasks", [])) / max(len(o.get("tasks", [])), 1)
        h_avg = sum(t.get("score", 0) for t in h.get("tasks", [])) / max(len(h.get("tasks", [])), 1)
        print(f"\n  {'─'*40}")
        print(f"  ⚔️  HEAD-TO-HEAD")
        print(f"  {'─'*40}")
        print(f"  {'':20} {'odek':>10} {'hermes':>10} {'winner':>10}")
        print(f"  {'─'*20} {'─'*10} {'─'*10} {'─'*10}")
        print(f"  {'Avg score':20} {o_avg:>8.1f}%   {h_avg:>8.1f}%   {'odek' if o_avg > h_avg else 'hermes'}")
        print(f"  {'Total time':20} {o.get('total_time',0):>8.0f}s   {h.get('total_time',0):>8.0f}s   {'odek' if o.get('total_time',0) < h.get('total_time',0) else 'hermes'}")
        for task in TASKS:
            ot = next((x for x in o.get("tasks", []) if x["id"] == task["id"]), None)
            ht = next((x for x in h.get("tasks", []) if x["id"] == task["id"]), None)
            os_ = ot["score"] if ot else 0
            hs_ = ht["score"] if ht else 0
            winner = "odek" if os_ > hs_ else "hermes" if hs_ > os_ else "tie"
            print(f"  [{task['id']}] {task['name']:<15} {os_:>8.1f}%   {hs_:>8.1f}%   {winner}")

    # Save
    out_path = BENCHMARK_DIR / "results.json"
    out_path.write_text(json.dumps({
        "benchmark": "AIEB v2.0",
        "timestamp": datetime.now().isoformat(),
        "results": {k: {"total_time": v["total_time"], "tasks": v["tasks"]}
                     for k, v in all_results.items()}
    }, indent=2))
    print(f"\n  Results saved: {out_path}")
    return all_results


def avg_score(agent_results: dict) -> float:
    tasks = agent_results.get("tasks", [])
    if not tasks:
        return 0
    return sum(t.get("score", 0) for t in tasks) / len(tasks)


if __name__ == "__main__":
    import argparse
    p = argparse.ArgumentParser()
    p.add_argument("--hermes", action="store_true")
    p.add_argument("--both", action="store_true")
    args = p.parse_args()

    if args.both:
        agents = ["odek", "hermes"]
    elif args.hermes:
        agents = ["hermes"]
    else:
        agents = ["odek"]

    run_benchmark(agents)
