package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/events"
)

func TestSweepRuntimeLogExpiration(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "runtime.log")
	expired, _ := json.Marshal(events.Event{Type: "run_completed", Timestamp: time.Now().Add(-48 * time.Hour), SessionID: "expired"})
	recent, _ := json.Marshal(events.Event{Type: "run_completed", Timestamp: time.Now(), SessionID: "recent"})
	raw := append(append(append(expired, '\n'), recent...), '\n')
	for _, name := range []string{path, path + ".1"} {
		if err := os.WriteFile(name, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := Sweep(context.Background(), home, Config{RuntimeLogMaxAgeHours: 0})
	if err != nil || rep.RuntimeLogRecordsRemoved != 0 {
		t.Fatalf("zero retention: %+v %v", rep, err)
	}
	rep, err = Sweep(context.Background(), home, Config{RuntimeLogMaxAgeHours: 24})
	if err != nil || rep.RuntimeLogRecordsRemoved != 2 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	for _, name := range []string{path, path + ".1"} {
		b, _ := os.ReadFile(name)
		if strings.Contains(string(b), "expired") || !strings.Contains(string(b), "recent") {
			t.Fatalf("retention: %s", b)
		}
	}
}
