"""REST API handlers."""
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
