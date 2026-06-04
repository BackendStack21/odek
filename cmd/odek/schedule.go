package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/render"
	"github.com/BackendStack21/odek/internal/schedule"
	"github.com/BackendStack21/odek/internal/telegram"
)

// scheduleCmd is the entry point for "odek schedule" — management of native,
// in-process scheduled agent tasks (see internal/schedule).
func scheduleCmd(args []string) error {
	if len(args) == 0 {
		printScheduleUsage()
		return nil
	}

	// daemon and run resolve their own config; the rest only touch the store.
	switch args[0] {
	case "daemon":
		return scheduleDaemon(args[1:])
	case "run":
		return scheduleRunNow(args[1:])
	}

	st, err := schedule.NewStore()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list", "ls":
		return scheduleList(st)
	case "add":
		return scheduleAdd(st, args[1:])
	case "rm", "remove", "delete":
		return scheduleRemove(st, args[1:])
	case "enable":
		return scheduleSetEnabled(st, args[1:], true)
	case "disable":
		return scheduleSetEnabled(st, args[1:], false)
	case "next":
		return scheduleNext(st, args[1:])
	default:
		return fmt.Errorf("unknown schedule command %q (use list, add, rm, enable, disable, run, next, daemon)", args[0])
	}
}

func printScheduleUsage() {
	fmt.Println(`Usage: odek schedule <command>

Commands:
  list                      List scheduled jobs (id, next fire, last status)
  add --cron "<expr>" <task>  Add a job (see flags below)
  rm <id>                   Remove a job
  enable <id>               Enable a job
  disable <id>              Disable a job (kept, but never fires)
  run <id>                  Run a job once now and deliver (test it)
  next <id|cron-expr>       Show the next few fire times
  daemon                    Run the scheduler in the foreground

Add flags:
  --name <label>            Human label (defaults to the first words of the task)
  --cron "<expr>"           5-field cron or @macro (@hourly @daily @weekly @monthly @yearly)
  --deliver <dest>          stdout (default) | log | telegram | telegram:<chatID>
  --tz <IANA>               Timezone, e.g. Europe/Berlin (default UTC)
  --catchup                 Run once on startup if a fire was missed while down
  --disabled                Add without enabling

Examples:
  odek schedule add --cron "0 9 * * 1-5" --deliver telegram "Summarize today's calendar"
  odek schedule next "*/15 * * * *"
  odek schedule daemon`)
}

// ── list ────────────────────────────────────────────────────────────────

func scheduleList(st *schedule.Store) error {
	jobs, err := st.List()
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Println(`No scheduled jobs. Add one with:
  odek schedule add --cron "0 9 * * 1-5" --deliver telegram "your task"`)
		return nil
	}
	state, _ := st.LoadState()
	now := time.Now()

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tON\tCRON\tNEXT (local)\tLAST\tNAME")
	for _, j := range jobs {
		next := "—"
		if s, err := jobSchedule(j); err != nil {
			next = "invalid"
		} else if nt := s.Next(now); !nt.IsZero() {
			next = nt.Local().Format("Mon 02 Jan 15:04")
		}
		last := "—"
		if rs, ok := state[j.ID]; ok && rs.LastStatus != "" {
			last = rs.LastStatus
		}
		on := "yes"
		if !j.Enabled {
			on = "no"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, on, j.Cron, next, last, j.Name)
	}
	return w.Flush()
}

// ── add ─────────────────────────────────────────────────────────────────

