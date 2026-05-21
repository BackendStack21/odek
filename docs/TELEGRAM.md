# Telegram Bot Integration

odek includes a built-in Telegram bot that lets you run agent tasks directly from Telegram. The bot supports long-polling, slash commands, voice messages, photo messages, session persistence, and plan management.

## Architecture

```
Telegram Bot API ◄── bot.go (HTTP client)
                          │
                    poller.go (long-polling loop)
                          │
                    handler.go (message dispatcher)
                          │
                    ┌─────┴──────┐
                    │            │
            commands.go    session.go (per-chat agent sessions)
            (slash           │
             commands)   plan.go (.md plan files)
                             │
                      download.go (voice/photo media)
```

The package is self-contained under `internal/telegram/` with 409 tests and 86.9% coverage. All Telegram API calls use the Bot struct, which wraps `net/http` with JSON marshaling and multipart upload support. No external Telegram libraries are used.

## Configuration

### Environment Variables

All configuration flows through `TelegramConfig` and can be set via environment variables:

| Variable | Field | Default |
|---|---|---|
| `ODEK_TELEGRAM_BOT_TOKEN` | Token | — (required) |
| `ODEK_TELEGRAM_ALLOWED_CHATS` | AllowedChats | all |
| `ODEK_TELEGRAM_ALLOWED_USERS` | AllowedUsers | all |
| `ODEK_TELEGRAM_BOT_USERNAME` | BotUsername | — |
| `ODEK_TELEGRAM_POLL_INTERVAL` | PollInterval | 1s |
| `ODEK_TELEGRAM_POLL_TIMEOUT` | PollTimeout | 30s |
| `ODEK_TELEGRAM_MAX_MSG_LENGTH` | MaxMsgLength | 4096 |
| `ODEK_TELEGRAM_DAILY_TOKEN_BUDGET` | DailyTokenBudget | unlimited |
| `ODEK_TELEGRAM_SESSION_TTL_HOURS` | SessionTTL | 24h |
| `ODEK_TELEGRAM_FALLBACK_URLS` | FallbackURLs | — |
| `ODEK_TELEGRAM_LOG_LEVEL` | LogLevel | info |
| `ODEK_TELEGRAM_LOG_FILE` | LogFile | stderr |

### Config Validation

`ValidateConfig` checks:
- **Token** must not be empty
- **PollInterval** must be ≥ 1
- **PollTimeout** must be between 1 and 60
- **MaxMsgLength** must be between 1 and 4096
- **SessionTTL** must be ≥ 1

## Bot API Client (`bot.go`)

The `Bot` struct is a lightweight Telegram Bot API client built on the standard library. It provides methods for all major API endpoints.

### Core Methods

| Method | Endpoint | Purpose |
|---|---|---|
| `GetUpdates` | `getUpdates` | Long-poll for incoming updates |
| `SendMessage` | `sendMessage` | Send a text message (supports parse mode, reply markup, web page preview) |
| `EditMessageText` | `editMessageText` | Edit a previously sent message |
| `SendPhoto` | `sendPhoto` | Send a photo file (multipart upload) |
| `SendVoice` | `sendVoice` | Send a voice note (multipart upload) |
| `SendChatAction` | `sendChatAction` | Show typing/recording status in chat |
| `GetFile` | `getFile` | Get file metadata for download |
| `DownloadFile` | — | Download file bytes from Telegram's file server |
| `AnswerCallbackQuery` | `answerCallbackQuery` | Answer inline keyboard callbacks |
| `SetMyCommands` | `setMyCommands` | Register bot command list |
| `GetMe` | `getMe` | Health check / bot info |
| `CheckDailyBudget` | — | Enforce daily token usage limit |

### Underlying HTTP Helpers

- **`doJSON`** — Sends JSON POST requests and unmarshals the `result` field from Telegram's response envelope
- **`doUpload`** — Sends multipart/form-data POST requests for file uploads (photo, voice)

### Fallback URLs

`SetFallbackURLs` configures alternate Telegram API endpoints. If the primary endpoint is unreachable, the bot falls through to the next URL in the list. This is useful for regions where `api.telegram.org` may be blocked.

### Daily Token Budget

