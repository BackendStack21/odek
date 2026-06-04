// Package schedule provides a native, in-process task scheduler for odek.
//
// It runs agent tasks on a cron schedule from inside a long-lived process
// (the Telegram bot, the `odek schedule daemon`, or `odek serve`) and
// delivers each result somewhere (Telegram, stdout, a log file). Running
// in-process is the whole point: the host process has already resolved its
// configuration (API key, model, bot token, default chat) into memory, so a
// scheduled task sees exactly what an interactive one does — no environment
// inheritance games, no external cron daemon, no container-only behaviour.
//
// The package is deliberately decoupled from the agent and Telegram packages.
// The firing engine (Scheduler) talks to the rest of odek through two small
// interfaces, Runner and Deliverer, so it can be unit-tested against fakes
// and reused by every host process unchanged.
//
// Layout on disk (mirrors the rest of ~/.odek):
//
//	~/.odek/schedules.json        job definitions (user-editable, 0600)
//	~/.odek/schedule-state.json   runtime state: last/next run, status (0600)
//
// Definitions and runtime state are kept in separate files on purpose: the
// definitions file is something a human edits or the CLI rewrites, while the
// state file churns on every fire. Keeping them apart means a hand-edit never
// races with a state write and the definitions file stays diff-clean.
package schedule

import "time"

// Delivery kinds. A job's result is routed to exactly one destination.
const (
	DeliverTelegram = "telegram" // send via the bot to ChatID (0 = default_chat_id)
	DeliverStdout   = "stdout"   // print to the daemon's stdout
	DeliverLog      = "log"      // append to the schedule run log
)

// Run-status values recorded in RunState.LastStatus.
const (
	StatusOK      = "ok"      // task ran and delivered
	StatusError   = "error"   // task or delivery failed (see LastError)
	StatusSkipped = "skipped" // a due fire was intentionally not run (e.g. missed while down, catchup off)
)

// Delivery describes where a job's result is sent.
type Delivery struct {
	Kind   string `json:"kind"`              // one of the Deliver* constants
	ChatID int64  `json:"chat_id,omitempty"` // telegram only; 0 = use the configured default_chat_id
}

// Job is a single scheduled agent task. Definitions live in schedules.json.
// All fields are exported so the CLI layer can construct and mutate jobs
// directly, matching the convention used by session.Session.
type Job struct {
	ID        string    `json:"id"`                 // stable short id, e.g. "jb-ab12cd"
	Name      string    `json:"name"`               // human-readable label
	Cron      string    `json:"cron"`               // 5-field expression or @macro (see cronexpr.go)
	Task      string    `json:"task"`               // the prompt handed to the agent
	Deliver   Delivery  `json:"deliver"`            // where the result goes
	Enabled   bool      `json:"enabled"`            // disabled jobs are parsed but never fired
	Catchup   bool      `json:"catchup,omitempty"`  // if a fire was missed while the process was down, run once on startup
	Timezone  string    `json:"timezone,omitempty"` // IANA name (e.g. "Europe/Berlin"); "" = scheduler default
	CreatedAt time.Time `json:"created_at"`         // when the job was added
}

// RunState is the mutable runtime state for one job, persisted in
// schedule-state.json keyed by Job.ID. It is updated after every fire.
type RunState struct {
	JobID      string    `json:"job_id"`
	LastRun    time.Time `json:"last_run,omitzero"`     // omitzero (not omitempty) — time.Time is a struct
	LastStatus string    `json:"last_status,omitempty"` // one of the Status* constants
	LastError  string    `json:"last_error,omitempty"`  // populated when LastStatus == StatusError
	LastResult string    `json:"last_result,omitempty"` // truncated preview of the delivered text
	NextRun    time.Time `json:"next_run,omitzero"`     // computed projected next fire
	Runs       int       `json:"runs,omitempty"`        // total successful + failed fires
}
