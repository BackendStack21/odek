package bgproc

import (
	"testing"
	"time"
)

// The output ring reserves its full steady-state capacity on the FIRST write,
// whatever its size: appendLocked builds slices.Grow(make([]byte, limit), limit)
// — keeping a backing array of at least 2x the limit (2504 KiB measured for
// the 1 MiB default) — even when the job only ever prints "ok". Finished job
// records keep their ring for bg_status / bg_output until pruneLocked (up to
// 64 + max_jobs records per manager session) or DeleteSession drops them, so
// each finished job that wrote anything pins ~2.5x max_output_bytes of mostly
// empty buffer. The documented bound is max_output_bytes of retained output
// per job; the backing array should grow with the output and reserve the
// two-window steady-state capacity only once the ring is full.
func TestRED_OutputRingReservesTwoWindowsForTinyOutput(t *testing.T) {
	const limit = 1 << 20
	const smallCap = 64 << 10
	r := &outputRing{limit: limit}
	if _, err := r.Write([]byte("ok\n")); err != nil {
		t.Fatal(err)
	}
	if c := cap(r.store); c > smallCap {
		t.Errorf("a 3-byte write reserved a %d KiB backing array (limit %d KiB); retained memory should track the output, not 2x the cap", c>>10, limit>>10)
	}

	// The same holds for finished jobs the manager keeps for bg_status /
	// bg_output: each record pins its ring's backing array.
	m := newTestManager(t, func(c *Config) { c.MaxOutputBytes = limit })
	const jobs = 4
	for i := 0; i < jobs; i++ {
		job, err := m.Start("s1", "echo ok", "", 0)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		waitFor(t, 10*time.Second, func() bool {
			got, _ := m.Get("s1", job.ID)
			return got.Status != StatusRunning
		})
	}
	m.mu.Lock()
	pinned := 0
	for _, e := range m.sess["s1"] {
		e.ring.mu.Lock()
		pinned += cap(e.ring.store)
		e.ring.mu.Unlock()
	}
	m.mu.Unlock()
	if pinned > jobs*smallCap {
		t.Errorf("%d finished `echo ok` jobs pin %d KiB of output buffers (%d KiB each)", jobs, pinned>>10, pinned/jobs>>10)
	}
}
