package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/schedule"
)

// This file implements the Telegram `/schedule` and `/schedules` slash commands,
// letting an authorized user manage scheduled tasks from inside the chat. The
// parsing/formatting lives here (store-backed, unit-testable); the `/schedule run`
// dispatch — which needs the live agent pipeline — stays in telegram.go and is
// driven by the runTask value this returns.
//
// Replies use the same "odek markdown" dialect as the other handlers
// (`*bold*`, `_italic_`, `` `code` ``); the bot's FormatResponse escapes
// reserved MarkdownV2 characters outside code spans and falls back to plain
// text on a parse error, so cron expressions and IDs are wrapped in backticks
// to stay literal.

const scheduleTelegramMaxRows = 20

// telegramScheduleReply handles a `/schedule <sub> …` command and returns the
// reply to send. When the subcommand is `run` and the job exists, runTask holds
// the job's task for the caller to dispatch through the normal chat pipeline
// (this helper has no agent access); it is empty otherwise.
//
// chatID is the originating chat — telegram-delivered jobs added here default to
// delivering back to it. reload, if non-nil, is invoked after a mutation so the
// embedded scheduler reconciles immediately. allowManage gates the mutating
// verbs; read-only verbs (list/view/next/help) always work.
func telegramScheduleReply(chatID int64, argsStr string, st *schedule.Store, reload func(), allowManage bool) (reply, runTask string) {
	if st == nil {
		return "❌ Schedule store is unavailable.", ""
	}
	sub, rest, _ := strings.Cut(strings.TrimSpace(argsStr), " ")
	sub = strings.ToLower(strings.TrimSpace(sub))
	rest = strings.TrimSpace(rest)

	// Read-only verbs — always available.
	switch sub {
	case "", "help":
		return scheduleTelegramUsage(), ""
	case "list", "ls":
		return scheduleTelegramList(st), ""
	case "view", "show":
		return scheduleTelegramView(st, rest), ""
	case "next":
		return scheduleTelegramNext(st, rest), ""
	}

	// Mutating verbs — gated by config.
	if !allowManage {
		return "🔒 Managing schedules from Telegram is disabled (`schedules.allow_telegram_management = false`). Use `odek schedule` on the host.", ""
	}
	switch sub {
	case "add":
		return scheduleTelegramAdd(chatID, rest, st, reload), ""
	case "rm", "remove", "delete":
		return scheduleTelegramRemove(st, rest, reload), ""
	case "enable":
		return scheduleTelegramSetEnabled(st, rest, true, reload), ""
	case "disable":
		return scheduleTelegramSetEnabled(st, rest, false, reload), ""
	case "run":
		return scheduleTelegramRun(st, rest)
	default:
		return fmt.Sprintf("❓ Unknown subcommand `%s`.\n\n%s", sub, scheduleTelegramUsage()), ""
	}
}

func scheduleTelegramUsage() string {
	return "⏰ *Schedule commands*\n\n" +
		"`/schedules` — list jobs\n" +
		"`/schedule add <cron> <task> [| opts]` — add a job (delivered to this chat)\n" +
		"`/schedule view <id>` — job detail\n" +
		"`/schedule next <id|cron>` — preview fire times\n" +
		"`/schedule run <id>` — run once now, here\n" +
		"`/schedule enable|disable <id>` — toggle a job\n" +
		"`/schedule rm <id>` — remove a job\n\n" +
		"*opts* (after ` | `): `deliver=stdout|log|telegram|telegram:<id>` `tz=<IANA>` `name=<label>` `catchup` `disabled`\n\n" +
		"Example:\n`/schedule add 0 9 * * 1-5 Summarize my unread emails | tz=Europe/Berlin`"
}