func scheduleAdd(st *schedule.Store, args []string) error {
	fs := flag.NewFlagSet("schedule add", flag.ContinueOnError)
	name := fs.String("name", "", "human label")
	cron := fs.String("cron", "", "cron expression or @macro")
	deliver := fs.String("deliver", "stdout", "stdout | log | telegram | telegram:<chatID>")
	tz := fs.String("tz", "", "IANA timezone (default UTC)")
	catchup := fs.Bool("catchup", false, "run once on startup if a fire was missed while down")
	disabled := fs.Bool("disabled", false, "add without enabling")
	if err := fs.Parse(args); err != nil {
		return err
	}
	task := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if *cron == "" || task == "" {
		return fmt.Errorf(`usage: odek schedule add --cron "<expr>" [flags] <task>`)
	}
	del, err := parseDeliver(*deliver)
	if err != nil {
		return err
	}
	job := schedule.Job{
		Name:     *name,
		Cron:     *cron,
		Task:     task,
		Deliver:  del,
		Timezone: *tz,
		Catchup:  *catchup,
		Enabled:  !*disabled,
	}
	if job.Name == "" {
		job.Name = firstWords(task, 6)
	}
	saved, err := st.Add(job)
	if err != nil {
		return err
	}
	next := "—"
	if s, err := jobSchedule(saved); err == nil {
		if nt := s.Next(time.Now()); !nt.IsZero() {
			next = nt.Local().Format(time.RFC1123)
		}
	}
	state := "enabled"
	if !saved.Enabled {
		state = "disabled"
	}
	fmt.Printf("Added %s (%s, %s)\n  cron:    %s\n  deliver: %s\n  next:    %s\n",
		saved.ID, saved.Name, state, saved.Cron, deliverString(saved.Deliver), next)
	return nil
}

// ── rm / enable / disable ───────────────────────────────────────────────

func scheduleRemove(st *schedule.Store, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: odek schedule rm <id>")
	}
	if err := st.Remove(args[0]); err != nil {
		return err
	}
	fmt.Printf("Removed %s\n", args[0])
	return nil
}

func scheduleSetEnabled(st *schedule.Store, args []string, enabled bool) error {
	verb := "enable"
	if !enabled {
		verb = "disable"
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: odek schedule %s <id>", verb)
	}
	if err := st.SetEnabled(args[0], enabled); err != nil {
		return err
	}
	fmt.Printf("%sd %s\n", verb, args[0])
	return nil
}

// ── next ────────────────────────────────────────────────────────────────

func scheduleNext(st *schedule.Store, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf(`usage: odek schedule next <id|"cron-expr">`)
	}
	var s *schedule.Schedule
	if job, ok, _ := st.Get(args[0]); ok && len(args) == 1 {
		sc, err := jobSchedule(job)
		if err != nil {
			return err
		}
		s = sc
		fmt.Printf("Job %s (%s): %s\n", job.ID, job.Name, job.Cron)
	} else {
		expr := strings.Join(args, " ")
		sc, err := schedule.Parse(expr)
		if err != nil {
			return err
		}
		s = sc
		fmt.Printf("Expression: %s (UTC)\n", expr)
	}
	t := time.Now()
	for range 5 {
		t = s.Next(t)
		if t.IsZero() {
			fmt.Println("  (no further fires within the search horizon)")
			break
		}
		fmt.Printf("  %s\n", t.Local().Format(time.RFC1123))
	}
	return nil
}

// ── run (once, now) ─────────────────────────────────────────────────────

func scheduleRunNow(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: odek schedule run <id>")
	}
	st, err := schedule.NewStore()
	if err != nil {
		return err
	}
	job, ok, err := st.Get(args[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no job with ID %q", args[0])
	}

	resolved := config.LoadConfig(config.CLIFlags{})
	system := buildSystemPrompt(resolved)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Fprintf(os.Stderr, "Running %s (%s)…\n", job.ID, job.Name)
	result, _, err := runTaskHeadless(ctx, resolved, system, job.Task)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	if err := (cliDeliverer{resolved: resolved}).Deliver(job, result); err != nil {
		return fmt.Errorf("deliver: %w", err)
	}
	return nil
}

// ── daemon ──────────────────────────────────────────────────────────────

