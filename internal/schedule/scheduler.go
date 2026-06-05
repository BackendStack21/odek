package schedule

import (
	"context"
	"sync"
	"time"
)

// Runner executes one scheduled job's task and returns the agent's final text,
// the tokens it consumed (for budgeting/telemetry; 0 if unknown), and any
// error. Implementations live outside this package — the daemon and the
// Telegram bot each build an agent-backed Runner — so the engine stays
// decoupled from the agent and is trivially faked in tests.
type Runner interface {
	Run(ctx context.Context, job Job) (result string, tokens int64, err error)
}

// Deliverer routes a successful job result to its destination (Telegram chat,
// stdout, a log file). It is called only when Run succeeded. The context lets a
// slow delivery (e.g. an unreachable Telegram endpoint) be cancelled on
// shutdown instead of blocking the drain.
type Deliverer interface {
	Deliver(ctx context.Context, job Job, result string) error
}

// Logger is the minimal logging surface the engine needs, satisfied by the
// Telegram file logger and by NopLogger. Key/value variadics mirror slog.
type Logger interface {
	Info(msg string, kv ...any)
	Error(msg string, kv ...any)
}

// NopLogger discards all log output.
type NopLogger struct{}

func (NopLogger) Info(string, ...any)  {}
func (NopLogger) Error(string, ...any) {}

// Options configures a Scheduler. Zero values fall back to sensible defaults.
type Options struct {
	MaxConcurrent int              // max jobs running at once (default 2)
	DefaultTZ     *time.Location   // timezone for jobs with no Timezone set (default UTC)
	Catchup       bool             // global default: run a job once if a fire was missed while down
	ReloadEvery   time.Duration    // how often to poll schedules.json mtime for changes (default 30s)
	RunTimeout    time.Duration    // max wall-clock per job run (default 15m; <=0 keeps the engine default)
	Logger        Logger           // defaults to NopLogger
	Now           func() time.Time // injectable clock for decisions (default time.Now); tests override
}

const (
	defaultMaxConcurrent = 2
	defaultReloadEvery   = 30 * time.Second
	defaultRunTimeout    = 15 * time.Minute // bounds a single job so a hung run can't hold a slot forever
	maxSleep             = time.Hour        // cap on a single idle sleep so the loop stays responsive
	resultPreviewRunes   = 280              // how much of a result we persist as LastResult
)

// Scheduler fires jobs from a Store on their cron schedule, runs them through
// a Runner, and routes results through a Deliverer. It is safe for a single
// Run call; do not call Run concurrently on the same Scheduler.
type Scheduler struct {
	store     *Store
	runner    Runner
	deliverer Deliverer
	opts      Options
	log       Logger

	mu       sync.Mutex
	jobs     map[string]Job       // id → latest definition
	compiled map[string]*Schedule // id → parsed cron
	sig      map[string]string    // id → cron|tz signature, to detect changes on reload
	next     map[string]time.Time // id → next fire time
	runs     map[string]int       // id → total fires so far
	running  map[string]bool      // id → currently executing (overlap guard)

	sem chan struct{}  // bounds concurrent executions
	wg  sync.WaitGroup // tracks in-flight executions for graceful drain

	reloadCh chan struct{} // manual reconcile trigger (Reload); buffered, coalescing
}

