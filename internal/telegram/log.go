package telegram

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// LogLevel represents the severity of a log message.
type LogLevel int

const (
	LogDebug LogLevel = iota
	LogInfo
	LogWarn
	LogError
)

var levelNames = map[LogLevel]string{
	LogDebug: "DEBUG",
	LogInfo:  "INFO",
	LogWarn:  "WARN",
	LogError: "ERROR",
}

// Logger is the logging interface for the Telegram integration.
// Fields are alternating key-value pairs: log.Info("msg", "chat_id", 42, "error", err)
type Logger interface {
	Debug(msg string, fields ...any)
	Info(msg string, fields ...any)
	Warn(msg string, fields ...any)
	Error(msg string, fields ...any)
	// With returns a child logger that adds the given fields to every message.
	With(fields ...any) Logger
}

// ─── File Logger ────────────────────────────────────────────────────────────────

// fileLogger implements Logger by writing to a file (or stderr).
type fileLogger struct {
	mu     *sync.Mutex
	level  LogLevel
	file   *os.File
	fields []any // pre-pended fields from With()
}

// NewFileLogger creates a Logger that writes to the given file path.
// If path is empty, writes to stderr. Format:
//
//	2006-01-02T15:04:05.000Z [INFO] telegram: message key=value
//
// The file is opened in append mode, created if missing.
// Thread-safe via sync.Mutex.
func NewFileLogger(level LogLevel, path string) Logger {
	fl := &fileLogger{level: level, mu: &sync.Mutex{}}
	if path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			// Fall back to stderr if we can't open the file.
			fmt.Fprintf(os.Stderr, "telegram: failed to open log file %s: %v\n", path, err)
			fl.file = os.Stderr
		} else {
			fl.file = f
		}
	} else {
		fl.file = os.Stderr
	}
	return fl
}

// NewNopLogger returns a Logger that discards all messages (for tests).
func NewNopLogger() Logger {
	return nopLogger{}
}

// nopLogger silently discards all log messages.
type nopLogger struct{}

func (nopLogger) Debug(_ string, _ ...any) {}
func (nopLogger) Info(_ string, _ ...any)  {}
func (nopLogger) Warn(_ string, _ ...any)  {}
func (nopLogger) Error(_ string, _ ...any) {}
func (n nopLogger) With(_ ...any) Logger   { return n }

// Log implements the core logging for fileLogger.
func (fl *fileLogger) log(level LogLevel, msg string, fields ...any) {
	if level < fl.level {
		return
	}

	fl.mu.Lock()
	defer fl.mu.Unlock()

	// Format: 2006-01-02T15:04:05.000Z [LEVEL] telegram: message key=value
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	levelName := levelNames[level]

	line := fmt.Sprintf("%s [%s] telegram: %s", ts, levelName, msg)

	// Write pre-pended fields first (from With()), then call-site fields.
	allFields := append(fl.fields, fields...)
	if len(allFields) > 0 {
		line += " "
		for i := 0; i < len(allFields); i += 2 {
			if i > 0 {
				line += " "
			}
			key, ok := allFields[i].(string)
			if !ok {
				key = fmt.Sprintf("%v", allFields[i])
			}
			if i+1 < len(allFields) {
				line += fmt.Sprintf("%s=%v", key, allFields[i+1])
			} else {
				line += fmt.Sprintf("%s=(missing)", key)
			}
		}
	}

	line += "\n"
	fmt.Fprint(fl.file, line)
}

func (fl *fileLogger) Debug(msg string, fields ...any) { fl.log(LogDebug, msg, fields...) }
func (fl *fileLogger) Info(msg string, fields ...any)  { fl.log(LogInfo, msg, fields...) }
func (fl *fileLogger) Warn(msg string, fields ...any)  { fl.log(LogWarn, msg, fields...) }
func (fl *fileLogger) Error(msg string, fields ...any) { fl.log(LogError, msg, fields...) }

// With returns a child logger that adds the given fields to every message.
func (fl *fileLogger) With(fields ...any) Logger {
	return &fileLogger{
		mu:     fl.mu,
		level:  fl.level,
		file:   fl.file,
		fields: append(append([]any(nil), fl.fields...), fields...),
	}
}

// ParseLogLevel converts a string to a LogLevel. Defaults to LogInfo.
func ParseLogLevel(s string) LogLevel {
	switch strings.ToLower(s) {
	case "debug":
		return LogDebug
	case "info":
		return LogInfo
	case "warn":
		return LogWarn
	case "error":
		return LogError
	default:
		return LogInfo
	}
}
