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

The package is self-contained under `internal/telegram/` and well-tested under `-race`. All Telegram API calls use the Bot struct, which wraps `net/http` with JSON marshaling, multipart upload support, exponential backoff retry, and typed error handling. No external Telegram libraries are used.

## Configuration

### Environment Variables

All configuration flows through `TelegramConfig` and can be set via environment variables:

| Variable | Field | Default |
|---|---|---|
| `ODEK_TELEGRAM_BOT_TOKEN` | Token | — (required) |
| `ODEK_TELEGRAM_ALLOWED_CHATS` | AllowedChats | — (see below) |
| `ODEK_TELEGRAM_ALLOWED_USERS` | AllowedUsers | — (see below) |
| `ODEK_TELEGRAM_ALLOW_ALL` | AllowAllUsers | false |
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

- **`doJSON`** — Sends JSON POST requests with exponential backoff retry. Unmarshals the `result` field from Telegram's response envelope.
- **`doUpload`** — Sends multipart/form-data POST requests for file uploads (photo, voice). Buffers the entire file in memory to enable retry without re-reading from disk.

See [Error Handling & Retry](#error-handling--retry) above for retry strategy, `TelegramError` type, and `isRetryableNetworkError` details.

### Fallback URLs

`SetFallbackURLs` configures alternate Telegram API endpoints. If the primary endpoint is unreachable, the bot falls through to the next URL in the list. This is useful for regions where `api.telegram.org` may be blocked or for pointing at a local [Telegram Bot API server](https://core.telegram.org/bots/api#using-a-local-bot-api-server).

Fallback URLs are validated on startup. Only the following are accepted:

- HTTPS hosts under `telegram.org` (e.g. `https://api.telegram.org`, `https://fallback.api.telegram.org`).
- Loopback addresses for local Bot API servers (e.g. `http://127.0.0.1:8081`, `http://localhost:8081`, `http://[::1]:8081`).

Non-HTTPS, non-loopback, or non-Telegram URLs are rejected to prevent the bot token from leaking to third parties, because the fallback transport rewrites the request host while preserving the original path (`/bot<token>/<method>`).

### Daily Token Budget

`SetDailyTokenBudget` and `CheckDailyBudget` implement a simple daily token usage tracker:
- Usage is persisted to `~/.odek/telegram_token_usage_<YYYY-MM-DD>`
- Budget resets automatically each calendar day
- **Pre-flight check**: before each agent run, a lightweight check verifies the budget isn't already exhausted, avoiding wasted API calls
- **Post-run billing**: after each agent run, actual token usage (`input + output`) is deducted from the daily budget
- Returns a warning message to the chat if the budget is exceeded, but still delivers the response
- No-op when budget is 0 (unlimited, the default)
- Configurable via `ODEK_TELEGRAM_DAILY_TOKEN_BUDGET` env var or `daily_token_budget` in `odek.json`

### Error Handling & Retry

`doJSON` and `doUpload` implement exponential backoff retry with these characteristics:

- **Up to 4 retries** (5 total attempts), with 1s → 2s → 4s → 8s backoff
- **Transient errors that get retried:**
  - Network errors with `net.Error.Timeout()` or `net.Error.Temporary()` (timeouts, connection refused). Non-transient network errors (DNS failures) are returned immediately.
  - HTTP 429 (rate limit) — server indicates client should slow down
  - HTTP 5xx (server error) — may be temporary backend issues
- **Fatal errors that are NOT retried:** HTTP 401, 403, 409, and other 4xx client errors (except 429)

### Typed API Errors

Telegram API errors use the `TelegramError` type instead of opaque strings:

```go
type TelegramError struct {
    Method      string // API method that failed (e.g. "sendMessage")
    Description string // Human-readable error description
    Code        int    // HTTP status code
}
```

`IsFatalAPIError(err)` uses `errors.As` for type-safe code extraction — callers don't depend on error string format. Fatal codes: 401 (Unauthorized), 403 (Forbidden), 409 (Conflict — duplicate polling).

### Upload Memory Tradeoff

`doUpload` buffers the entire file in memory before sending (`bytes.Buffer`). This enables retry without re-reading from disk. Telegram's 50 MB upload limit makes this acceptable for bot use cases.

## Health Server (`health.go`)

The `HealthServer` provides an HTTP `/health` endpoint for monitoring systems.

- **Address**: configurable via `--telegram-health-addr` (e.g. `127.0.0.1:9090`). Empty string disables.
- **`ready` state**: uses `atomic.Bool` for thread-safe access. `/health` returns `503 Service Unavailable` with `{"status": "starting"}` until `SetReady()` is called (after polling begins).
- **`200 OK`**: `{"status": "ok", "uptime_seconds": <N>}` once polling is active.
- Graceful shutdown on context cancellation.

## Long-Polling (`poller.go`)

The `Poller` struct implements Telegram's long-polling update mechanism:

- **Offset tracking**: updates are acknowledged by advancing the offset past the highest received update ID, preventing duplicate processing
- **Configurable timeout**: default 30s long-poll (Telegram holds the connection open until updates arrive or the timeout expires)
- **Polling interval**: 1s between poll cycles (configurable via `PollInterval`)
- **Exponential backoff**: consecutive poll errors trigger increasing delays: `interval × 2^errors`, capped at `60 × interval`. Error count clamps at 30 to prevent integer overflow in the bit-shift. A successful poll resets the error counter.
- **Context cancellation**: `GetUpdatesContext(ctx, ...)` and `Poll(ctx)` use context-aware HTTP requests (`http.NewRequestWithContext`). When the context is cancelled (e.g. SIGINT/SIGTERM), the long-poll HTTP call is **aborted immediately** instead of blocking until the poll timeout expires. The retry loop also checks `ctx.Done()` during backoff sleeps, so cancellation during retries returns promptly.

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
| `OnTextMessage` | Plain text message | `(chatID int64, messageID int, text string, forwarded bool) (string, error)` |
| `OnCommand` | Slash command (e.g. `/start`) | `(chatID int64, command, args string) (string, error)` |
| `OnVoiceMessage` | Voice message (OGG Opus) | `(chatID int64, messageID int, fileID string) (string, error)` |
| `OnPhotoMessage` | Photo message | `(chatID int64, messageID int, fileIDs []string, caption string) (string, error)` |
| `OnCallbackQuery` | Inline keyboard callback | `(chatID int64, callbackData string) (string, error)` |

All callbacks return a response string (may be empty) and an error. The `Handle` method:
1. Sends `SendChatAction("typing")` immediately
2. Dispatches to the appropriate callback
3. Sends the response text back to the chat
4. Splits long messages exceeding `MaxMsgLength` into chunks

### Access Control

`HandlerConfig` supports:
- **AllowedChats** — restrict to specific chat IDs
- **AllowedUsers** — restrict to specific user IDs
- **AllowAllUsers** — explicit opt-in to run with no allowlist (see below)
- **BotUsername** — for `@mention` detection in groups

> **Fail-closed authorization.** The bot **refuses to start** if neither
> `ODEK_TELEGRAM_ALLOWED_CHATS` nor `ODEK_TELEGRAM_ALLOWED_USERS` is set, so an
> open bot — where any Telegram user could drive the agent and its shell/file
> tools — can never be deployed by accident. To intentionally run an open bot,
> set `ODEK_TELEGRAM_ALLOW_ALL=true` (logged as a loud warning at startup). At
> runtime, with both allowlists empty and `AllowAllUsers` unset, every update is
> denied.

### Inline Keyboards

The handler uses `sync.Map` for `TelegramApprover` instances, keyed by `chatID`. This allows the agent to send inline keyboard approval requests (yes/no) and receive responses via callback queries. The handler intercepts callback queries matching pending approval requests before dispatching to `OnCallbackQuery`.

## Slash Commands (`commands.go`)

### Built-in Commands

| Command | Description |
|---|---|
| `/start` | Welcome message and bot introduction |
| `/help` | Show all available commands with descriptions |
| `/new` | Archive the current session and start a fresh conversation. Archived sessions are timestamped (`tg-<chatID>-<YYYYMMDD>-<HHMMSS>`) and remain visible via `odek session list` |
| `/stats` | Show session statistics (turn count, model used, etc.) |
| `/stop` | Cancel a running agent task |
| `/mode` | Show current agent modes (interaction_mode, sandbox, skills) |
| `/restart` | Gracefully restart the bot process. Restricted to operator chats/users and rate-limited to once per 60 seconds. |
| `/plan <description>` | Create a new plan from a natural language description |
| `/plans` | List all saved plans |
| `/plan-view <slug>` | View a specific plan's content |
| `/plan-delete <slug>` | Delete a saved plan |
| `/sessions` | List recent conversation sessions |
| `/resume <session_id>` | Resume a previous session by ID |
| `/prune [days]` | Clean up old sessions (default: 30 days) |
| `/schedules` | List scheduled tasks (id, on/off, cron, next fire, last status) |
| `/schedule <subcommand>` | Manage scheduled tasks — `add`, `rm`, `enable`, `disable`, `run`, `next`, `view`. Mutating commands are restricted to configured operator chats/users. See [Managing schedules from Telegram](SCHEDULES.md#managing-from-telegram) |

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
5. **Session recall** — the user message is saved to the session store *before* the agent loop runs, enabling `session_search` to find the current conversation's data during the same turn
6. Active sessions survive bot restarts — on reconnect, the session is loaded from disk
7. **/now archives** — using `/new` archives the current session with a timestamped ID (`tg-<chatID>-<YYYYMMDD>-<HHMMSS>`) before starting fresh. Archived sessions remain on disk and are visible via `odek session list`.

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

Supports downloading voice messages, photos, and documents from Telegram to the local filesystem.

### Media Directory

Media files are saved to `~/.odek/media/` (created automatically on first download).

### Download limits

- **Per-file cap:** `telegram.max_download_size` (default **5 MiB**). Files larger than the cap are rejected before they are written to disk. Set to `-1` to disable.
- **Per-chat quota:** `telegram.media_quota_per_chat` (default **disabled**). When set to a positive byte value, the bot refuses downloads that would push that chat's total stored media above the quota.
- Filenames include the chat ID (`voice_chat<chatID>_<hash>.ogg`, `photo_chat<chatID>_<hash>.jpg`, `chat<chatID>_<filename>`) so the quota can be enforced per chat.

### DownloadVoice

- Gets file metadata via `GetFile`
- Downloads raw bytes via `DownloadFile`
- Saves as `voice_chat<chatID>_<hash>.<ext>` (default extension: `.ogg`)
- `<hash>` is the first 16 hex chars of the SHA-256 of the full Telegram `file_id`

### DownloadPhoto

- Takes a slice of `PhotoSize` IDs (Telegram sends multiple sizes)
- Uses the last (largest) photo size
- Saves as `photo_chat<chatID>_<hash>.<ext>` (default extension: `.jpg`)
- Hashing the **full** id avoids a collision: Telegram photo `file_id`s share a long constant prefix (e.g. `AgACAgIAAxkBAAI…`), so raw-truncating to 16 chars produced identical filenames for different photos — each overwrote the last, making the bot report a photo as "already processed". Voice downloads use the same scheme.

### Auto-Describe (Photo → Vision)

When `vision.auto_describe: true` is set in config (default) and the MiniCPM-V model is available, photos are automatically run through the local vision model before reaching the agent:

```
Photo received → DownloadPhoto (largest size to disk)
               → vision tool (llama-mtmd-cli, focused by the caption if any)
               → extracted description + the caption injected as the user message
               → agent answers the request using the description
```

If the photo has a **caption**, that text becomes the user's request and also focuses the vision extraction. Both the caption passed to the local vision model and the description returned to the main agent are wrapped in `<untrusted_content>` boundaries (external text is untrusted input).

**Fallback:** If auto-describe is disabled or the vision model fails, the agent receives the file path (and caption, if any) with a suggestion to use the `vision` tool manually.

**Docker:** the official image bundles `llama-mtmd-cli` and MiniCPM-V 4.6, with `auto_describe` enabled in the shipped configs — so photo understanding works out of the box. See [../docker/README.md](../docker/README.md#image--video-understanding-out-of-the-box).

### Auto-Transcribe (Voice → Text)

When `transcription.auto_transcribe: true` is set in config and whisper is installed, voice messages are automatically transcribed into text before reaching the agent:

```
Voice message received → DownloadVoice (OGG Opus to disk)
                        → convertToWAV (ffmpeg: OGG→16kHz mono WAV)
                        → whisper.cpp (local transcription)
                        → transcribed text injected as user message
```

**OGG Opus handling:** whisper.cpp uses `dr_wav`/`dr_mp3` internally and does not support OGG Opus. The transcribe tool auto-detects unsupported formats and converts via ffmpeg. If ffmpeg is unavailable, the original file is passed to whisper which produces a clear error message.

**Fallback:** If auto-transcribe fails (ffmpeg unavailable, corrupt audio, whisper error), the agent receives the file path with a suggestion to use the `transcribe()` tool manually.

**Docker:** the official image bundles the whisper.cpp CLI, the `tiny` model, and ffmpeg, with `auto_transcribe` enabled in the shipped configs — so voice transcription works out of the box with no host install. See [../docker/README.md](../docker/README.md#voice-transcription-out-of-the-box).

### Tool Progress (Narrator)

Tool progress shows what the agent is doing in real time. Controlled by the `tool_progress` config field (independent from `interaction_mode`):

| Mode | Behavior |
|------|----------|
| `all` (default) | Reasoning-first progress: LLM's first reasoning sentence as header, then individual tool previews below. Eg: `"Let me search that file..."` then `📝 read_file: "main.go"`. With edit throttling (1.5s), dedup, and flood-control fallback |
| `new` | Like `all` but reasoning header only updates on new iteration |
| `verbose` | Raw tool arguments as separate messages — `📝 `read_file` {"path":"main.go"}` then `📝 `read_file` ✅ (12ms, 2KB)` on completion, including execution latency |
| `off` | No per-tool progress messages — just the thinking preamble and final answer |

**Key features:**
- **Reasoning-first progress** — the first sentence of the LLM's internal reasoning (under 20 words) appears at the top of the progress bubble, followed by individual tool previews. The LLM is prompted to make this sentence user-facing, specific, and engaging
- **Language matching** — the bot always replies in the same language the user writes in, including the thinking message and progress indicator
- **Smart previews** — extracts meaningful context: filename for file tools, command for shell, URL for browser, query for memory, filename for transcribe, file path for vision, query for web_search
- **Edit throttling** — 1.5s minimum between edits prevents Telegram flood control (429 errors)
- **Tool dedup** — if the same tool runs N times in a row (common with parallel batches), shows `📝 read_file: "main.go" (×5)` instead of 5 identical lines
- **Flood fallback** — if an edit fails with "flood" or "retry after", automatically switches to sending new messages
- **Content reset** — when `send_message` fires mid-run, the progress bubble resets below the sent content
- **Cleanup** — progress message deleted after final answer (configurable via `tool_progress_cleanup: false`)

Config example:
```json
{
  "tool_progress": "all",
  "tool_progress_cleanup": true
}
```

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
| `TelegramError` | Typed API error with `Method`, `Description`, `Code` |
| `TelegramConfig` | Full bot configuration |
| `HealthServer` | HTTP health check server |
| `PlanInfo` | Plan file summary |
| `ChatSession` | Per-chat agent session |
| `SessionManager` | Session lifecycle manager |
| `Logger` | Logging interface |
| `Poller` | Long-polling update fetcher |

## Process Lifecycle

### Singleton Lock

The bot writes its PID to `~/.odek/telegram.pid` on startup. If a stale PID file exists from a previous instance, the new process kills it (SIGTERM → 5s grace → SIGKILL) before taking over. This prevents 409 Conflict errors from dual polling.

### Graceful Restart

Restarts use a **three-phase graceful shutdown** before spawning a child process:

```
Phase 1: NOTIFY + CANCEL          Phase 2: BOUNDED DRAIN       Phase 3: SPAWN + EXIT
┌─────────────────────────┐       ┌──────────────────────┐     ┌──────────────────────┐
│ Set restart flag        │       │ activeTaskWG.Wait()  │     │ writeRestartMarker()  │
│ Iterate active chats:   │  ──►  │   with 15s timeout   │ ──► │   (with active chat   │
│   • Send "restarting"   │       │   (or all done)      │     │    IDs)               │
│   • Cancel ctx          │       │                      │     │ spawnChild()          │
└─────────────────────────┘       └──────────────────────┘     │ os.Exit(0)            │
                                                                └──────────────────────┘
```

The restart is triggered by:
- **`/restart` command** — sends `SIGHUP` to self
- **`SIGHUP` signal** — external `kill -HUP <pid>`
- **`build-and-restart-telegram.sh`** — sends `SIGHUP`, waits for old process to exit

During restart:

1. **Active tasks are notified** — each chat with a running agent receives "🔄 Bot restarting — your current task was interrupted." The message includes a prompt to use `/new` after restart.

2. **Agent contexts are cancelled** — the same cancel mechanism used by `/stop`. Each agent loop's `RunWithMessages` returns with `context.Canceled`, the partial session is saved, and the goroutine exits cleanly.

3. **New messages are rejected** — any message arriving while restart is in progress gets "⏳ Bot is restarting — please try again in a few seconds." The message is not lost (it remains in the Telegram server).

4. **Bounded drain** — the process waits up to 15 seconds for all agent goroutines to finish. If a task is stuck (e.g., a long HTTP call that ignores context), the child process takes over and the parent is killed by the singleton lock.

5. **PID file cleanup** — before `os.Exit(0)`, the PID file lock is explicitly released so the child process starts with no stale lock file.

6. **Post-restart notification** — when the new instance starts, it reads the restart marker file and sends "🔄 Bot restarted" to each chat that was active during the restart.

### Spawn+Exit Mechanism

The actual process handoff uses the same spawn+exit mechanism:

```
SIGHUP → gracefulRestart() → writeRestartMarker() → spawnChild() → os.Exit(0)
                                                                   ↓
                                                child acquireLock() kills parent
                                                child gets fresh HTTP/2 connections
                                                child starts polling Telegram
```

The child process inherits environment variables and command-line arguments. `acquireLock` ensures the old process is dead before the new one starts polling. The restart marker at `~/.odek/restart.json` carries the list of chat IDs that had active agent runs.

This avoids binary overwrite races, stale HTTP/2 connections, and session context loops that plagued `syscall.Exec`. The restart marker (`~/.odek/restart.json`) enables the new instance to notify users that a restart occurred.

### Typing Indicator

A fire-and-forget goroutine sends `sendChatAction("typing")` every 4 seconds while the agent runs. Each API call is dispatched in its own goroutine so a slow or hanging HTTP call cannot block the ticker and permanently stop the indicator.

## Cron Integration

> **Prefer the native scheduler.** odek now has a built-in, in-process
> scheduler — `odek schedule add --cron "..." --deliver telegram "..."`. The
> bot hosts it automatically, so there's no host crontab to manage and a
> scheduled task sees the same resolved config the bot does. See
> [SCHEDULES.md](SCHEDULES.md). The OS-cron approach below still works and is
> handy when you'd rather drive scheduling from the host.

odek can also run fully offline agent tasks from system cron and deliver the result to Telegram with `--deliver` — no long-running odek process required.

### How it works

```
cron daemon  ──[every N minutes]──►  odek run "<task>" --deliver
                                           │
                                    ┌──────┴──────┐
                                    │  agent loop  │
                                    │  (no daemon) │
                                    └──────┬──────┘
                                           │ result text
                                           ▼
                                    Telegram API
                                           │
                                    ┌──────┴──────┐
                                    │  your chat   │
                                    └─────────────┘
```

Each cron tick spawns an independent `odek run` process. The agent executes the task, produces a result, and exits. The `--deliver` flag sends the result to your configured Telegram chat as a plain text message. No persistent bot process, no long-polling — just a single agent run per tick.

### Prerequisites

1. **Telegram bot token** and **default chat ID** must be configured in `~/.odek/config.json`:

```json
{
  "telegram": {
    "bot_token": "8610437446:AAElHFJ...",
    "default_chat_id": 8592463065
  }
}
```

2. **Verify delivery works** before setting up cron:

```bash
odek run "Say hello" --deliver
```

If no message arrives, check:
- The bot token is valid (`curl https://api.telegram.org/bot<TOKEN>/getMe`)
- The `default_chat_id` is correct (the numeric chat ID, not the username)
- The binary is at a stable path (system cron uses a minimal PATH, so use the full path)

### Setting up a cron job

```bash
# Edit your crontab
crontab -e

# Add a job — runs every 5 minutes
*/5 * * * * /usr/local/bin/odek run "Say hello briefly" --deliver >> /tmp/odek-cron.log 2>&1
```

**Important:**
- Use the **full absolute path** to the `odek` binary — cron runs with a minimal `PATH`
- Always redirect stderr to a log file (`2>&1`) for debugging
- Place `--deliver` **before** the task text, or anywhere after it (both work)
- The agent runs in **single-shot mode** by default — no session persistence, no learning loop
- Each cron tick is a fully independent agent invocation with no memory of previous runs

### Debugging

If messages don't arrive:

```bash
# 1. Verify the binary works standalone
/usr/local/bin/odek run "test" --deliver

# 2. Check cron's stderr log
cat /tmp/odek-cron.log

# 3. Confirm Telegram API is reachable from cron's environment
# Add to crontab to test:
# * * * * * curl -s "https://api.telegram.org/bot<TOKEN>/getMe" >> /tmp/telegram-test.log 2>&1

# 4. Check that the config file is readable by cron's user
ls -la ~/.odek/config.json
```

### Config reference

| Config field | Env var | Description |
|---|---|---|
| `telegram.bot_token` | `ODEK_TELEGRAM_BOT_TOKEN` | Telegram bot API token (required) |
| `telegram.default_chat_id` | — | Numeric chat ID to deliver results to (required) |

## Testing

The Telegram package is exhaustively tested under `-race`. Tests use:
- `httptest.NewServer` to mock Telegram API responses
- HTTP handler functions for each API endpoint (getFile, sendMessage, sendDocument, etc.)
- `t.TempDir()` + `t.Setenv("HOME", ...)` for filesystem isolation
- Hashed fileID suffix tests for voice/photo downloads (incl. prefix-collision regression)
- Plan CRUD tests with prefix matching, ambiguous matches, and error paths
- Session manager tests with TTL expiry and cache behavior

```bash
# Run all Telegram tests
go test ./internal/telegram/ -v -count=1

# With coverage
go test -cover ./internal/telegram/
```
