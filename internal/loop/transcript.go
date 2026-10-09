package loop

import "github.com/BackendStack21/odek/internal/session"

// SetInvocationTurnID connects persisted messages to the caller's public turn.
// A supplied ID is used once, so a later standalone engine run cannot inherit it.
func (e *Engine) SetInvocationTurnID(id string) { e.invocationTurnID = id }

// Model context is a lossy projection; the completed transcript is not.
func (e *Engine) startTranscript(messages []session.Message) {
	session.EnsureMessageIDs(messages)
	e.activeTurnID = ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && messages[i].Name != "bg-notice" {
			if e.invocationTurnID != "" {
				messages[i].TurnID = e.invocationTurnID
			}
			if messages[i].TurnID == "" {
				messages[i].TurnID = messages[i].ID
			}
			e.activeTurnID = messages[i].TurnID
			break
		}
	}
	e.invocationTurnID = ""
	e.durableTranscript = session.CloneMessages(messages)
	e.durableIndex = session.NewCheckpointIndex(e.durableTranscript)
}

func (e *Engine) checkpointTranscript(messages []session.Message) {
	if e.activeTurnID == "" {
		return
	} // standalone trimming helpers
	for i := range messages {
		if messages[i].ID == "" && messages[i].TurnID == "" {
			messages[i].TurnID = e.activeTurnID
		}
	}
	session.EnsureMessageIDs(messages)
	if e.durableIndex == nil {
		e.durableIndex = session.NewCheckpointIndex(e.durableTranscript)
	}
	e.durableTranscript = session.MergeCheckpointInPlace(e.durableTranscript, e.durableIndex, messages)
}