// New builds a Scheduler. The store, runner, and deliverer are required.
func New(store *Store, runner Runner, deliverer Deliverer, opts Options) *Scheduler {
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = defaultMaxConcurrent
	}
	if opts.DefaultTZ == nil {
		opts.DefaultTZ = time.UTC
	}
	if opts.ReloadEvery <= 0 {
		opts.ReloadEvery = defaultReloadEvery
	}
	if opts.RunTimeout == 0 {
		opts.RunTimeout = defaultRunTimeout
	}
	if opts.Logger == nil {
		opts.Logger = NopLogger{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Scheduler{
		store:     store,
		runner:    runner,
		deliverer: deliverer,
		opts:      opts,
		log:       opts.Logger,
		jobs:      map[string]Job{},
		compiled:  map[string]*Schedule{},
		sig:       map[string]string{},
		next:      map[string]time.Time{},
		runs:      map[string]int{},
		running:   map[string]bool{},
		sem:       make(chan struct{}, opts.MaxConcurrent),
		reloadCh:  make(chan struct{}, 1),
	}
}

// Reload asks a running Run loop to re-read job definitions immediately instead
// of waiting for the next mtime poll — used after an out-of-band edit (e.g. the
// Telegram `/schedule` commands) so changes take effect at once. Safe to call
// from any goroutine; if a reload is already pending it coalesces, and if Run
// isn't active the buffered signal is consumed on the next loop iteration.
func (s *Scheduler) Reload() {
	select {
	case s.reloadCh <- struct{}{}:
	default:
	}
}

// Run drives the scheduler until ctx is cancelled. On cancellation it stops
// scheduling new fires and waits for in-flight executions to finish before
// returning ctx.Err().
func (s *Scheduler) Run(ctx context.Context) error {
	s.reconcile(s.opts.Now())
	lastMod := s.store.ModTime()

	reload := time.NewTicker(s.opts.ReloadEvery)
	defer reload.Stop()

	for {
		now := s.opts.Now()
		s.fireDue(ctx, now)

		timer := time.NewTimer(s.timeToNext(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			s.log.Info("scheduler: shutting down, draining in-flight jobs")
			s.wg.Wait()
			return ctx.Err()
		case <-timer.C:
			// Earliest fire is (about) due — loop and fireDue handles it.
		case <-reload.C:
			timer.Stop()
			if m := s.store.ModTime(); m.After(lastMod) {
				lastMod = m
				s.log.Info("scheduler: schedules changed, reloading")
				s.reconcile(s.opts.Now())
			}
		case <-s.reloadCh:
			// Explicit Reload() — reconcile now and resync lastMod so the mtime
			// poll doesn't redundantly reconcile the same write on its next tick.
			timer.Stop()
			lastMod = s.store.ModTime()
			s.log.Info("scheduler: manual reload")
			s.reconcile(s.opts.Now())
		}
	}
}

// Wait blocks until all in-flight executions complete. Intended for tests.
func (s *Scheduler) Wait() { s.wg.Wait() }

// reconcile loads job definitions, (re)compiles their schedules, and seeds the
// next-fire time for any job not already tracked. Jobs that disappeared or
// were disabled are dropped. It is called on startup and whenever the
// schedules file changes.
func (s *Scheduler) reconcile(now time.Time) {
	jobs, err := s.store.List()
	if err != nil {
		s.log.Error("scheduler: list jobs failed", "error", err)
		return
	}
	state, err := s.store.LoadState()
	if err != nil {
		s.log.Error("scheduler: load state failed", "error", err)
		state = map[string]RunState{}
	}

	s.mu.Lock()

	// Skip records to persist are collected here and written AFTER the lock is
	// released — SaveState does disk I/O and must not run under s.mu.
	var skips []RunState

	seen := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		newSig := jobSig(job)

		// Unchanged and already scheduled — leave its next-fire intact (so an
		// unrelated edit doesn't shift this job) and skip the relatively
		// expensive re-parse + timezone load entirely. The in-memory Runs
		// counter is authoritative here (a concurrent execute() may have already
		// incremented it past the on-disk value), so it is NOT reseeded.
		if _, tracked := s.next[job.ID]; tracked && s.sig[job.ID] == newSig {
			seen[job.ID] = true
			s.jobs[job.ID] = job
			continue
		}

		sched, err := compile(job, s.opts.DefaultTZ)
		if err != nil {
			// A malformed job is skipped, not fatal — one bad entry must not
			// stop every other schedule. Leaving it out of `seen` also drops a
			// previously-valid job that was just edited into an invalid one.
			s.log.Error("scheduler: skipping job with invalid schedule", "id", job.ID, "name", job.Name, "error", err)
			continue
		}
		// Reject expressions that parse but never match a real date (e.g. Feb 30,
		// hand-edited past Validate). Their next-fire would be the zero time,
		// which the engine would treat as perpetually due.
		if sched.Next(now).IsZero() {
			s.log.Error("scheduler: skipping job whose cron never matches a real date", "id", job.ID, "name", job.Name, "cron", job.Cron)
			continue
		}
		seen[job.ID] = true
		s.jobs[job.ID] = job
		s.compiled[job.ID] = sched
		s.sig[job.ID] = newSig
		// Seed the run counter from disk only when this job isn't currently
		// executing; an in-flight run owns the authoritative count.
		if !s.running[job.ID] {
			s.runs[job.ID] = state[job.ID].Runs
		}

		// Determine the first fire for a newly-seen or changed job, applying the
		// missed-run policy. Only trust the persisted NextRun if it was produced
		// by the SAME schedule signature; otherwise the cron/timezone changed
		// while we were down and the old slot is meaningless.
		prevNext := time.Time{}
		if st := state[job.ID]; st.Sig == newSig {
			prevNext = st.NextRun
		}
		catchup := job.Catchup || s.opts.Catchup
		switch {
		case !prevNext.IsZero() && prevNext.Before(now) && catchup:
			// A fire was missed while we were down and catchup is on → run asap.
			s.next[job.ID] = now
		case !prevNext.IsZero() && prevNext.Before(now):
			// Missed but no catchup → record the skip (persisted after unlock).
			s.next[job.ID] = sched.Next(now)
			s.log.Info("scheduler: skipping missed fire", "id", job.ID, "name", job.Name)
			skips = append(skips, RunState{
				JobID: job.ID, LastStatus: StatusSkipped, LastRun: now,
				NextRun: s.next[job.ID], Runs: s.runs[job.ID], Sig: newSig,
			})
		default:
			s.next[job.ID] = sched.Next(now)
		}
	}

	// Drop jobs that are gone or newly disabled.
	for id := range s.next {
		if !seen[id] {
			delete(s.next, id)
			delete(s.compiled, id)
			delete(s.jobs, id)
			delete(s.sig, id)
			delete(s.runs, id)
		}
	}
	s.mu.Unlock()

	// Persist skip records outside the lock; log failures (don't swallow them).
	for _, st := range skips {
		if err := s.store.SaveState(st); err != nil {
			s.log.Error("scheduler: save skip state failed", "id", st.JobID, "error", err)
		}
	}
}

// fireDue launches every job whose next-fire time is at or before now, then
// advances each fired job's next-fire to the following occurrence. A job
// already executing is not fired again (overlap guard); its schedule still
// advances so it doesn't pile up.
func (s *Scheduler) fireDue(ctx context.Context, now time.Time) {
	s.mu.Lock()
	var toFire []Job
	for id, nt := range s.next {
		// A zero next-fire means the cron never matches (reconcile/Validate
		// normally prevent this); never treat it as due (zero is before any
		// real instant) and drop it so it can't spin.
		if nt.IsZero() {
			delete(s.next, id)
			continue
		}
		if nt.After(now) {
			continue
		}
		sched := s.compiled[id]
		s.next[id] = sched.Next(now) // schedule the following fire regardless
		if s.running[id] {
			s.log.Info("scheduler: previous run still in flight, skipping this fire", "id", id)
			continue
		}
		s.running[id] = true
		s.runs[id]++
		toFire = append(toFire, s.jobs[id])
	}
	s.mu.Unlock()

	for i, job := range toFire {
		// Acquire a slot, but stay responsive to cancellation: if all slots are
		// held by long-running jobs and ctx is cancelled, don't wedge here —
		// release the overlap guard for every job we won't dispatch and bail so
		// Run() can reach its shutdown path.
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			s.mu.Lock()
			for _, j := range toFire[i:] {
				s.running[j.ID] = false
			}
			s.mu.Unlock()
			return
		}
		s.wg.Add(1)
		go func(job Job, firedAt time.Time) {
			defer s.wg.Done()
			defer func() { <-s.sem }()
			s.execute(ctx, job, firedAt)
		}(job, now)
	}
}

