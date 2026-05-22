#!/usr/bin/env python3
"""AIEB v1.0 — Complete benchmark. Run: `python3 benchmark/run_aieb.py`"""
import json, os, sys, time, subprocess, re, shutil, importlib.util
from pathlib import Path
from datetime import datetime

BENCHMARK_DIR = Path(__file__).parent.resolve()
ODEK_BIN = os.path.expanduser("~/projects/odek/odek")
ODEK_API_KEY = os.environ.get("ODEK_API_KEY") or open("/tmp/.aieb_odek_key").read().strip()

# ─── Setup ────────────────────────────────────────────────────────────

def setup():
    """Create benchmark data + clean output."""
    data = BENCHMARK_DIR / "benchmark_data"
    data.mkdir(exist_ok=True)
    
    # Clean output
    out = data / "output"
    if out.exists():
        shutil.rmtree(out)
    out.mkdir()
    
    # Tier 1: Code Understanding
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
    
    arch = data / "arch_project"
    arch.mkdir(exist_ok=True)
    (arch / "__init__.py").write_text("")
    (arch / "base.py").write_text('''"""Chain of Responsibility base."""
from abc import ABC, abstractmethod
from typing import Any

class BaseHandler(ABC):
    def __init__(self): self._next: BaseHandler | None = None
    def set_next(self, h: "BaseHandler") -> "BaseHandler": self._next = h; return h
    @abstractmethod
    def handle(self, r: dict[str, Any]) -> dict[str, Any] | None: ...
    def _pass(self, r: dict[str, Any]) -> dict[str, Any] | None:
        return self._next.handle(r) if self._next else None
''')
    (arch / "handlers.py").write_text('''"""Concrete handlers."""
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
''')
    (arch / "pipeline.py").write_text('''"""Pipeline builder."""
from .handlers import AuthHandler, RateLimitHandler, BusinessLogicHandler

def build_pipeline(max_rps=100):
    a = AuthHandler(); r = RateLimitHandler(max_rps); l = BusinessLogicHandler()
    a.set_next(r).set_next(l)
    return a
''')
    
    # Tier 2: Search Target
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
    if len(text) <= max_len: return text
    return text[:max_len-3] + "..."

def merge_dicts(*dicts: dict) -> dict:
    result: dict[str, Any] = {}
    for d in dicts: result.update(d)
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
    def list_users(self) -> list[User]: return []

class PostAPI:
    def create_post(self, data: dict[str, Any]) -> Post:
        return Post(**data)
    def publish_post(self, post_id: int) -> Post:
        # HACK: hardcoded for demo
        return Post(id=post_id, author_id=1, title="demo", content="...", published=True)
''')
    
    # Tier 3: Generate
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
''')
    print("✓ Benchmark data ready")

# ─── Tasks ────────────────────────────────────────────────────────────

