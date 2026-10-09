package main

import (
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/telegram"
)

// progressAPI is the slice of the Telegram bot the progress bubble needs.
type progressAPI interface {
	EditMessageText(chatID int64, messageID int, text string, opts *telegram.SendOpts) error
	SendMessage(chatID int64, text string, opts *telegram.SendOpts) (*telegram.Message, error)
}

// progressUpdate is one desired bubble state: the full text for an edit and
// the newest line for the send-a-new-message fallback.
type progressUpdate struct {
	text string
	line string
}

// progressBubble owns the Telegram progress message of one turn. The agent
// loop only records the desired state (submit, which never blocks on the
// network); a background goroutine applies it. Updates are latest-wins: while
// an edit is in flight or throttled only the newest state is kept.
//
// Ordering guarantee: finish waits for the worker, so once it returns every
// progress edit that will ever be issued has completed. The caller invokes it
// before sending the final answer, so no progress edit can land after (or
// race with) the answer, and a pending bubble deletion never overtakes an edit.
type progressBubble struct {
	abandoned bool // finish gave up waiting: never post new messages
	api       progressAPI
	chatID    int64
	replyTo   int
	throttle  time.Duration

	mu       sync.Mutex
	msgID    int
	canEdit  bool
	lastEdit time.Time
	pending  *progressUpdate
	flush    bool // apply the last pending update during finish

	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func newProgressBubble(api progressAPI, chatID int64, replyTo int, throttle time.Duration) *progressBubble {
	p := &progressBubble{
		api: api, chatID: chatID, replyTo: replyTo, throttle: throttle,
		canEdit: true,
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go p.run()
	return p
}

// setMessageID records the bubble's message; 0 disables progress updates.
func (p *progressBubble) setMessageID(id int) {
	p.mu.Lock()
	p.msgID = id
	p.mu.Unlock()
}

// messageID returns the current bubble message (0 when none).
func (p *progressBubble) messageID() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.msgID
}

// reset detaches the bubble so the next one appears below newer messages.
func (p *progressBubble) reset() {
	p.mu.Lock()
	p.msgID = 0
	p.pending = nil
	p.mu.Unlock()
}

// submit records the newest desired state and wakes the worker.
func (p *progressBubble) submit(text, line string) {
	p.mu.Lock()
	if p.msgID == 0 {
		p.mu.Unlock()
		return
	}
	p.pending = &progressUpdate{text: text, line: line}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// finish stops the worker and waits for it. With flush, a still-pending
// update is applied first (ignoring the throttle); otherwise it is discarded.
// progressFinishWait bounds how long finish waits for the worker's final
// edit. The bubble is cosmetic: a rate-limited edit must not hold back the
// answer, so past this bound the answer is sent and any late edit is
// abandoned to the worker.
const progressFinishWait = 3 * time.Second

func (p *progressBubble) finish(flush bool) {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.flush = flush
		p.mu.Unlock()
		close(p.stop)
	})
	select {
	case <-p.done:
	case <-time.After(progressFinishWait):
		// The answer goes out now; whatever the worker still manages to
		// edit is harmless, but it must not post a new message that would
		// land after the answer and the bubble cleanup.
		p.mu.Lock()
		p.abandoned = true
		p.mu.Unlock()
	}
}

func (p *progressBubble) run() {
	defer close(p.done)
	for {
		select {
		case <-p.wake:
		case <-p.stop:
			p.mu.Lock()
			flush := p.flush
			p.mu.Unlock()
			if flush {
				p.apply()
			}
			return
		}
		// Honour the edit throttle by waiting out the remainder; a newer
		// update arriving meanwhile simply replaces the pending one.
		p.mu.Lock()
		wait := p.throttle - time.Since(p.lastEdit)
		p.mu.Unlock()
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-p.stop:
				p.mu.Lock()
				flush := p.flush
				p.mu.Unlock()
				if flush {
					p.apply()
				}
				return
			}
		}
		p.apply()
	}
}

// apply sends the latest pending update, if any.
func (p *progressBubble) apply() {
	p.mu.Lock()
	u := p.pending
	p.pending = nil
	msgID, canEdit := p.msgID, p.canEdit
	p.mu.Unlock()
	if u == nil || msgID == 0 {
		return
	}
	opts := &telegram.SendOpts{ReplyToMessageID: p.replyTo}
	if canEdit {
		err := p.api.EditMessageText(p.chatID, msgID, u.text, nil)
		if err != nil {
			es := err.Error()
			if strings.Contains(es, "flood") || strings.Contains(es, "retry after") {
				// Editing is rate limited: continue with new messages, unless
				// the turn already moved on without us.
				p.mu.Lock()
				p.canEdit = false
				abandoned := p.abandoned
				p.mu.Unlock()
				if abandoned {
					return
				}
				msg, err2 := p.api.SendMessage(p.chatID, u.line, opts)
				p.mu.Lock()
				if err2 == nil && p.msgID == msgID {
					p.msgID = msg.ID
				}
				p.mu.Unlock()
			}
		}
		p.mu.Lock()
		p.lastEdit = time.Now()
		p.mu.Unlock()
		return
	}
	p.mu.Lock()
	abandoned := p.abandoned
	p.mu.Unlock()
	if abandoned {
		return
	}
	msg, err := p.api.SendMessage(p.chatID, u.line, opts)
	p.mu.Lock()
	if err == nil && p.msgID == msgID {
		p.msgID = msg.ID
	}
	p.lastEdit = time.Now()
	p.mu.Unlock()
}