// execute runs a single job, delivers its result, and persists run state.
func (s *Scheduler) execute(ctx context.Context, job Job, firedAt time.Time) {
	defer func() {
		s.mu.Lock()
		s.running[job.ID] = false
		s.mu.Unlock()
	}()

	s.mu.Lock()
	st := RunState{JobID: job.ID, LastRun: firedAt, Runs: s.runs[job.ID], NextRun: s.next[job.ID], Sig: jobSig(job)}
	s.mu.Unlock()

	// Bound the run so a hung agent/tool can't hold its concurrency slot forever.
	runCtx := ctx
	if s.opts.RunTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, s.opts.RunTimeout)
		defer cancel()
	}
	result, tokens, err := s.runner.Run(runCtx, job)
	switch {
	case err != nil:
		st.LastStatus = StatusError
		st.LastError = err.Error()
		s.log.Error("scheduler: job run failed", "id", job.ID, "name", job.Name, "error", err)
	default:
		if derr := s.deliverer.Deliver(runCtx, job, result); derr != nil {
			st.LastStatus = StatusError
			st.LastError = "delivery: " + derr.Error()
			s.log.Error("scheduler: delivery failed", "id", job.ID, "name", job.Name, "error", derr)
		} else {
			st.LastStatus = StatusOK
			st.LastResult = preview(result)
			s.log.Info("scheduler: job delivered", "id", job.ID, "name", job.Name, "tokens", tokens)
		}
	}
	if serr := s.store.SaveState(st); serr != nil {
		s.log.Error("scheduler: save state failed", "id", job.ID, "error", serr)
	}
}

// timeToNext returns how long to sleep until the earliest pending fire,
// clamped to [0, maxSleep]. With no jobs it returns maxSleep.
func (s *Scheduler) timeToNext(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	earliest := time.Time{}
	for _, nt := range s.next {
		if earliest.IsZero() || nt.Before(earliest) {
			earliest = nt
		}
	}
	if earliest.IsZero() {
		return maxSleep
	}
	d := earliest.Sub(now)
	if d < 0 {
		return 0
	}
	if d > maxSleep {
		return maxSleep
	}
	return d
}

// jobSig is the change signature for a job's schedule. Two jobs with the same
// signature fire at the same times; a change means the persisted NextRun no
// longer corresponds to the current schedule.
func jobSig(j Job) string { return j.Cron + "|" + j.Timezone }

// compile parses a job's cron expression in its timezone (or the supplied
// default when the job specifies none).
func compile(job Job, defaultTZ *time.Location) (*Schedule, error) {
	loc := defaultTZ
	if job.Timezone != "" {
		l, err := time.LoadLocation(job.Timezone)
		if err != nil {
			return nil, err
		}
		loc = l
	}
	return ParseInLocation(job.Cron, loc)
}

// preview truncates a result for storage in RunState.LastResult.
func preview(s string) string {
	r := []rune(s)
	if len(r) <= resultPreviewRunes {
		return s
	}
	return string(r[:resultPreviewRunes]) + "…"
}
