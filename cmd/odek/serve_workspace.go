package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/maintenance"
	"github.com/BackendStack21/odek/internal/schedule"
)

// workspaceCapabilities is additive: clients must tolerate missing features.
func workspaceCapabilities() map[string]any {
	return map[string]any{"version": 1, "features": map[string]bool{"media_uploads": true, "artifact_previews": true, "tool_identity": true, "tool_outcomes": true, "skill_review": true, "schedules": true, "maintenance": true, "tool_schemas": true}, "result_renderers": []string{"code", "diff", "terminal", "search", "json", "sources", "text"}}
}
func handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	writeAPIJSON(w, 200, workspaceCapabilities())
}

// handleSchedules shares the CLI store and file locks. serve manages definitions;
// a scheduler daemon or Telegram host executes them and publishes runtime state.
func handleSchedules(home string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", 405)
			return
		}
		store, err := schedule.NewStoreAt(home)
		if err != nil {
			http.Error(w, "schedule store unavailable", 500)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/schedules")
		id = strings.TrimPrefix(id, "/")
		if r.Method == http.MethodGet {
			jobs, err := store.List()
			if err != nil {
				http.Error(w, "cannot read schedules", 500)
				return
			}
			states, err := store.LoadState()
			if err != nil {
				http.Error(w, "cannot read schedule state", 500)
				return
			}
			if jobs == nil {
				jobs = []schedule.Job{}
			}
			next := map[string]time.Time{}
			for _, j := range jobs {
				loc := time.UTC
				if j.Timezone != "" {
					if l, e := time.LoadLocation(j.Timezone); e == nil {
						loc = l
					}
				}
				if expr, e := schedule.ParseInLocation(j.Cron, loc); e == nil {
					next[j.ID] = expr.Next(time.Now())
				}
			}
			writeAPIJSON(w, 200, map[string]any{"jobs": jobs, "states": states, "next": next, "execution_host": "schedule daemon or Telegram"})
			return
		}
		if r.Method == http.MethodDelete {
			if id == "" {
				http.Error(w, "schedule id required", 400)
				return
			}
			if err := store.Remove(id); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			w.WriteHeader(204)
			return
		}
		var job schedule.Job
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&job); err != nil {
			http.Error(w, "invalid schedule", 400)
			return
		}
		if len(job.Task) > 32000 || len(job.Name) > 200 {
			http.Error(w, "schedule too large", 400)
			return
		}
		if id != "" {
			old, found, e := store.Get(id)
			if e != nil || !found {
				http.Error(w, "schedule not found", 404)
				return
			}
			job.ID = id
			job.CreatedAt = old.CreatedAt
			err = store.Put(job)
		} else {
			job.ID = ""
			job.CreatedAt = time.Time{}
			job, err = store.Add(job)
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeAPIJSON(w, 200, job)
	}
}

// Maintenance uses only the operator-resolved policy; request bodies cannot
// broaden retention or choose a filesystem root.
func handleMaintenance(home string, resolved config.ResolvedConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeAPIJSON(w, 200, map[string]any{"policy": resolved.Maintenance, "description": "Cleanup uses the server's retention policy. Zero retention keeps that category indefinitely."})
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		var body struct {
			Confirm string `json:"confirm"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil || body.Confirm != "cleanup" {
			http.Error(w, "type cleanup to confirm retention cleanup", 400)
			return
		}
		report, err := maintenance.Sweep(r.Context(), filepath.Clean(home), resolved.Maintenance)
		if err != nil {
			writeAPIJSON(w, 500, map[string]any{"error": "cleanup partially failed", "report": report})
			return
		}
		writeAPIJSON(w, 200, report)
	}
}
