package diagnostics

import (
	"errors"
	"sync"
	"syscall"
	"testing"

	"github.com/BackendStack21/odek/internal/events"
)

func TestObserverLifecycleAndConcurrentReports(t *testing.T) {
	var mu sync.Mutex
	var got []events.Event
	restore := Install(func(ev events.Event) { mu.Lock(); defer mu.Unlock(); got = append(got, ev) })
	t.Cleanup(restore)
	Report("storage", "write", "session", nil)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				Report("storage", "write", "session", syscall.ENOSPC)
			}
		})
	}
	wg.Wait()
	Warning("config", "fallback", nil)
	if len(got) != 81 || got[0].SessionID != "session" || got[0].Data["error_class"] != "disk_full" || got[80].Type != "operation_warning" {
		t.Fatalf("unexpected reports: %+v", got)
	}
	undo := Install(func(events.Event) { panic("observer failed") })
	Report("test", "panic_isolation", "", errors.New("private"))
	undo()
	Report("storage", "restored", "", syscall.EACCES)
	if len(got) != 82 {
		t.Fatal("previous observer not restored")
	}
	restore()
	Report("storage", "disabled", "", syscall.EACCES)
	if len(got) != 82 {
		t.Fatal("observer remains active")
	}
}