func scheduleDaemon(_ []string) error {
	unlock, err := acquireScheduleLock()
	if err != nil {
		return err
	}
	defer unlock()

	resolved := config.LoadConfig(config.CLIFlags{})
	system := buildSystemPrompt(resolved)
	st, err := schedule.NewStore()
	if err != nil {
		return err
	}

	sched := schedule.New(st,
		agentRunner{resolved: resolved, system: system},
		cliDeliverer{resolved: resolved},
		schedule.Options{Logger: stderrLogger{}},
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	jobs, _ := st.List()
	enabled := 0
	for _, j := range jobs {
		if j.Enabled {
			enabled++
		}
	}
	fmt.Fprintf(os.Stderr, "odek schedule daemon ⏰  %d job(s) loaded (%d enabled). Ctrl-C to stop.\n", len(jobs), enabled)

	if err := sched.Run(ctx); err != nil && err != context.Canceled {
		return err
	}
	fmt.Fprintln(os.Stderr, "odek schedule daemon: stopped")
	return nil
}

// ── Runner / Deliverer implementations ──────────────────────────────────

// agentRunner runs each job's task through a fresh, headless odek agent using
// the config resolved at daemon startup.
type agentRunner struct {
	resolved config.ResolvedConfig
	system   string
}

func (r agentRunner) Run(ctx context.Context, job schedule.Job) (string, int64, error) {
	return runTaskHeadless(ctx, r.resolved, r.system, job.Task)
}

// cliDeliverer routes results to stdout, a log file, or Telegram, honouring a
// per-job chat ID (falling back to the configured default_chat_id).
type cliDeliverer struct {
	resolved config.ResolvedConfig
}

func (d cliDeliverer) Deliver(job schedule.Job, result string) error {
	switch job.Deliver.Kind {
	case schedule.DeliverStdout:
		fmt.Printf("\n── %s · %s ──\n%s\n", job.Name, time.Now().Format(time.RFC1123), result)
		return nil
	case schedule.DeliverLog:
		return appendScheduleLog(job, result)
	case schedule.DeliverTelegram:
		return d.deliverTelegram(job, result)
	default:
		return fmt.Errorf("unknown delivery kind %q", job.Deliver.Kind)
	}
}

func (d cliDeliverer) deliverTelegram(job schedule.Job, result string) error {
	if d.resolved.Telegram.Token == "" {
		return fmt.Errorf("telegram bot_token not configured")
	}
	chatID := job.Deliver.ChatID
	if chatID == 0 {
		chatID = d.resolved.Telegram.DefaultChatID
	}
	if chatID == 0 {
		return fmt.Errorf("no chat id (set the job's telegram:<chatID> or telegram.default_chat_id)")
	}
	bot := telegram.NewBot(d.resolved.Telegram.Token)
	_, err := bot.SendMessage(chatID, result, nil)
	return err
}

// ── embedded scheduler (inside `odek telegram`) ─────────────────────────

// scheduleUnlockRef holds the embedded scheduler's pid-lock releaser so the
// Telegram graceful-restart path can release it before os.Exit(0), mirroring
// instanceLockRef. Without this the restarted child would see a live lock and
// skip starting its scheduler.
var scheduleUnlockRef func()

// telegramRunner runs a job's task headlessly and accounts its token usage
// against the bot's daily budget.
type telegramRunner struct {
	resolved config.ResolvedConfig
	system   string
	bot      *telegram.Bot
}

func (r telegramRunner) Run(ctx context.Context, job schedule.Job) (string, int64, error) {
	budgeted := r.resolved.Telegram.DailyTokenBudget > 0
	if budgeted {
		// Pre-flight: refuse if the budget is already exhausted so a runaway
		// schedule can't keep spending. Mirrors the chat-message pre-check.
		if err := r.bot.CheckDailyBudget(1); err != nil {
			return "", 0, fmt.Errorf("daily token budget exhausted: %w", err)
		}
	}
	result, tokens, err := runTaskHeadless(ctx, r.resolved, r.system, job.Task)
	if err == nil && budgeted && tokens > 0 {
		_ = r.bot.CheckDailyBudget(tokens) // bill the run (best-effort)
	}
	return result, tokens, err
}

// telegramDeliverer delivers via the live bot for telegram jobs (sharing its
// client and rate limiting) and falls back to the CLI deliverer for stdout/log.
type telegramDeliverer struct {
	bot      *telegram.Bot
	fallback cliDeliverer
}

func (d telegramDeliverer) Deliver(job schedule.Job, result string) error {
	if job.Deliver.Kind != schedule.DeliverTelegram {
		return d.fallback.Deliver(job, result)
	}
	chatID := job.Deliver.ChatID
	if chatID == 0 {
		chatID = d.fallback.resolved.Telegram.DefaultChatID
	}
	if chatID == 0 {
		return fmt.Errorf("no chat id (set the job's telegram:<chatID> or telegram.default_chat_id)")
	}
	_, err := d.bot.SendMessage(chatID, result, nil)
	return err
}

// startSchedulerForBot starts the embedded scheduler unless an external
// `odek schedule daemon` already holds the lock (in which case the bot defers
// to it, to avoid double-firing). It returns a stop func that releases the
// lock; the scheduler goroutine itself stops when ctx is cancelled.
func startSchedulerForBot(ctx context.Context, bot *telegram.Bot, resolved config.ResolvedConfig, system string, log telegram.Logger) func() {
	unlock, err := acquireScheduleLock()
	if err != nil {
		log.Info("schedule: embedded scheduler not started", "reason", err.Error())
		return func() {}
	}
	st, err := schedule.NewStore()
	if err != nil {
		log.Error("schedule: store init failed", "error", err)
		unlock()
		return func() {}
	}
	sched := schedule.New(st,
		telegramRunner{resolved: resolved, system: system, bot: bot},
		telegramDeliverer{bot: bot, fallback: cliDeliverer{resolved: resolved}},
		schedule.Options{Logger: log},
	)
	scheduleUnlockRef = unlock
	go func() { _ = sched.Run(ctx) }()

	enabled := 0
	if jobs, err := st.List(); err == nil {
		for _, j := range jobs {
			if j.Enabled {
				enabled++
			}
		}
	}
	log.Info("schedule: embedded scheduler started", "enabled_jobs", enabled)

	return func() {
		scheduleUnlockRef = nil
		unlock()
	}
}

// ── headless agent execution ────────────────────────────────────────────

// runTaskHeadless builds a fresh agent with no terminal renderer and no
// interactive approver and runs one task to completion, returning the final
// text. It mirrors run()'s construction but for unattended use: the danger
// policy from resolved.Dangerous governs what the task may do (no human is
// present to approve), exactly as for `odek run` invoked non-interactively.
// Token usage is not yet surfaced (returns 0); the Telegram integration will
// supply its own Runner that accounts for the daily budget.
func runTaskHeadless(ctx context.Context, resolved config.ResolvedConfig, system, task string) (string, int64, error) {
	tools := builtinTools(resolved.Dangerous, nil, nil, resolved.MaxConcurrency, resolved.APIKey, resolved.Transcription, nil)

	if len(resolved.MCPServers) > 0 {
		cleanup, err := loadMCPTools(resolved.MCPServers, &tools)
		if err != nil {
			return "", 0, fmt.Errorf("mcp: %w", err)
		}
		defer cleanup()
	}

	// Capture cumulative token usage from the final iteration so the Runner
	// can report it (the engine logs it; the bot bills it against the budget).
	// RunWithMessages drives the loop synchronously on this goroutine, so the
	// callback needs no synchronisation.
	var lastInfo loop.IterationInfo
	agent, err := odek.New(odek.Config{
		Model:             resolved.Model,
		BaseURL:           resolved.BaseURL,
		APIKey:            resolved.APIKey,
		MaxIterations:     resolved.MaxIter,
		MaxToolParallel:   resolved.MaxToolParallel,
		SystemMessage:     system,
		RuntimeContext:    odek.BuildRuntimeContext("schedule"),
		NoProjectFile:     resolved.NoAgents,
		Thinking:          resolved.Thinking,
		Temperature:       0,
		Tools:             tools,
		Renderer:          render.New(io.Discard, false), // silent: unattended
		InteractionMode:   "off",
		PromptCaching:     resolved.PromptCaching,
		IterationCallback: func(info loop.IterationInfo) { lastInfo = info },
	})
	if err != nil {
		return "", 0, err
	}
	defer agent.Close()

	var messages []llm.Message
	if system != "" {
		messages = append(messages, llm.Message{Role: "system", Content: system})
	}
	messages = append(messages, llm.Message{Role: "user", Content: task})

	result, _, err := agent.RunWithMessages(ctx, messages)
	tokens := int64(lastInfo.InputTokens + lastInfo.OutputTokens)
	return result, tokens, err
}

// ── helpers ─────────────────────────────────────────────────────────────

// jobSchedule compiles a job's cron in its timezone (or UTC) for display.
func jobSchedule(j schedule.Job) (*schedule.Schedule, error) {
	loc := time.UTC
	if j.Timezone != "" {
		l, err := time.LoadLocation(j.Timezone)
		if err != nil {
			return nil, err
		}
		loc = l
	}
	return schedule.ParseInLocation(j.Cron, loc)
}

// parseDeliver parses a --deliver value: "stdout", "log", "telegram", or
// "telegram:<chatID>".
func parseDeliver(s string) (schedule.Delivery, error) {
	kind, rest, _ := strings.Cut(s, ":")
	switch kind {
	case "", schedule.DeliverStdout:
		return schedule.Delivery{Kind: schedule.DeliverStdout}, nil
	case schedule.DeliverLog:
		return schedule.Delivery{Kind: schedule.DeliverLog}, nil
	case schedule.DeliverTelegram:
		d := schedule.Delivery{Kind: schedule.DeliverTelegram}
		if rest != "" {
			id, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				return d, fmt.Errorf("invalid telegram chat id %q", rest)
			}
			d.ChatID = id
		}
		return d, nil
	default:
		return schedule.Delivery{}, fmt.Errorf("unknown delivery %q (use stdout, log, telegram, telegram:<chatID>)", s)
	}
}