TASKS = [
    # Tier 1: Code Understanding
    {"id":"1.1","tier":1,"name":"explain","max_iter":5,
     "prompt":"Read benchmark_data/explain_me.py. Explain in ONE sentence what process_events does. List all edge cases it handles.\nOutput:\nPURPOSE: <sentence>\nEDGE_CASES: <list>"},
    {"id":"1.2","tier":1,"name":"find_bug","max_iter":5,
     "prompt":"Read benchmark_data/buggy.py. There is exactly ONE bug. Identify: file, line, the bug, the fix.\nOutput:\nFILE: <name>\nLINE: <num>\nBUG: <desc>\nFIX: <code>"},
    {"id":"1.3","tier":1,"name":"architecture","max_iter":8,
     "prompt":"Read ALL files in benchmark_data/arch_project/. Identify: architecture pattern, key abstractions, communication mechanism.\nOutput:\nPATTERN: <name>\nABSTRACTIONS: <list>\nCOMMUNICATION: <desc>"},
    # Tier 2: Tool Orchestration
    {"id":"2.1","tier":2,"name":"exports","max_iter":10,
     "prompt":"In benchmark_data/search_target/, list all exported (non-underscore) functions and classes grouped by file.\nOutput: FILE: <name>\\n  FUNCTIONS: <list>\\n  CLASSES: <list>"},
    {"id":"2.2","tier":2,"name":"count_loc","max_iter":8,
     "prompt":"In benchmark_data/search_target/, count non-blank non-comment lines of code per file + total. Use shell (grep, wc).\nOutput: <file>: <N> LOC\\n...\\nTOTAL: <N>"},
    {"id":"2.3","tier":2,"name":"find_todos","max_iter":8,
     "prompt":"In benchmark_data/search_target/, find all TODO/FIXME/HACK comments. Report: file, line, type, text.\nOutput: <file>:<line>: <TYPE> <text>"},
    # Tier 3: Code Generation
    {"id":"3.1","tier":3,"name":"write_fn","max_iter":8,
     "prompt":"Write Python function merge_intervals(intervals) to benchmark_data/output/merge_intervals.py.\nTakes list of [start,end] int pairs, returns merged non-overlapping intervals sorted by start.\n[[1,3],[2,6],[8,10]] → [[1,6],[8,10]]. Handle: empty, single, all overlapping, no overlap, negatives.\nWrite ONLY the function file with docstring. Importable."},
    {"id":"3.2","tier":3,"name":"add_tests","max_iter":10,
     "prompt":"Read benchmark_data/under_tested.py. Write tests at benchmark_data/output/test_under_tested.py for parse_config().\nCover: valid config, empty, missing fields, malformed JSON, edge cases.\nStdlib only (no pytest). Run with: python3 benchmark_data/output/test_under_tested.py"},
    {"id":"3.3","tier":3,"name":"refactor","max_iter":10,
     "prompt":"Read benchmark_data/refactor_me.py. Refactor validate_user_v1/v2/v3 into ONE validate_user(data, rules) where rules is dict of field→validator.\nWrite to benchmark_data/output/refactored.py. Original imports intact."},
]

# ─── Scoring ──────────────────────────────────────────────────────────

def score_keywords(output, required, bonus=[]):
    lower = output.lower()
    r = sum(1 for k in required if k.lower() in lower) / max(len(required), 1) * 80
    b = sum(1 for k in bonus if k.lower() in lower) / max(len(bonus), 1) * 20 if bonus else 0
    return round(r + b, 1)

def score_loc(output, expected):
    m = re.search(r"TOTAL\s*[:=-]?\s*(\d+)", output, re.I)
    if not m: return 0
    diff = abs(int(m.group(1)) - expected) / expected
    return round(max(0, 100 - diff * 100), 1)

def verify_merge(_=None):
    path = BENCHMARK_DIR / "benchmark_data" / "output" / "merge_intervals.py"
    if not path.exists(): return 0
    try:
        spec = importlib.util.spec_from_file_location("mi", path)
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        fn = mod.merge_intervals
        tests = [
            ([[1,3],[2,6],[8,10],[15,18]], [[1,6],[8,10],[15,18]]),
            ([], []), ([[1,4]], [[1,4]]),
            ([[1,4],[2,3],[3,6]], [[1,6]]),
            ([[1,2],[3,4]], [[1,2],[3,4]]),
            ([[-5,0],[-3,2],[1,5]], [[-5,5]]),
        ]
        return round(sum(1 for i,e in tests if fn(i)==e)/len(tests)*100, 1)
    except: return 0

def verify_tests(_=None):
    path = BENCHMARK_DIR / "benchmark_data" / "output" / "test_under_tested.py"
    if not path.exists(): return 0
    try:
        r = subprocess.run([sys.executable, str(path)], capture_output=True, text=True, timeout=10)
        if r.returncode != 0: return 0
        n = len(re.findall(r"def test_", path.read_text()))
        return min(100, n * 10)
    except: return 0

def verify_refactor(_=None):
    path = BENCHMARK_DIR / "benchmark_data" / "output" / "refactored.py"
    if not path.exists(): return 0
    c = path.read_text()
    s = 0
    if "def validate_user" in c: s += 40
    if "v1" not in c and "v2" not in c and "v3" not in c: s += 30
    if "rules" in c: s += 30
    return s

SCORERS = {
    "1.1": lambda out: score_keywords(out, ["dedup","sort","window","aggregate"], ["empty","null"]),
    "1.2": lambda out: score_keywords(out, ["=","==","buggy.py"]),
    "1.3": lambda out: score_keywords(out, ["chain","responsibility"], ["BaseHandler","handler"]),
    "2.1": lambda out: score_keywords(out, ["UserAPI","PostAPI","find_user_by_email","slugify"]),
    "2.2": lambda out: score_loc(out, 48),
    "2.3": lambda out: score_keywords(out, ["TODO","FIXME","HACK"], ["avatar","implement","hardcoded"]),
    "3.1": verify_merge,
    "3.2": verify_tests,
    "3.3": verify_refactor,
}

