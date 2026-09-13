package loop

import "github.com/BackendStack21/odek/internal/session"

// Model context is a lossy projection; the completed transcript is not.
func (e *Engine) startTranscript(messages []session.Message) {
	session.EnsureMessageIDs(messages)
	e.activeTurnID = ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && messages[i].Name != "bg-notice" {
			if messages[i].TurnID == "" {
				messages[i].TurnID = messages[i].ID
			}
			e.activeTurnID = messages[i].TurnID
			break
		}
	}
	e.durableTranscript = session.CloneMessages(messages)
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
	e.durableTranscript = session.MergeCheckpoint(e.durableTranscript, messages)
}
