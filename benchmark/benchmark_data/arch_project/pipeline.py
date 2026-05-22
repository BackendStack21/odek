"""Pipeline builder."""
from .handlers import AuthHandler, RateLimitHandler, BusinessLogicHandler

def build_pipeline(max_rps=100):
    a = AuthHandler(); r = RateLimitHandler(max_rps); l = BusinessLogicHandler()
    a.set_next(r).set_next(l)
    return a
