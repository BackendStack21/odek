package loop

import (
	"sync"
	"testing"
	"time"
)

// TestSignalHandlerSetVsEmitNoRace exercises the synchronization between
// SetSignalHandler and emitSignal: concurrent setter and heartbeat-emitter
// goroutines must not race on the handler field (go test -race).
func TestSignalHandlerSetVsEmitNoRace(t *testing.T) {
	e := &Engine{}
	var wg sync.WaitGroup
	stop := make(chan struct{})

	var fired sync.Mutex
	firedCount := 0
	e.SetSignalHandler(func(SignalEvent) {
		fired.Lock()
		firedCount++
		fired.Unlock()
	})

	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.SetSignalHandler(func(SignalEvent) {
				fired.Lock()
				firedCount++
				fired.Unlock()
			})
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.emitSignal(SignalEvent{Type: "tool_running"})
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	fired.Lock()
	defer fired.Unlock()
}
