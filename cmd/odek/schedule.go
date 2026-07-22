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
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/guard"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/redact"
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
	job, ok, err := st.Get(args[0])
	if err != nil {
		return err // surface a corrupt/unreadable store, not a misleading cron error
	}
	var s *schedule.Schedule
	if ok && len(args) == 1 {
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
	if err := approveProjectSandbox(resolved, os.Stdin, os.Stdout); err != nil {
		return err
	}
	system := buildSystemPrompt(resolved)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	mcpTools, mcpCleanup, err := buildScheduledMCPTools(resolved)
	if err != nil {
		return err
	}
	defer mcpCleanup()

	fmt.Fprintf(os.Stderr, "Running %s (%s)…\n", job.ID, job.Name)
	result, _, err := runTaskHeadless(ctx, resolved, system, job.Task, mcpTools)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	if err := (cliDeliverer{resolved: resolved}).Deliver(ctx, job, result); err != nil {
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
	if err := approveProjectSandbox(resolved, os.Stdin, os.Stdout); err != nil {
		return err
	}
	system := buildSystemPrompt(resolved)
	st, err := schedule.NewStore()
	if err != nil {
		return err
	}

	// Connect MCP servers once (if any) and share them across every fire.
	mcpTools, mcpCleanup, err := buildScheduledMCPTools(resolved)
	if err != nil {
		return err
	}
	defer mcpCleanup()

	logger := telegram.NewFileLogger(telegram.LogInfo, "") // "" → stderr
	sched := schedule.New(st,
		agentRunner{resolved: resolved, system: system, mcpTools: mcpTools},
		cliDeliverer{resolved: resolved},
		schedulerOptions(resolved.Schedules, logger),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Start the storage-maintenance janitor (expired sessions, audit records,
	// plans, skill skips, log rotation), tied to the daemon's lifetime.
	startStorageMaintenance(ctx, resolved)

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
// the config resolved at daemon startup. mcpTools are connected once and reused.
type agentRunner struct {
	resolved config.ResolvedConfig
	system   string
	mcpTools []odek.Tool
}

func (r agentRunner) Run(ctx context.Context, job schedule.Job) (string, int64, error) {
	return runTaskHeadless(ctx, r.resolved, r.system, job.Task, r.mcpTools)
}

// cliDeliverer routes results to stdout, a log file, or Telegram, honouring a
// per-job chat ID (falling back to the configured default_chat_id).
type cliDeliverer struct {
	resolved config.ResolvedConfig
}

func (d cliDeliverer) Deliver(ctx context.Context, job schedule.Job, result string) error {
	switch job.Deliver.Kind {
	case schedule.DeliverStdout:
		fmt.Printf("\n── %s · %s ──\n%s\n", job.Name, time.Now().Format(time.RFC1123), result)
		return nil
	case schedule.DeliverLog:
		return appendScheduleLog(job, result)
	case schedule.DeliverTelegram:
		return d.deliverTelegram(ctx, job, result)
	default:
		return fmt.Errorf("unknown delivery kind %q", job.Deliver.Kind)
	}
}

func (d cliDeliverer) deliverTelegram(ctx context.Context, job schedule.Job, result string) error {
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
	return sendTelegramResult(ctx, bot, chatID, result)
}

// sendTelegramResult delivers a scheduled task's result to Telegram, mirroring
// the live bot's Handler.SendResponse pipeline: the result (odek markdown) is
// converted to Telegram MarkdownV2 and chunked via FormatResponse, then each
// chunk is sent with MarkdownV2 and retried as plain text if Telegram rejects
// the formatting. Without this the raw "**bold**" markdown is delivered as
// literal asterisks. Uses the context-aware send so a stuck delivery doesn't
// block the scheduler's graceful shutdown.
func sendTelegramResult(ctx context.Context, bot *telegram.Bot, chatID int64, result string) error {
	chunks, err := telegram.FormatResponse(result)
	if err != nil {
		return fmt.Errorf("format response: %w", err)
	}
	for _, chunk := range chunks {
		if chunk == "" {
			continue
		}
		_, err := bot.SendMessageContext(ctx, chatID, chunk, &telegram.SendOpts{ParseMode: telegram.ParseModeMarkdownV2})
		if err == nil {
			continue
		}
		// Retry as plain text — covers MarkdownV2 parse errors and other
		// transient failures — but give up if the context was cancelled.
		if ctx.Err() != nil {
			return err
		}
		if _, perr := bot.SendMessageContext(ctx, chatID, chunk, nil); perr != nil {
			return perr
		}
	}
	return nil
}

// ── embedded scheduler (inside `odek telegram`) ─────────────────────────

// scheduleUnlockRef holds the embedded scheduler's pid-lock releaser so the
// Telegram graceful-restart path can release it before os.Exit(0), mirroring
// instanceLockRef. Without this the restarted child would see a live lock and
// skip starting its scheduler.
var scheduleUnlockRef func()

// mcpCleanupRef holds the embedded scheduler's MCP-connection cleanup so the
// graceful-restart path can run it before os.Exit(0). os.Exit skips deferred
// functions, so without this the MCP child processes (e.g. Playwright/Chromium)
// would leak across every /restart.
var mcpCleanupRef func()

// scheduleReloadRef points at the running embedded scheduler's Reload method so
// the Telegram `/schedule` commands can force an immediate reconcile after an
// edit instead of waiting for the mtime poll. nil when no embedded scheduler is
// running (disabled, or an external daemon holds the lock) — callers treat a nil
// ref as a no-op and rely on the file write being picked up by whoever schedules.
var scheduleReloadRef func()

// telegramRunner runs a job's task headlessly and accounts its token usage
// against the bot's daily budget.
type telegramRunner struct {
	resolved config.ResolvedConfig
	system   string
	bot      *telegram.Bot
	mcpTools []odek.Tool
}

func (r telegramRunner) Run(ctx context.Context, job schedule.Job) (string, int64, error) {
	budgeted := r.resolved.Telegram.DailyTokenBudget > 0
	if budgeted {
		// Pre-flight: refuse if the budget is already exhausted, WITHOUT mutating
		// it (DailyTokenUsage is read-only; CheckDailyBudget would charge the
		// probe amount on every fire).
		if used, limit := r.bot.DailyTokenUsage(); limit > 0 && used >= limit {
			return "", 0, fmt.Errorf("daily token budget exhausted (%d/%d tokens)", used, limit)
		}
	}
	result, tokens, err := runTaskHeadless(ctx, r.resolved, r.system, job.Task, r.mcpTools)
	if err == nil && budgeted && tokens > 0 {
		_ = r.bot.CheckDailyBudget(tokens) // bill the run (best-effort)
	}
	return result, tokens, err
}

// telegramDeliverer delivers via the live bot for telegram jobs (sharing its
// client and rate limiting) and falls back to the CLI deliverer for stdout/log.
//
// sessions, when set, lets a delivered result be recorded into the target
// chat's conversation (Option B) so a follow-up message in that chat sees what
// the schedule posted. The scheduled run itself stays isolated — only its
// output is written back.
type telegramDeliverer struct {
	bot      *telegram.Bot
	fallback cliDeliverer
	sessions *telegram.SessionManager
	log      schedule.Logger
}

func (d telegramDeliverer) Deliver(ctx context.Context, job schedule.Job, result string) error {
	if job.Deliver.Kind != schedule.DeliverTelegram {
		return d.fallback.Deliver(ctx, job, result)
	}
	chatID := job.Deliver.ChatID
	if chatID == 0 {
		chatID = d.fallback.resolved.Telegram.DefaultChatID
	}
	if chatID == 0 {
		return fmt.Errorf("no chat id (set the job's telegram:<chatID> or telegram.default_chat_id)")
	}
	if err := sendTelegramResult(ctx, d.bot, chatID, result); err != nil {
		return err
	}
	// Best-effort: record the delivered turn into the chat's conversation so
	// the agent can follow up on it. A failure here must NOT fail the run — the
	// message was already sent — so it is logged, not returned.
	if err := d.recordScheduledTurn(chatID, job, result); err != nil && d.log != nil {
		d.log.Error("schedule: record delivered turn into session failed", "id", job.ID, "chat", chatID, "error", err)
	}
	return nil
}

// recordScheduledTurn appends the scheduled task and its result to the target
// chat's EXISTING session, so a later interactive turn has them in context. It
// deliberately does NOT create a session when none exists: a notification-only
// chat (one that's never been used interactively) shouldn't accumulate an
// ever-growing transcript of scheduled posts. The write is serialized with the
// interactive handler — and with concurrent deliveries to the same chat —
// through the per-chat mutex, so it can't interleave with a live turn's
// read-modify-write of the same session.
func (d telegramDeliverer) recordScheduledTurn(chatID int64, job schedule.Job, result string) error {
	if d.sessions == nil || result == "" {
		return nil
	}

	mu := getChatMutex(chatID)
	mu.Lock()
	defer mu.Unlock()

	cs, err := d.sessions.Load(chatID)
	if err != nil {
		return err
	}
	if cs == nil {
		return nil // no active conversation to attach to — deliver-only
	}

	label := job.Name
	if label == "" {
		label = job.ID
	}

	msgs := make([]llm.Message, len(cs.Messages), len(cs.Messages)+2)
	copy(msgs, cs.Messages)

	// Normally append a user turn (clearly marked as scheduler-originated, not
	// typed live) followed by the assistant result — well-formed and ready for
	// the next user message. But an existing session can already END on a bare
	// user message (a turn cancelled before the agent replied, or a
	// context-injection command). Appending another user turn there would put
	// two user messages back-to-back, which strict providers (Anthropic) reject
	// on the next call. In that case fold the label into a single assistant
	// message so roles stay alternating. Either way the session ends on an
	// assistant turn. Secrets are redacted by Store.Save.
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		msgs = append(msgs, llm.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("⏰ [scheduled task %q ran]\n%s", label, result),
		})
	} else {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: fmt.Sprintf("⏰ [scheduled task %q ran]\n%s", label, job.Task)},
			llm.Message{Role: "assistant", Content: result},
		)
	}
	return d.sessions.Save(chatID, msgs)
}

