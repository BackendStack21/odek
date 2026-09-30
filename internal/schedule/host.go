package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/fsatomic"
)

// HostHealth is a liveness observation, not a promise that a task will succeed.
type HostHealth struct {
	Status        string    `json:"status"`
	LastHeartbeat time.Time `json:"last_heartbeat,omitzero"`
	Hosts         int       `json:"hosts"`
}

func (s *Scheduler) startHeartbeat(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	path := filepath.Join(s.store.dir, fmt.Sprintf("schedule-host-%d.json", os.Getpid()))
	write := func() {
		data, _ := json.Marshal(time.Now().UTC())
		if err := fsatomic.WriteFile(path, data, 0600); err != nil {
			s.log.Error("scheduler: heartbeat unavailable", "error", err)
		}
	}
	write()
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				write()
			}
		}
	}()
	return func() { cancel(); <-done; _ = os.Remove(path) }
}

func ReadHostHealth(dir string, now time.Time) HostHealth {
	result := HostHealth{Status: "unavailable"}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "schedule-host-") || !strings.HasSuffix(entry.Name(), ".json") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		var at time.Time
		if readJSON(filepath.Join(dir, entry.Name()), &at) != nil {
			continue
		}
		age := now.Sub(at)
		if age < 0 || age > 45*time.Second {
			continue
		}
		result.Hosts++
		if at.After(result.LastHeartbeat) {
			result.LastHeartbeat = at
		}
	}
	if result.Hosts > 0 {
		result.Status = "recent_heartbeat"
	}
	return result
}