func scheduleTelegramList(st *schedule.Store) string {
	jobs, err := st.List()
	if err != nil {
		return "❌ " + err.Error()
	}
	if len(jobs) == 0 {
		return "⏰ *No scheduled jobs.*\n\nAdd one: `/schedule add 0 9 * * 1-5 your task`"
	}
	state, _ := st.LoadState()
	now := time.Now()
	var b strings.Builder
	b.WriteString("⏰ *Scheduled jobs*\n\n")
	for i, j := range jobs {
		if i >= scheduleTelegramMaxRows {
			fmt.Fprintf(&b, "\n_…and %d more — use `odek schedule list` on the host._", len(jobs)-scheduleTelegramMaxRows)
			break
		}
		onOff := "🟢"
		if !j.Enabled {
			onOff = "⚪️"
		}
		next := "—"
		if s, err := jobSchedule(j); err != nil {
			next = "invalid"
		} else if nt := s.Next(now); !nt.IsZero() {
			next = nt.Local().Format("Mon 02 Jan 15:04")
		}
		last := ""
		if rs, ok := state[j.ID]; ok && rs.LastStatus != "" {
			last = " · " + rs.LastStatus
		}
		fmt.Fprintf(&b, "%s `%s` `%s`%s\n   next %s\n", onOff, j.ID, j.Cron, last, next)
		if j.Name != "" {
			fmt.Fprintf(&b, "   _%s_\n", j.Name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func scheduleTelegramView(st *schedule.Store, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "❗ Usage: `/schedule view <id>`"
	}
	job, ok, err := st.Get(id)
	if err != nil {
		return "❌ " + err.Error()
	}
	if !ok {
		return fmt.Sprintf("❌ No job with ID `%s`.", id)
	}
	state, _ := st.LoadState()
	status := "enabled"
	if !job.Enabled {
		status = "disabled"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "⏰ *Job* `%s` (%s)\n", job.ID, status)
	if job.Name != "" {
		fmt.Fprintf(&b, "*Name:* %s\n", job.Name)
	}
	fmt.Fprintf(&b, "*Cron:* `%s`\n", job.Cron)
	if job.Timezone != "" {
		fmt.Fprintf(&b, "*TZ:* %s\n", job.Timezone)
	}
	fmt.Fprintf(&b, "*Deliver:* %s\n", deliverString(job.Deliver))
	fmt.Fprintf(&b, "*Task:* %s\n", job.Task)
	if rs, ok := state[job.ID]; ok {
		if rs.LastStatus != "" {
			fmt.Fprintf(&b, "*Last:* %s", rs.LastStatus)
			if !rs.LastRun.IsZero() {
				fmt.Fprintf(&b, " (%s)", rs.LastRun.Local().Format("Mon 02 Jan 15:04"))
			}
			b.WriteString("\n")
		}
		if rs.LastError != "" {
			fmt.Fprintf(&b, "*Error:* %s\n", rs.LastError)
		}
	}
	if s, err := jobSchedule(job); err == nil {
		b.WriteString("*Next fires:*\n")
		t := time.Now()
		for range 3 {
			t = s.Next(t)
			if t.IsZero() {
				break
			}
			fmt.Fprintf(&b, "  %s\n", t.Local().Format("Mon 02 Jan 15:04"))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func scheduleTelegramNext(st *schedule.Store, arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "❗ Usage: `/schedule next <id|cron>`"
	}
	var sc *schedule.Schedule
	var header string
	// A bare token with no spaces or cron metacharacters may be a job ID.
	if !strings.ContainsAny(arg, " *") {
		if job, ok, err := st.Get(arg); err == nil && ok {
			s, jerr := jobSchedule(job)
			if jerr != nil {
				return "❌ " + jerr.Error()
			}
			sc, header = s, fmt.Sprintf("⏰ Job `%s` (`%s`)", job.ID, job.Cron)
		}
	}
	if sc == nil {
		s, err := schedule.Parse(arg)
		if err != nil {
			return "❌ " + err.Error()
		}
		sc, header = s, fmt.Sprintf("⏰ `%s` (UTC)", arg)
	}
	var b strings.Builder
	b.WriteString(header + "\n")
	t := time.Now()
	for range 5 {
		t = sc.Next(t)
		if t.IsZero() {
			b.WriteString("  _(no further fires within the horizon)_\n")
			break
		}
		fmt.Fprintf(&b, "  %s\n", t.Local().Format("Mon 02 Jan 15:04 MST"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func scheduleTelegramAdd(chatID int64, args string, st *schedule.Store, reload func()) string {
	job, errMsg := parseTelegramScheduleAdd(chatID, args)
	if errMsg != "" {
		return errMsg
	}
	saved, err := st.Add(job)
	if err != nil {
		return "❌ " + err.Error()
	}
	if reload != nil {
		reload()
	}
	next := "—"
	if s, err := jobSchedule(saved); err == nil {
		if nt := s.Next(time.Now()); !nt.IsZero() {
			next = nt.Local().Format("Mon 02 Jan 15:04")
		}
	}
	status := "enabled"
	if !saved.Enabled {
		status = "disabled"
	}
	return fmt.Sprintf("✅ *Added* `%s` (%s)\n*Name:* %s\n*Cron:* `%s`\n*Deliver:* %s\n*Next:* %s",
		saved.ID, status, saved.Name, saved.Cron, deliverString(saved.Deliver), next)
}

// parseTelegramScheduleAdd turns a chat-friendly add string into a Job. It
// returns either a populated Job or a user-facing error reply (never both).
//
// Grammar: <cron|@macro> <task…> [| key=value … flag …]. Cron's fixed arity
// resolves the cron/task boundary without quoting — an @macro is one token, a
// classic expression is exactly five whitespace fields, and the remainder is the
// task. Options come after a literal "|".
func parseTelegramScheduleAdd(chatID int64, args string) (schedule.Job, string) {
	args = strings.TrimSpace(args)
	if args == "" {
		return schedule.Job{}, "❗ Usage: `/schedule add <cron> <task> [| opts]`\n\nExample: `/schedule add 0 9 * * 1-5 Stand-up reminder`"
	}
	main, optStr, _ := strings.Cut(args, "|")
	cron, task, ok := splitCronTask(main)
	if !ok {
		return schedule.Job{}, "❗ Could not read the cron and task. Provide 5 cron fields (or an `@macro`) then the task:\n`/schedule add 0 9 * * 1-5 your task`"
	}
	opts := parseScheduleOpts(optStr)

	delStr := opts["deliver"]
	if delStr == "" {
		delStr = "telegram"
	}
	del, err := parseDeliver(delStr)
	if err != nil {
		return schedule.Job{}, "❌ " + err.Error()
	}
	// A telegram delivery with no explicit chat defaults to THIS chat — the
	// natural expectation when adding from a conversation.
	if del.Kind == schedule.DeliverTelegram && del.ChatID == 0 {
		del.ChatID = chatID
	}

	name := opts["name"]
	if name == "" {
		name = firstWords(task, 6)
	}
	return schedule.Job{
		Name:     name,
		Cron:     cron,
		Task:     task,
		Deliver:  del,
		Timezone: opts["tz"],
		Catchup:  opts["catchup"] != "",
		Enabled:  opts["disabled"] == "",
	}, ""
}

// splitCronTask separates a cron expression (a single @macro or exactly five
// whitespace fields) from the trailing task text.
func splitCronTask(s string) (cron, task string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	if strings.HasPrefix(s, "@") {
		macro, rest, _ := strings.Cut(s, " ")
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return "", "", false
		}
		return macro, rest, true
	}
	fields := strings.Fields(s)
	if len(fields) < 6 { // 5 cron fields + at least one task word
		return "", "", false
	}
	return strings.Join(fields[:5], " "), strings.Join(fields[5:], " "), true
}

// parseScheduleOpts parses the option tail into a map. `key=value` pairs map to
// their value; bare flags (e.g. `catchup`) map to "true".
func parseScheduleOpts(s string) map[string]string {
	out := map[string]string{}
	for _, tok := range strings.Fields(s) {
		if k, v, ok := strings.Cut(tok, "="); ok {
			out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		} else {
			out[strings.ToLower(tok)] = "true"
		}
	}
	return out
}

func scheduleTelegramRemove(st *schedule.Store, id string, reload func()) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "❗ Usage: `/schedule rm <id>`"
	}
	if err := st.Remove(id); err != nil {
		return "❌ " + err.Error()
	}
	if reload != nil {
		reload()
	}
	return fmt.Sprintf("🗑️ Removed `%s`.", id)
}

func scheduleTelegramSetEnabled(st *schedule.Store, id string, enabled bool, reload func()) string {
	id = strings.TrimSpace(id)
	verb := "enable"
	if !enabled {
		verb = "disable"
	}
	if id == "" {
		return fmt.Sprintf("❗ Usage: `/schedule %s <id>`", verb)
	}
	if err := st.SetEnabled(id, enabled); err != nil {
		return "❌ " + err.Error()
	}
	if reload != nil {
		reload()
	}
	if enabled {
		return fmt.Sprintf("🟢 Enabled `%s`.", id)
	}
	return fmt.Sprintf("⚪️ Disabled `%s` (kept, won't fire).", id)
}

func scheduleTelegramRun(st *schedule.Store, id string) (reply, runTask string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "❗ Usage: `/schedule run <id>`", ""
	}
	job, ok, err := st.Get(id)
	if err != nil {
		return "❌ " + err.Error(), ""
	}
	if !ok {
		return fmt.Sprintf("❌ No job with ID `%s`.", id), ""
	}
	return fmt.Sprintf("🏃 Running `%s` (%s) now — the result will arrive here.", job.ID, job.Name), job.Task
}