// startSchedulerForBot starts the embedded scheduler unless an external
// `odek schedule daemon` already holds the lock (in which case the bot defers
// to it, to avoid double-firing). It returns a stop func that releases the
// lock; the scheduler goroutine itself stops when ctx is cancelled.
func startSchedulerForBot(ctx context.Context, bot *telegram.Bot, resolved config.ResolvedConfig, system string, log telegram.Logger, st *schedule.Store, sessions *telegram.SessionManager) func() {
	if !resolved.Schedules.Enabled {
		log.Info("schedule: embedded scheduler disabled by config")
		return func() {}
	}
	if st == nil {
		log.Error("schedule: store unavailable, embedded scheduler not started")
		return func() {}
	}
	unlock, err := acquireScheduleLock()
	if err != nil {
		log.Info("schedule: embedded scheduler not started", "reason", err.Error())
		return func() {}
	}
	// Connect MCP servers once and share across fires. A failure here must not
	// take down the bot — log and continue without MCP tools for scheduled jobs.
	mcpTools, mcpCleanup, err := buildScheduledMCPTools(resolved)
	if err != nil {
		log.Error("schedule: MCP connect failed, scheduled jobs run without MCP tools", "error", err)
		mcpTools, mcpCleanup = nil, func() {}
	}
	sched := schedule.New(st,
		telegramRunner{resolved: resolved, system: system, bot: bot, mcpTools: mcpTools},
		telegramDeliverer{bot: bot, fallback: cliDeliverer{resolved: resolved}, sessions: sessions, log: log},
		schedulerOptions(resolved.Schedules, log),
	)
	scheduleUnlockRef = unlock
	mcpCleanupRef = mcpCleanup
	scheduleReloadRef = sched.Reload
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sched.Run(ctx)
	}()

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
		mcpCleanupRef = nil
		scheduleReloadRef = nil
		// Wait for the scheduler to drain in-flight jobs before tearing down the
		// shared MCP connections (otherwise a draining run sees broken pipes and
		// persists a misleading error state) and releasing the lock. Bounded so a
		// stuck job can't block shutdown indefinitely.
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			log.Error("schedule: drain timed out, proceeding with cleanup")
		}
		mcpCleanup()
		unlock()
	}
}

