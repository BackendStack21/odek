# P1: Wire Telegram config into odek.json

## Task A — Add telegram section to config structs

Files to modify:
- `internal/config/loader.go` — Add TelegramConfig to FileConfig + ResolvedConfig
- `internal/telegram/config.go` — Add ConfigFromResolved(resolved) function
- `cmd/odek/telegram.go` — Switch from ConfigFromEnv to ConfigFromResolved

Config shape in odek.json:
```json
{
  "telegram": {
    "bot_token": "",
    "allowed_chats": [],
    "allowed_users": [],
    "bot_username": "",
    "poll_interval": 1,
    "poll_timeout": 30,
    "max_msg_length": 4096,
    "daily_token_budget": 1000000,
    "session_ttl_hours": 24,
    "fallback_urls": []
  }
}
```

Priority: env vars > odek.json > defaults (matching existing 4-layer pattern)

## Task B — Wire DailyTokenBudget

Files to modify:
- `internal/telegram/bot.go` — Track daily token usage, reject when over budget
- `internal/telegram/session.go` — Wire budget check before processing

## Task C — Wire SessionTTL into SessionManager

Files to modify:
- `internal/telegram/session.go` — Replace hardcoded 24h with config value
- Pass TTL from config through constructor

## Task D — Wire FallbackURLs into network transport

Files to modify:
- `internal/telegram/network.go` — The FallbackTransport exists; wire FallbackURLs
