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

// CheckpointIndex maps a record ID to its position in a durable transcript.
// A caller that merges repeatedly keeps one alongside the transcript so each
// merge costs the size of the snapshot, not the size of the transcript.
type CheckpointIndex map[string]int

// NewCheckpointIndex indexes durable. For duplicate IDs the last one wins.
func NewCheckpointIndex(durable []Message) CheckpointIndex {
	idx := make(CheckpointIndex, len(durable))
	for i, m := range durable {
		if m.ID != "" {
			idx[m.ID] = i
		}
	}
	return idx
}

// MergeCheckpoint appends newly completed records to the durable transcript.
// Shortened context projections never replace completed actions or principal
// input. System records may be refreshed in place (for example a rolling
// digest), and input slices are never aliased by the returned snapshot.
func MergeCheckpoint(durable, snapshot []Message) []Message {
	merged := CloneMessages(durable)
	return MergeCheckpointInPlace(merged, NewCheckpointIndex(merged), snapshot)
}

// MergeCheckpointInPlace applies MergeCheckpoint's rules to durable itself and
// returns the (possibly regrown) transcript. idx must describe durable and is
// kept current. Only records adopted from snapshot are cloned, so snapshot is
// never aliased; durable's existing records are not copied.
func MergeCheckpointInPlace(durable []Message, idx CheckpointIndex, snapshot []Message) []Message {
	merged := durable
	for snapshotIndex := range snapshot {
		m := &snapshot[snapshotIndex]
		if i, ok := idx[m.ID]; ok && m.ID != "" {
			if m.Role == "system" {
				merged[i] = cloneRecord(*m)
			} else if m.Superseded && !merged[i].Superseded {
				// A draft checkpointed earlier can be marked superseded later
				// (completion nudge, verification retry); replay clients must
				// not show it as a final answer.
				merged[i].Superseded = true
				merged[i].SupersededReason = m.SupersededReason
			}
			continue
		}
		// Dynamic system context belongs before the next retained snapshot
		// record, not at the transcript tail after its principal input.
		if m.Role == "system" {
			insertAt := len(merged)
			for _, successor := range snapshot[snapshotIndex+1:] {
				if i, ok := idx[successor.ID]; ok && successor.ID != "" {
					insertAt = i
					break
				}
			}
			merged = append(merged, Message{})
			copy(merged[insertAt+1:], merged[insertAt:])
			merged[insertAt] = cloneRecord(*m)
			for i := insertAt; i < len(merged); i++ {
				if merged[i].ID != "" {
					idx[merged[i].ID] = i
				}
			}
			continue
		}
		if m.ID != "" {
			idx[m.ID] = len(merged)
		}
		merged = append(merged, cloneRecord(*m))
	}
	return merged
}

// cloneRecord deep-copies one record with CloneMessages' rules.
func cloneRecord(m Message) Message { return CloneMessages([]Message{m})[0] }

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