// ── headless agent execution ────────────────────────────────────────────

// buildHeadlessDangerConfig assembles the danger policy for an unattended
// scheduled run. It starts from the global config, overlays any schedule-
// specific policy, and then applies a non-overrideable safety floor.
//
// Safety floor:
//   - non_interactive is forced to "deny" (no human present to approve)
//   - destructive and blocked classes are always denied
//
// Schedule-specific overrides can allow network_egress, system_write,
// code_execution, install, or unknown for cron jobs.
func buildHeadlessDangerConfig(resolved config.ResolvedConfig) danger.DangerousConfig {
	dangerCfg := resolved.Dangerous
	mergeScheduleDangerous(&dangerCfg, resolved.Schedules.Dangerous)

	deny := "deny"
	dangerCfg.NonInteractive = &deny
	if dangerCfg.Classes == nil {
		dangerCfg.Classes = make(map[danger.RiskClass]danger.Action)
	}
	// Non-overrideable floor. Destructive and blocked are irreversible or
	// hard-coded malicious; scheduled runs must never execute them.
	for _, cls := range []danger.RiskClass{
		danger.Destructive,
		danger.Blocked,
	} {
		dangerCfg.Classes[cls] = danger.Deny
	}
	return dangerCfg
}

// runTaskHeadless builds a fresh agent with no terminal renderer and no
// interactive approver and runs one task to completion, returning the final
// text and the tokens it consumed. mcpTools are pre-connected MCP tools shared
// across fires (the builtin tools are rebuilt per call — they're cheap and
// must not be shared concurrently); pass nil for none.
//
// Safety: a scheduled task runs unattended, so there is no human to answer an
// approval prompt. builtinTools is given a nil approver, which means a
// Prompt-class op would fall back to DangerousConfig.NonInteractiveAction().
// The danger policy used is built by buildHeadlessDangerConfig, which applies
// a non-overrideable safety floor on top of the global + schedule-specific
// policy. Project-level odek.json is not allowed to set schedules.dangerous.
func runTaskHeadless(ctx context.Context, resolved config.ResolvedConfig, system, task string, mcpTools []odek.Tool) (string, int64, error) {
	dangerCfg := buildHeadlessDangerConfig(resolved)

	tools := builtinTools(dangerCfg, nil, nil, resolved.MaxConcurrency, resolved.APIKey, toolConfig{Transcription: resolved.Transcription, Vision: resolved.Vision, WebSearch: resolved.WebSearch}, nil)

	tools = append(tools, mcpTools...)

	// Apply tool filtering based on configuration (after MCP tools are appended
	// so disabled/enabled lists can reference MCP tool names too).
	tools = filterBuiltinTools(tools, resolved.Tools, nil)

	// Capture cumulative token usage from the final iteration so the Runner
	// can report it (the engine logs it; the bot bills it against the budget).
	// RunWithMessages drives the loop synchronously on this goroutine, so the
	// callback needs no synchronisation.
	var lastInfo loop.IterationInfo

	injectionGuard, err := guard.New(&resolved.Guard)
	if err != nil {
		return "", 0, fmt.Errorf("guard: %w", err)
	}
	if injectionGuard != nil {
		defer injectionGuard.Close()
		SetToolOutputGuard(injectionGuard, resolved.Guard)
	}

	agent, err := odek.New(odek.Config{
		Model:             resolved.Model,
		BaseURL:           resolved.BaseURL,
		APIKey:            resolved.APIKey,
		MaxIterations:     resolved.MaxIter,
		MaxToolParallel:   resolved.MaxToolParallel,
		SystemMessage:     system,
		UntrustedWrapper:  func(source, content string) string { return wrapUntrusted(context.Background(), source, content) },
		RuntimeContext:    odek.BuildRuntimeContext("schedule"),
		NoProjectFile:     resolved.NoAgents,
		Thinking:          resolved.Thinking,
		Temperature:       0,
		Tools:             tools,
		ToolFilter:        odek.ToolFilterConfig{Enabled: resolved.Tools.Enabled, Disabled: resolved.Tools.Disabled},
		Renderer:          render.New(io.Discard, false), // silent: unattended
		InteractionMode:   "off",
		PromptCaching:     resolved.PromptCaching,
		IterationCallback: func(info loop.IterationInfo) { lastInfo = info },
		Guard:             injectionGuard,
		GuardConfig:       resolved.Guard,
		// Scheduled jobs may analyze memory and past sessions (e.g. proactive
		// nudges over open goals), so they get the same memory wiring as
		// interactive runs (see cmd/odek/main.go).
		MemoryDir:    expandHome("~/.odek/memory"),
		MemoryConfig: resolved.Memory,
	})
	if err != nil {
		return "", 0, err
	}
	defer agent.Close()

	// Use agent.Run (not RunWithMessages): the engine prepends its stored system
	// message — which odek.New built as RuntimeContext + SystemMessage — so the
	// host/cwd/date header actually reaches the model. RunWithMessages would take
	// our messages verbatim and silently drop that context (breaking date-aware
	// tasks like "summarize today's calendar").
	result, err := agent.Run(ctx, task)
	tokens := int64(lastInfo.InputTokens + lastInfo.OutputTokens)
	return result, tokens, err
}