func deliverString(d schedule.Delivery) string {
	if d.Kind == schedule.DeliverTelegram && d.ChatID != 0 {
		return fmt.Sprintf("telegram:%d", d.ChatID)
	}
	return d.Kind
}

// firstWords returns up to n whitespace-separated words of s, for a default
// job label.
func firstWords(s string, n int) string {
	fields := strings.Fields(s)
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}

// appendScheduleLog appends a delivered result to ~/.odek/schedule.log.
func appendScheduleLog(job schedule.Job, result string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "schedule.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "[%s] %s (%s)\n%s\n\n", time.Now().Format(time.RFC3339), job.Name, job.ID, result)
	return err
}

// stderrLogger is a minimal schedule.Logger that writes key/value lines to
// stderr, matching the daemon's foreground logging style.
type stderrLogger struct{}

func (stderrLogger) Info(msg string, kv ...any)  { logKV("INFO", msg, kv) }
func (stderrLogger) Error(msg string, kv ...any) { logKV("ERROR", msg, kv) }

func logKV(level, msg string, kv []any) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s schedule: %s", level, msg)
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, " %v=%v", kv[i], kv[i+1])
	}
	fmt.Fprintln(os.Stderr, b.String())
}

// acquireScheduleLock prevents two schedule daemons from firing the same jobs.
// Unlike the Telegram lock it refuses to start when a live daemon is found
// rather than killing it — a running scheduler should not be silently usurped.
func acquireScheduleLock() (func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	pidFile := filepath.Join(home, ".odek", "schedule.pid")
	if err := os.MkdirAll(filepath.Dir(pidFile), 0755); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(pidFile); err == nil {
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid > 1 {
			if err := syscall.Kill(pid, 0); err == nil {
				return nil, fmt.Errorf("another schedule daemon is already running (PID %d)", pid)
			}
		}
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		return nil, err
	}
	return func() { os.Remove(pidFile) }, nil
}
