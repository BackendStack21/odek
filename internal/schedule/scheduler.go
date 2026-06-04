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
// stdout, a log file). It is called only when Run succeeded.
type Deliverer interface {
	Deliver(job Job, result string) error
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
	Logger        Logger           // defaults to NopLogger
	Now           func() time.Time // injectable clock for decisions (default time.Now); tests override
}

const (
	defaultMaxConcurrent = 2
	defaultReloadEvery   = 30 * time.Second
	maxSleep             = time.Hour // cap on a single idle sleep so the loop stays responsive
	resultPreviewRunes   = 280       // how much of a result we persist as LastResult
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
	defer s.mu.Unlock()

	seen := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		sched, err := compile(job, s.opts.DefaultTZ)
		if err != nil {
			// A malformed job is skipped, not fatal — one bad entry must not
			// stop every other schedule.
			s.log.Error("scheduler: skipping job with invalid schedule", "id", job.ID, "name", job.Name, "error", err)
			continue
		}
		seen[job.ID] = true
		s.jobs[job.ID] = job
		s.compiled[job.ID] = sched
		s.runs[job.ID] = state[job.ID].Runs

		newSig := job.Cron + "|" + job.Timezone
		if _, tracked := s.next[job.ID]; tracked && s.sig[job.ID] == newSig {
			// Unchanged and already scheduled — leave its next-fire intact so an
			// unrelated file edit doesn't shift this job.
			continue
		}
		s.sig[job.ID] = newSig

		// Determine the first fire for a newly-seen or changed job, applying the
		// missed-run policy against any persisted next-fire.
		prevNext := state[job.ID].NextRun
		catchup := job.Catchup || s.opts.Catchup
		switch {
		case !prevNext.IsZero() && prevNext.Before(now) && catchup:
			// A fire was missed while we were down and catchup is on → run asap.
			s.next[job.ID] = now
		case !prevNext.IsZero() && prevNext.Before(now):
			// Missed but no catchup → record the skip and move on.
			s.next[job.ID] = sched.Next(now)
			s.log.Info("scheduler: skipping missed fire", "id", job.ID, "name", job.Name)
			_ = s.store.SaveState(RunState{
				JobID: job.ID, LastStatus: StatusSkipped, LastRun: now,
				NextRun: s.next[job.ID], Runs: s.runs[job.ID],
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
}

// fireDue launches every job whose next-fire time is at or before now, then
// advances each fired job's next-fire to the following occurrence. A job
// already executing is not fired again (overlap guard); its schedule still
// advances so it doesn't pile up.
func (s *Scheduler) fireDue(ctx context.Context, now time.Time) {
	s.mu.Lock()
	var toFire []Job
	for id, nt := range s.next {
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

	for _, job := range toFire {
		s.sem <- struct{}{} // acquire (blocks if at MaxConcurrent)
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
	st := RunState{JobID: job.ID, LastRun: firedAt, Runs: s.runs[job.ID], NextRun: s.next[job.ID]}
	s.mu.Unlock()

	result, tokens, err := s.runner.Run(ctx, job)
	switch {
	case err != nil:
		st.LastStatus = StatusError
		st.LastError = err.Error()
		s.log.Error("scheduler: job run failed", "id", job.ID, "name", job.Name, "error", err)
	default:
		if derr := s.deliverer.Deliver(job, result); derr != nil {
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