// mergeScheduleDangerous overlays schedule-specific dangerous policy onto the
// global policy. It mutates base in place. Lists are appended; scalar/map
// fields in schedule override global. Schedule policy comes from operator-
// controlled sources only (~/.odek/config.json and ODEK_SCHEDULES_DANGEROUS_*
// env vars); project-level odek.json is rejected by the config loader.
func mergeScheduleDangerous(base *danger.DangerousConfig, schedule danger.DangerousConfig) {
	if schedule.Classes != nil {
		if base.Classes == nil {
			base.Classes = make(map[danger.RiskClass]danger.Action)
		}
		for k, v := range schedule.Classes {
			base.Classes[k] = v
		}
	}
	base.Allowlist = append(base.Allowlist, schedule.Allowlist...)
	base.Denylist = append(base.Denylist, schedule.Denylist...)
	if schedule.DefaultAction != nil {
		base.DefaultAction = schedule.DefaultAction
	}
	if schedule.NonInteractive != nil {
		base.NonInteractive = schedule.NonInteractive
	}
}

// buildScheduledMCPTools connects the configured MCP servers ONCE so the
// connections can be reused across every scheduled fire (the MCP client
// serialises calls with a mutex, so sharing across concurrent runs is safe),
// instead of reconnecting per fire. Returns the tools, a cleanup to close the
// connections, and any error. With no MCP servers it's a no-op.
func buildScheduledMCPTools(resolved config.ResolvedConfig) ([]odek.Tool, func(), error) {
	if len(resolved.MCPServers) == 0 {
		return nil, func() {}, nil
	}
	var tools []odek.Tool
	cleanup, err := loadMCPTools(resolved, &tools)
	if err != nil {
		return nil, func() {}, fmt.Errorf("mcp: %w", err)
	}
	return tools, cleanup, nil
}

