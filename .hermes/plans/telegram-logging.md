# P2: Proper logging abstraction + file adapter

## Design

### Logger interface (internal/telegram/log.go)
```go
type LogLevel int
const ( LogDebug, LogInfo, LogWarn, LogError )

type Logger interface {
    Debug(msg string, fields ...any)
    Info(msg string, fields ...any)
    Warn(msg string, fields ...any)
    Error(msg string, fields ...any)
    With(fields ...any) Logger  // returns child with extra fields
}
```

Fields are alternating key-value pairs: `log.Info("started", "chat_id", chatID)`.

### FileLogger (internal/telegram/log.go)
- Writes to a file or stderr
- Format: `2006-01-02T15:04:05.000Z [LEVEL] telegram: <msg> [key=value ...]`
- File is opened at creation, append mode
- If no log_file in config, defaults to stderr using the same format
- Thread-safe (mutex on writes)

### Config additions (existing TelegramConfig)
- `log_level` string → "debug", "info", "warn", "error" (default: "info")
- `log_file` string → path (empty = stderr)

### Wiring changes
Each component that currently logs gets a Logger field:

- **Bot**: `Logger` field, logs API errors
- **Handler**: `Logger` field, replaces all `OnError` + `fmt.Fprintf` + `reportError`
- **Poller**: `Logger` field, replaces `fmt.Fprintf`
- **Approver**: `Logger` field, replaces `a.OnError`

**telegram.go entry point**:
1. Resolve log level + log file from config
2. Create the logger (file or stderr)
3. Pass it to Bot, Handler, Poller via constructors/setters
4. Handler's OnError can still exist as a callback (for chat notifications), but logging goes through Logger

### Files to modify
- `internal/telegram/log.go` — NEW: interface + file adapter
- `internal/telegram/config.go` — Add log_level, log_file fields
- `internal/telegram/bot.go` — Add Logger field
- `internal/telegram/handler.go` — Add Logger field, replace all logging
- `internal/telegram/poller.go` — Add Logger field
- `internal/telegram/approver.go` — Replace a.OnError with Logger
- `cmd/odek/telegram.go` — Wire logger creation + pass to components
