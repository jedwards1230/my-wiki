package dispatch

import (
	"testing"
	"time"
)

// waitUntil polls cond every 5ms up to deadline. It is the preferred way to
// wait for debouncer output in tests: fixed time.Sleep waits are flaky on
// loaded CI (especially under -race) because timer goroutines may run later
// than the sleep allows. waitUntil returns as soon as cond reports true.
//
// Deadlines should be generous enough to absorb CI noise (roughly 4x the
// debounce window has proven reliable in this package's tests).
func waitUntil(t *testing.T, deadline time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if cond() {
		return
	}
	t.Fatalf("timed out after %v waiting for condition", deadline)
}

// waitStable returns once cond has held true for stable duration, or fails
// after deadline. Used to assert "no further flushes" after an initial
// observation — a fixed sleep would be needed to confirm stability, but we
// can at least bound it and fail fast if something fires.
func waitStable(t *testing.T, stable, deadline time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	stableEnd := time.Now().Add(stable)
	for time.Now().Before(end) {
		if !cond() {
			t.Fatalf("condition became false during stability window")
		}
		if time.Now().After(stableEnd) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition false at end of deadline")
	}
}
