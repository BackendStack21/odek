"""Data models."""
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
