#!/usr/bin/env python3
"""
AIEB — Agent Intelligence & Efficiency Benchmark v1.0
=====================================================
Three tiers × three tasks. Automated scoring. Both agents on same tasks.

Usage:
  python3 benchmark/aieb.py              # run odek only
  python3 benchmark/aieb.py --hermes     # run hermes only
  python3 benchmark/aieb.py --both       # compare both
"""
import json, os, sys, time, subprocess, re, importlib, importlib.util, tempfile, shutil
from pathlib import Path
from datetime import datetime

BENCHMARK_DIR = Path(__file__).parent.resolve()
ODEK_BIN = os.path.expanduser("~/projects/odek/odek")
ODEK_API_KEY = os.environ.get("ODEK_API_KEY", "")
HERMES_BIN = shutil.which("hermes") or "/usr/local/lib/hermes-agent/venv/bin/hermes"

# ─── Tasks ────────────────────────────────────────────────────────────

TASKS = [
    # Tier 1: Code Understanding
    {
        "id": "1.1", "tier": 1, "name": "explain_function", "max_iter": 5,
        "prompt": "Read benchmark_data/explain_me.py. The function `process_events` does something specific. Explain in ONE sentence what it does, then list all edge cases it handles. Output format:\nPURPOSE: <one sentence>\nEDGE_CASES: <comma-separated list>",
        "score": lambda out: score_keywords(out, ["dedup", "sort", "window", "aggregate"], ["empty list", "null id"]),
    },
    {
        "id": "1.2", "tier": 1, "name": "find_bug", "max_iter": 5,
        "prompt": "Read benchmark_data/buggy.py. There is exactly ONE bug. Identify: file, line number, the bug, and the fix.\nOutput format:\nFILE: <filename>\nLINE: <number>\nBUG: <description>\nFIX: <the corrected line>",
        "score": lambda out: score_keywords(out, ["=", "=="], ["buggy.py"]),
    },
    {
        "id": "1.3", "tier": 1, "name": "identify_architecture", "max_iter": 8,
        "prompt": "Read ALL files in benchmark_data/arch_project/. Identify the architecture pattern, key abstractions, and communication mechanism.\nOutput format:\nPATTERN: <name>\nABSTRACTIONS: <comma-separated>\nCOMMUNICATION: <one sentence>",
        "score": lambda out: score_keywords(out, ["chain", "responsibility"], ["BaseHandler", "handler"]),
    },
    # Tier 2: Tool Orchestration
    {
        "id": "2.1", "tier": 2, "name": "find_exports", "max_iter": 10,
        "prompt": "In benchmark_data/search_target/, find all Python files. List every exported (non-underscore-prefixed) function and class name, grouped by file.\nOutput format:\nFILE: <filename>\n  FUNCTIONS: <comma-separated>\n  CLASSES: <comma-separated>",
        "score": lambda out: score_keywords(out, ["UserAPI", "PostAPI", "find_user_by_email", "slugify"], []),
    },
    {
        "id": "2.2", "tier": 2, "name": "count_loc", "max_iter": 8,
        "prompt": "In benchmark_data/search_target/, count total lines of code (non-blank, non-comment) across all Python files. Use shell commands (grep, wc).\nOutput: per-file breakdown + TOTAL.",
        "score": lambda out: score_loc(out, expected_total=48),
    },
    {
        "id": "2.3", "tier": 2, "name": "find_todos", "max_iter": 8,
        "prompt": "In benchmark_data/search_target/, find all TODO, FIXME, and HACK comments. Report: file, line, type, text.\nOutput format: <file>:<line>: <TYPE> <text>",
        "score": lambda out: score_keywords(out, ["TODO", "FIXME", "HACK"], ["avatar_url", "implement actual", "hardcoded"]),
    },
    # Tier 3: Code Generation
    {
        "id": "3.1", "tier": 3, "name": "write_function", "max_iter": 8,
        "prompt": "Write a Python function to benchmark_data/output/merge_intervals.py:\n- Function: merge_intervals(intervals: list[list[int]]) -> list[list[int]]\n- Merges overlapping [start,end] intervals, returns sorted non-overlapping list\n- Example: [[1,3],[2,6],[8,10],[15,18]] → [[1,6],[8,10],[15,18]]\n- Handle: empty, single, all overlapping, no overlap, negatives\nWrite ONLY the function file with docstring. Importable, callable.",
        "score": lambda out: verify_merge_intervals(),
    },
    {
        "id": "3.2", "tier": 3, "name": "add_test", "max_iter": 10,
        "prompt": "Read benchmark_data/under_tested.py. Write tests at benchmark_data/output/test_under_tested.py for parse_config(). Cover: valid, empty, missing fields, malformed JSON, edge cases. Stdlib only, no pytest. Run with: python3 benchmark_data/output/test_under_tested.py",
        "score": lambda out: verify_test_file(),
    },
    {
        "id": "3.3", "tier": 3, "name": "refactor", "max_iter": 10,
        "prompt": "Read benchmark_data/refactor_me.py. validate_user_v1/v2/v3 do the same thing with variations. Refactor into ONE validate_user(data, rules) where rules is dict of field→validator. Write to benchmark_data/output/refactored.py. Original imports intact.",
        "score": lambda out: verify_refactor(),
    },
]


