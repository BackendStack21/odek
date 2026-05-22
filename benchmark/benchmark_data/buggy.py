"""User authentication — CONTAINS A BUG."""
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
