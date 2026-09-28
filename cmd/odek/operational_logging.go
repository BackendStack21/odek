package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

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
	booting := true
	restore := diagnostics.Install(func(ev events.Event) {
		mu.Lock()
		defer mu.Unlock()
		if logger != nil {
			logger.Emit(ev)
		} else if booting {
			if len(pending) < 128 {
				pending = append(pending, ev)
			} else {
				lost++
			}
		}
	})
	enabled, maxMB := config.LoadLoggingSettings()
	mu.Lock()
	booting = false
	if enabled {
		var err error
		logger, err = runtimelog.Open(filepath.Join(expandHome("~/.odek"), "runtime.log"), surface, maxMB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "odek: operational logging unavailable: %v\n", err)
		} else {
			for _, ev := range pending {
				logger.Emit(ev)
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
				l.Close()
			}
		})
	}
}

func logCommandFailure(err error) {
	diagnostics.Report("cli", "command", "", err)
}