// ── helpers ─────────────────────────────────────────────────────────────

// schedulerOptions builds engine options from the resolved config, falling
// back to UTC if the configured default timezone can't be loaded.
func schedulerOptions(sc config.ScheduleConfig, logger schedule.Logger) schedule.Options {
	loc := time.UTC
	if sc.Timezone != "" {
		if l, err := time.LoadLocation(sc.Timezone); err == nil {
			loc = l
		} else {
			logger.Error("schedule: invalid default timezone, using UTC", "timezone", sc.Timezone, "error", err)
		}
	}
	return schedule.Options{
		MaxConcurrent: sc.MaxConcurrent,
		DefaultTZ:     loc,
		Catchup:       sc.Catchup,
		Logger:        logger,
	}
}

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
// Both the job label and the result are run through secret redaction before
// they are written, because task output can contain API keys, tokens, or
// private keys fetched or produced by the agent.
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
	name := redact.RedactSecrets(job.Name)
	safe := redact.RedactSecrets(result)
	_, err = fmt.Fprintf(f, "[%s] %s (%s)\n%s\n\n", time.Now().Format(time.RFC3339), name, job.ID, safe)
	return err
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
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid > 1 && syscall.Kill(pid, 0) == nil {
			// The PID is alive, but after an unclean exit the OS may have recycled
			// it onto an unrelated process. Confirm it's actually an odek process
			// (mirrors the Telegram instance lock) before refusing — otherwise a
			// recycled PID would make us refuse to start forever. On platforms
			// without /proc the read fails and we stay conservative (treat as live).
			owned := true
			if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
				owned = strings.Contains(string(cmdline), "odek")
			}
			if owned {
				return nil, fmt.Errorf("another schedule daemon is already running (PID %d)", pid)
			}
		}
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return nil, err
	}
	return func() { os.Remove(pidFile) }, nil
}