`SetDailyTokenBudget` and `CheckDailyBudget` implement a simple daily token usage tracker:
- Usage is persisted to `~/.odek/telegram_token_usage_<YYYY-MM-DD>`
- Budget resets automatically each calendar day
- Returns an error if the total exceeds the configured budget
- No-op when budget is 0 (unlimited)

## Long-Polling (`poller.go`)

The `Poller` struct implements Telegram's long-polling update mechanism:

- **Offset tracking**: updates are acknowledged by advancing the offset past the highest received update ID, preventing duplicate processing
- **Configurable timeout**: default 30s long-poll (Telegram holds the connection open until updates arrive or the timeout expires)
- **Polling interval**: 1s between poll cycles (configurable via `PollInterval`)

```go
poller := telegram.NewPoller(bot)
poller.SetLogger(logger)

for {
    updates, err := poller.Poll(ctx)
    if err != nil {
        continue // log and retry
    }
    for _, update := range updates {
        handler.Handle(ctx, update)
    }
}
```

## Message Handler (`handler.go`)

The `Handler` struct routes incoming updates to the appropriate callback based on message type. It is the bridge between raw Telegram updates and the agent.

### Callbacks

| Callback | Trigger | Signature |
|---|---|---|
| `OnTextMessage` | Plain text message | `(chatID int64, text string) (string, error)` |
| `OnCommand` | Slash command (e.g. `/start`) | `(chatID int64, command, args string) (string, error)` |
| `OnVoiceMessage` | Voice message (OGG) | `(chatID int64, fileID string) (string, error)` |
| `OnPhotoMessage` | Photo message | `(chatID int64, fileIDs []string) (string, error)` |
| `OnCallbackQuery` | Inline keyboard callback | `(chatID int64, callbackData string) (string, error)` |

All callbacks return a response string (may be empty) and an error. The `Handle` method:
1. Sends `SendChatAction("typing")` immediately
2. Dispatches to the appropriate callback
3. Sends the response text back to the chat
4. Splits long messages exceeding `MaxMsgLength` into chunks

### Access Control

`HandlerConfig` supports:
- **AllowedChats** — restrict to specific chat IDs (empty = allow all)
- **AllowedUsers** — restrict to specific user IDs (empty = allow all)
- **BotUsername** — for `@mention` detection in groups

### Inline Keyboards

The handler uses `sync.Map` for `TelegramApprover` instances, keyed by `chatID`. This allows the agent to send inline keyboard approval requests (yes/no) and receive responses via callback queries. The handler intercepts callback queries matching pending approval requests before dispatching to `OnCallbackQuery`.

## Slash Commands (`commands.go`)

### Built-in Commands

| Command | Description |
|---|---|
| `/start` | Welcome message and bot introduction |
| `/help` | Show all available commands with descriptions |
| `/new` | Reset the current conversation (clear context) |
| `/stats` | Show session statistics (turn count, model used, etc.) |
| `/stop` | Cancel a running agent task |
| `/mode` | Toggle agent modes (sandbox, verbose) |
| `/restart` | Gracefully restart the bot process |
| `/plan <description>` | Create a new plan from a natural language description |
| `/plans` | List all saved plans |
| `/plan-view <slug>` | View a specific plan's content |
| `/plan-delete <slug>` | Delete a saved plan |
| `/sessions` | List recent conversation sessions |
| `/resume <session_id>` | Resume a previous session by ID |
| `/prune [days]` | Clean up old sessions (default: 30 days) |

### Architecture

Commands are registered via `CommandDescriptor` structs in the `DefaultCommands` slice. Each descriptor has:
- **Command** — the slash command name (without `/`)
- **Description** — shown in `/help` and registered via `SetMyCommands`
- **Handler** — function `func(args string) (string, error)`

The handler uses `init()` to populate `DefaultCommands`, avoiding initialization cycles between the command definitions and the handler functions they reference.

## Session Manager (`session.go`)

The `SessionManager` manages per-chat Telegram agent conversations, backed by the existing [`session.Store`](SESSIONS.md).

### How It Works

1. Each Telegram chat gets a session identified by `tg-<chatID>`
2. Sessions are persisted to `~/.odek/sessions/tg-<chatID>.json`
3. An in-memory cache (`map[int64]*ChatSession`) avoids redundant disk reads
4. Session TTL (default 24h) controls inactivity timeout
5. Active sessions survive bot restarts — on reconnect, the session is loaded from disk

