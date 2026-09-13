package session

// EnsureMessageIDs assigns identities to previously unversioned records in
// place. Call before deriving or trimming a model-context projection.
func EnsureMessageIDs(messages []Message) {
	for i := range messages {
		if messages[i].ID == "" {
			messages[i].ID = generateID()
		}
	}
}

// MergeCheckpoint appends newly completed records to the durable transcript.
// Shortened context projections never replace completed actions or principal
// input. System records may be refreshed in place (for example a rolling
// digest), and input slices are never aliased by the returned snapshot.
func MergeCheckpoint(durable, snapshot []Message) []Message {
	merged := CloneMessages(durable)
	seen := make(map[string]int, len(merged))
	for i, m := range merged {
		if m.ID != "" {
			seen[m.ID] = i
		}
	}
	for snapshotIndex, m := range CloneMessages(snapshot) {
		if i, ok := seen[m.ID]; ok && m.ID != "" {
			if m.Role == "system" {
				merged[i] = m
			}
			continue
		}
		// Dynamic system context belongs before the next retained snapshot
		// record, not at the transcript tail after its principal input.
		if m.Role == "system" {
			insertAt := len(merged)
			for _, successor := range snapshot[snapshotIndex+1:] {
				if i, ok := seen[successor.ID]; ok && successor.ID != "" {
					insertAt = i
					break
				}
			}
			merged = append(merged, Message{})
			copy(merged[insertAt+1:], merged[insertAt:])
			merged[insertAt] = m
			for i := insertAt; i < len(merged); i++ {
				if merged[i].ID != "" {
					seen[merged[i].ID] = i
				}
			}
			continue
		}
		if m.ID != "" {
			seen[m.ID] = len(merged)
		}
		merged = append(merged, m)
	}
	return merged
}

// TurnMessages selects a turn by stable identity, independent of injected or
// removed context rows. Legacy history without this identity is not included.
func TurnMessages(messages []Message, turnID string) []Message {
	if turnID == "" {
		return nil
	}
	var turn []Message
	for _, m := range messages {
		if m.TurnID == turnID {
			turn = append(turn, m)
		}
	}
	return CloneMessages(turn)
}
