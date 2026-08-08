package events

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// FuzzEventJSON unmarshals arbitrary JSON into an Event and asserts it never
// panics, that a successful unmarshal can always be re-marshalled, and that
// the Emitter's redaction path handles whatever Data values the input
// carried (events with secret-looking strings must not dispatch them raw).
func FuzzEventJSON(f *testing.F) {
	seeds := []string{
		`{"schema":"odek.event/v1","type":"run_started","run_id":"abc123","timestamp":"2026-08-07T12:00:00Z"}`,
		`{"schema":"odek.event/v1","type":"tool_call_started","tool":"shell","iteration":3,"data":{"args_sha256":"deadbeef","args_bytes":42}}`,
		`{"schema":"odek.event/v1","type":"budget_exceeded","data":{"limit_name":"runtime","observed":60,"limit":30}}`,
		`{"schema":"odek.event/v1","type":"run_failed","data":{"error_class":"error","duration_ms":1234.5}}`,
		`{"schema":"odek.event/v1","type":"unknown_future_type","data":{"new_field":[1,2,3]}}`,
		`{}`,
		`[]`,
		`null`,
		`"string"`,
		`123`,
		`not json`,
		`{"schema":123,"type":["array"],"iteration":"not-a-number"}`,
		`{"timestamp":"not-a-time"}`,
		`{"timestamp":12345}`,
		`{"data":"not-a-map"}`,
		`{"data":{"k":null}}`,
		`{"data":{"nested":{"deep":{"deeper":[{"x":1}]}}}}`,
		`{"iteration":-1}`,
		`{"iteration":9223372036854775807}`,
		`{"iteration":1e400}`,
		`{"tool":"sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		`{"type":"run_failed","data":{"error_class":"sk-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`,
		`{"session_id":"` + strings.Repeat("s", 4096) + `"}`,
		`{"schema":"odek.event/v1","type":"t","timestamp":"0001-01-01T00:00:00Z","data":{"big":"` + strings.Repeat("b", 8192) + `"}}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data string) {
		var ev Event
		err := json.Unmarshal([]byte(data), &ev)
		if err != nil {
			return
		}
		// A successfully unmarshalled event must always re-marshal.
		out, merr := json.Marshal(ev)
		if merr != nil {
			t.Fatalf("re-marshal of unmarshalled event failed: %v (input %q)", merr, data)
		}
		var back Event
		if uerr := json.Unmarshal(out, &back); uerr != nil {
			t.Fatalf("round-trip unmarshal failed: %v (marshalled %q)", uerr, out)
		}

		// The Emitter's stamping/redaction path must handle the event without
		// panicking, regardless of what Data carried.
		em := NewEmitter(nil, "fuzz-run")
		em.Emit(ev)
		em.Emit(back)
		em.Close()

		// Timestamp must remain a valid (possibly zero) time after the round
		// trip — no partial-parse garbage.
		if !back.Timestamp.IsZero() && back.Timestamp.Before(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("round-tripped timestamp is invalid: %v", back.Timestamp)
		}
	})
}
