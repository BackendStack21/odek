package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/runtimelog"
)

// startOperationalLogging covers failures before an Agent exists and between
// turns. Agent loggers use the same process identity and cooperating file lock.
func startOperationalLogging(surface string) func() {
	var mu sync.Mutex
	var pending []events.Event
	var lost int
	var logger *runtimelog.Logger
	var release func()
	booting := true
	restore := diagnostics.Install(func(ev events.Event) {
		mu.Lock()
		defer mu.Unlock()
		if logger != nil {
			emitOperationalRecord(logger, ev)
		} else if booting {
			if len(pending) < 128 {
				pending = append(pending, ev)
			} else {
				lost++
			}
		}
	})
	settings := config.LoadLoggingConfig()
	mu.Lock()
	booting = false
	if settings.Enabled {
		var err error
		logger, release, err = runtimelog.Acquire(loggingOptions(settings, surface))
		if err != nil {
			fmt.Fprintf(os.Stderr, "odek: operational logging unavailable: %v\n", err)
		} else {
			if _, err := runtimelog.PruneAtStartup(context.Background(), loggingOptions(settings, surface)); err != nil {
				logger.Emit(diagnostics.Failure("logging", "startup_retention", "", err))
			}
			for _, ev := range pending {
				emitOperationalRecord(logger, ev)
			}
			if lost > 0 {
				logger.Emit(events.Event{Type: "logging_dropped", Data: map[string]any{"dropped": lost}})
			}
		}
	}
	pending = nil
	mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			restore()
			mu.Lock()
			l := logger
			logger = nil
			mu.Unlock()
			if l != nil {
				release()
			}
		})
	}
}

func logCommandFailure(err error) {
	diagnostics.Report("cli", "command", "", err)
}

func loggingOptions(c config.LoggingConfig, surface string) runtimelog.Options {
	return runtimelog.Options{Path: expandHome(c.File), Surface: surface, Level: c.Level, MaxFileMB: c.MaxFileMB, MaxFiles: c.MaxFiles, MaxAgeHours: c.MaxAgeHours}
}

// finishAgentInvocation preserves panic behavior while avoiding a false success.
func finishAgentInvocation(agent *odek.Agent, outcome *error) {
	if value := recover(); value != nil {
		agent.FinishRun(errors.New("invocation panicked"))
		panic(value)
	}
	agent.FinishRun(*outcome)
}

func emitOperationalRecord(logger *runtimelog.Logger, ev events.Event) {
	component, _ := ev.Data["component"].(string)
	switch component {
	case "serve", "telegram", "schedule":
		logger.EmitForSurface(ev, component)
	default:
		logger.Emit(ev)
	}
}
