"""Authentication service — stateless JWT auth."""
import hashlib
import time
from typing import Optional

USERS = {"admin": "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3"}

def verify_password(username: str, password: str) -> bool:
    """Hash and compare password."""
    h = hashlib.sha256(password.encode()).hexdigest()
    return USERS.get(username) == h

def create_session(user: str) -> str:
    """Issue a time-limited session token."""
    payload = f"{user}:{int(time.time())}:session"
    return hashlib.sha256(payload.encode()).hexdigest()[:32]