# ─── Scoring ──────────────────────────────────────────────────────────

def score_keywords(output: str, required: list[str], bonus: list[str]) -> float:
    """Score 0-100 based on presence of required and bonus keywords."""
    lower = output.lower()
    req_hits = sum(1 for k in required if k.lower() in lower)
    bon_hits = sum(1 for k in bonus if k.lower() in lower)
    score = (req_hits / max(len(required), 1)) * 80  # 80% for required
    if bonus:
        score += (bon_hits / len(bonus)) * 20  # 20% for bonus
    return round(min(score, 100), 1)

def score_loc(output: str, expected_total: int) -> float:
    """Score LOC counting — check total is within 10% of expected."""
    match = re.search(r"TOTAL:\s*(\d+)", output, re.IGNORECASE)
    if not match:
        return 0.0
    total = int(match.group(1))
    diff = abs(total - expected_total) / max(expected_total, 1)
    return round(max(0, 100 - diff * 100), 1)

def verify_merge_intervals() -> float:
    """Run the generated merge_intervals function and test it."""
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
        ]
        passed = sum(1 for inp, exp in tests if fn(inp) == exp)
        return round(passed / len(tests) * 100, 1)
    except Exception:
        return 0.0

def verify_test_file() -> float:
    """Run the generated test file and check exit code."""
    path = BENCHMARK_DIR / "benchmark_data" / "output" / "test_under_tested.py"
    if not path.exists():
        return 0.0
    try:
        r = subprocess.run([sys.executable, str(path)], capture_output=True, text=True, timeout=10)
        if r.returncode != 0:
            return 0.0
        # Count test functions
        tests = len(re.findall(r"def test_", path.read_text()))
        return min(100, tests * 10)  # up to 100 for 10+ tests
    except Exception:
        return 0.0

def verify_refactor() -> float:
    """Check refactored file exists and has single validate_user function."""
    path = BENCHMARK_DIR / "benchmark_data" / "output" / "refactored.py"
    if not path.exists():
        return 0.0
    content = path.read_text()
    score = 0.0
    if "def validate_user" in content:
        score += 40
    if "validate_user_v1" not in content and "validate_user_v2" not in content and "validate_user_v3" not in content:
        score += 30
    if "rules" in content:
        score += 30
    return score


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
    m = re.search(r"──\s*(\d+)\s+in\s*·\s*(\d+)\s+out", combined)
    if m:
        tokens_in = int(m.group(1))
        tokens_out = int(m.group(2))
    
    iterations = len(re.findall(r"═══ Iter \d+/\d+", combined))
    score = task["score"](combined)
    
    return {
        "wall_time": round(wall, 1), "tokens_in": tokens_in, "tokens_out": tokens_out,
        "iterations": iterations, "score": score, "error": None,
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
    # Hermes doesn't report token usage in oneshot mode — estimate
    score = task["score"](output)
    
    return {
        "wall_time": round(wall, 1), "tokens_in": 0, "tokens_out": 0,
        "iterations": 0, "score": score, "error": None,
    }


# ─── Main ─────────────────────────────────────────────────────────────

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
        print(f"  AIEB v1.0 — {agent}")
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
                print(f"{r['wall_time']:.0f}s | score:{r['score']:.0f} | {r['tokens_in']}→{r['tokens_out']} tok")
            
            agent_results["tasks"].append(r)
            agent_results["total_time"] += r.get("wall_time", 0)
            agent_results["total_score"] += r.get("score", 0)
        
        all_results[agent] = agent_results
    
    # Print comparison
    print(f"\n{'='*60}")
    print(f"  AIEB v1.0 — Comparison")
    print(f"{'='*60}")
    
    if len(agents) == 2:
        o = all_results.get("odek", {})
        h = all_results.get("hermes", {})
        
        print(f"\n  {'Metric':<20} {'odek':>12} {'hermes':>12}")
        print(f"  {'-'*20} {'-'*12} {'-'*12}")
        print(f"  {'Wall time':<20} {o.get('total_time',0):>8.0f}s   {h.get('total_time',0):>8.0f}s")
        print(f"  {'Avg score':<20} {avg_score(o):>8.0f}%   {avg_score(h):>8.0f}%")
        
        for task in TASKS:
            ot = next((t for t in o.get("tasks",[]) if t["id"]==task["id"]), None)
            ht = next((t for t in h.get("tasks",[]) if t["id"]==task["id"]), None)
            os_ = ot["score"] if ot else "-"
            hs_ = ht["score"] if ht else "-"
            print(f"  [{task['id']}] {task['name']:<15} {os_:>8}     {hs_:>8}")
    
    # Save
    out_path = BENCHMARK_DIR / "results.json"
    out_path.write_text(json.dumps({
        "benchmark": "AIEB v1.0",
        "timestamp": datetime.now().isoformat(),
        "results": {k: {"total_time": v["total_time"], "tasks": v["tasks"]}
                     for k, v in all_results.items()}
    }, indent=2))
    print(f"\n  Results: {out_path}")
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
