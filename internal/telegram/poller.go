package telegram

import (
	"context"
	"time"
)

// Poller implements long-polling for Telegram updates.
type Poller struct {
	Bot               *Bot
	Offset            int
	Interval          time.Duration
	Timeout           int
	consecutiveErrors int
	log               Logger
}

// NewPoller creates a new Poller with sensible defaults.
// Offset starts at 0, Interval is 1s, Timeout is 30s.
func NewPoller(bot *Bot) *Poller {
	return &Poller{
		Bot:      bot,
		Offset:   0,
		Interval: 1 * time.Second,
		Timeout:  30,
		log:      NewNopLogger(),
	}
}

// SetLogger sets the logger for this poller. If nil, a NopLogger is used.
func (p *Poller) SetLogger(l Logger) {
	if l == nil {
		p.log = NewNopLogger()
		return
	}
	p.log = l
}

// backoffDuration returns the sleep duration after consecutive poll errors.
// Formula: interval * 2^errors, capped at 60 * interval.
// Returns 0 for 0 errors (no backoff needed).
// errors is clamped to 30 to prevent integer overflow in 1<<errors.
func (p *Poller) backoffDuration(errors int) time.Duration {
	if errors <= 0 {
		return 0
	}
	if errors > 30 {
		errors = 30
	}
	shift := 1 << errors // 2^errors, safe after clamp
	d := p.Interval * time.Duration(shift)
	max := 60 * p.Interval
	if d > max {
		return max
	}
	return d
}

// Poll performs a single long-poll cycle.
// It calls GetUpdates with the current offset and timeout,
// advances the offset past the highest update ID received,
// and returns the updates (may be empty on timeout).
// Returns ctx.Err() if the context is cancelled.
func (p *Poller) Poll(ctx context.Context) ([]Update, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	updates, err := p.Bot.GetUpdatesContext(ctx, p.Offset, p.Timeout)
	if err != nil {
		return nil, err
	}

	if len(updates) > 0 {
		maxID := updates[0].ID
		for _, u := range updates[1:] {
			if u.ID > maxID {
				maxID = u.ID
			}
		}
		p.Offset = maxID + 1
	}

	return updates, nil
}

// Start begins the infinite long-polling loop.
// It calls Poll() repeatedly, sending each received update to the channel.
// On empty result (timeout), sleeps for Interval then retries.
// On error, sleeps with exponential backoff (interval * 2^consecutiveErrors,
// capped at 60 * interval), logs to the logger, but continues.
// Backoff resets to zero after a successful poll.
// When ctx is cancelled, closes the channel and returns ctx.Err().
func (p *Poller) Start(ctx context.Context, updates chan<- Update) error {
	defer close(updates)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		result, err := p.Poll(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// Fatal errors (401, 403, 409) should not be retried.
			if IsFatalAPIError(err) {
				p.log.Error("fatal poll error, stopping", "error", err)
				return err
			}

			p.consecutiveErrors++
			backoff := p.backoffDuration(p.consecutiveErrors)
			p.log.Error("poll error", "error", err, "consecutive_errors", p.consecutiveErrors, "backoff", backoff)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}

		// Successful poll — reset error counter.
		p.consecutiveErrors = 0

		if len(result) == 0 {
			// Timeout — no updates, sleep for Interval and retry.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(p.Interval):
			}
			continue
		}

		for _, u := range result {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case updates <- u:
			}
		}
	}
}