### Key Methods

| Method | Purpose |
|---|---|
| `GetOrCreate(chatID)` | Get existing session or create new one |
| `Append(chatID, messages)` | Add messages to session |
| `Save(chatID)` | Persist session to disk |
| `ClearMessages(chatID)` | Clear conversation history (keeps metadata) |

### Clarify Channels

The `clarifyChannels` sync.Map provides per-chat channels for the agent to ask the user questions and receive answers asynchronously. This bridges the gap between the agent's synchronous `clarify` tool call and Telegram's asynchronous message flow.

## Plan Management (`plan.go`)

Plans are stored as markdown files in `~/.odek/plans/<slug>.md`. Each plan is created from a natural language description and persisted for later review.

### Key Functions

| Function | Purpose |
|---|---|
| `Slugify(description)` | Convert description to filesystem-safe slug (max 60 chars) |
| `ListPlans(limit)` | List plans sorted by modification time (newest first) |
| `ReadPlan(slug)` | Load plan content by exact slug or prefix match |
| `DeletePlan(slug)` | Delete plan by exact slug or prefix match |
| `MostRecentPlan()` | Return the most recently modified plan's content |
| `EnsurePlansDir()` | Create plans directory if it doesn't exist |

### Slug Generation

Slug generation (`slugify`) collapses a description into a lowercase, hyphen-separated identifier:
- Strips non-alphanumeric characters
- Collapses whitespace to hyphens
- Truncates to 60 characters max
- Falls back to `"plan"` if no valid characters remain

### Prefix Matching

`ReadPlan` and `DeletePlan` support prefix matching: if the given slug uniquely prefixes an existing plan file, it matches. Ambiguous prefixes return an error listing the matching plans.

## Media Download (`download.go`)

Supports downloading voice messages and photos from Telegram to the local filesystem.

### Media Directory

Media files are saved to `~/.odek/media/` (created automatically on first download).

### DownloadVoice

- Gets file metadata via `GetFile`
- Downloads raw bytes via `DownloadFile`
- Saves as `voice_<truncated_fileID>.<ext>` (default extension: `.ogg`)
- Truncates fileID to 16 chars for filenames

### DownloadPhoto

- Takes a slice of `PhotoSize` IDs (Telegram sends multiple sizes)
- Uses the last (largest) photo size
- Saves as `photo_<truncated_fileID>.<ext>` (default extension: `.jpg`)
- Same fileID truncation as voice downloads

## Types (`types.go`)

The package defines Telegram API types used throughout:

| Type | Purpose |
|---|---|
| `Bot` | API client struct |
| `Message` | Incoming/outgoing message |
| `Update` | Incoming update (message, callback query, etc.) |
| `User` | Telegram user |
| `File` | File metadata for download |
| `PhotoSize` | Photo with dimensions |
| `BotCommand` | Command descriptor for `SetMyCommands` |
| `SendOpts` | Optional parameters for `SendMessage` |
| `ReplyKeyboardMarkup` | Keyboard layout |
| `InlineKeyboardMarkup` | Inline keyboard layout |
| `InlineKeyboardButton` | Inline keyboard button |
| `CallbackQuery` | Inline keyboard callback |
| `HandlerConfig` | Message handler configuration |
| `TelegramConfig` | Full bot configuration |
| `PlanInfo` | Plan file summary |
| `ChatSession` | Per-chat agent session |
| `SessionManager` | Session lifecycle manager |
| `Logger` | Logging interface |
| `Poller` | Long-polling update fetcher |

## Testing

The Telegram package has **409 tests** with **86.9% coverage**. Tests use:
- `httptest.NewServer` to mock Telegram API responses
- HTTP handler functions for each API endpoint (getFile, sendMessage, sendDocument, etc.)
- `t.TempDir()` + `t.Setenv("HOME", ...)` for filesystem isolation
- Long fileID truncation tests for voice/photo downloads
- Plan CRUD tests with prefix matching, ambiguous matches, and error paths
- Session manager tests with TTL expiry and cache behavior

```bash
# Run all Telegram tests
go test ./internal/telegram/ -v -count=1

# With coverage
go test -cover ./internal/telegram/
```