# ─── Runner ───────────────────────────────────────────────────────────

def run_odek(task):
    cmd = [ODEK_BIN, "run", "--model", "deepseek-v4-flash",
           "--max-iter", str(task["max_iter"]), "--no-color", task["prompt"]]
    env = {"PATH": os.environ["PATH"], "HOME": os.environ["HOME"],
           "ODEK_API_KEY": ODEK_API_KEY}
    
    start = time.time()
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=300,
                          cwd=str(BENCHMARK_DIR), env=env)
    except subprocess.TimeoutExpired:
        return {"error": "timeout", "wall_time": time.time()-start, "score": 0}
    except Exception as e:
        return {"error": str(e), "wall_time": time.time()-start, "score": 0}
    
    wall = time.time() - start
    combined = (r.stderr or "") + "\n" + (r.stdout or "")
    
    m = re.search(r"──\s*(\d+)\s+in\s*·\s*(\d+)\s+out", combined)
    tokens_in = int(m.group(1)) if m else 0
    tokens_out = int(m.group(2)) if m else 0
    iters = len(re.findall(r"═══ Iter \d+/\d+", combined))
    score = SCORERS[task["id"]](combined)
    
    return {"wall_time": round(wall,1), "tokens_in": tokens_in, "tokens_out": tokens_out,
            "iterations": iters, "score": score, "error": None}

# ─── Main ─────────────────────────────────────────────────────────────

def main():
    print("AIEB v1.0 — odek + deepseek-v4-flash\n")
    setup()
    
    total_time = 0
    total_ti = total_to = 0
    total_score = 0
    table = []
    
    for task in TASKS:
        tier = f"T{task['tier']}"
        print(f"  [{tier}.{task['id']}] {task['name']:12s}...", end=" ", flush=True)
        r = run_odek(task)
        
        if r.get("error"):
            print(f"❌ {r['error']}")
            table.append((task["id"], task["name"], "ERR", 0, 0, 0, 0))
            continue
        
        print(f"{r['wall_time']:4.0f}s | {r['score']:5.0f}% | {r['tokens_in']:>6}→{r['tokens_out']:<5} tok | {r['iterations']} iter")
        total_time += r["wall_time"]
        total_ti += r["tokens_in"]
        total_to += r["tokens_out"]
        total_score += r["score"]
        table.append((task["id"], task["name"], r["wall_time"], r["score"], r["tokens_in"], r["tokens_out"], r["iterations"]))
    
    n = len(TASKS)
    print(f"\n{'='*60}")
    print(f"  AIEB v1.0 Results")
    print(f"{'='*60}")
    print(f"  {'Task':<20} {'Time':>6} {'Score':>6} {'Tok in':>8} {'Tok out':>8} {'Iter':>5}")
    print(f"  {'-'*20} {'-'*6} {'-'*6} {'-'*8} {'-'*8} {'-'*5}")
    for tid, name, t, s, ti, to, it in table:
        print(f"  [{tid}] {name:<15} {t:>5.0f}s {s:>5.0f}% {ti:>7,} {to:>7,} {it:>4}")
    print(f"  {'─'*50}")
    print(f"  {'TOTAL':<20} {total_time:>5.0f}s {total_score/n:>5.0f}% {total_ti:>7,} {total_to:>7,}")
    print(f"\n  Score: {total_score/n:.0f}% | Time: {total_time:.0f}s | Tokens: {total_ti:,} in / {total_to:,} out")
    
    # Save
    (BENCHMARK_DIR / "results.json").write_text(json.dumps({
        "benchmark": "AIEB v1.0",
        "timestamp": datetime.now().isoformat(),
        "agent": "odek", "model": "deepseek-v4-flash",
        "total_time": total_time, "avg_score": round(total_score/n, 1),
        "total_tokens_in": total_ti, "total_tokens_out": total_to,
        "tasks": [{"id": tid, "name": name, "time": t, "score": s, "tokens_in": ti, "tokens_out": to, "iterations": it}
                  for tid, name, t, s, ti, to, it in table]
    }, indent=2))
    print(f"\n  Results saved to benchmark/results.json")

if __name__ == "__main__":
    main()
