package app

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCleaner records cleanup passes and fails on demand.
type fakeCleaner struct {
	mu         sync.Mutex
	calls      int
	failures   int
	failAlways bool
	removed    int64
	callTimes  []time.Time
}

func (f *fakeCleaner) CleanupStaleProxyBindings() (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.callTimes = append(f.callTimes, time.Now())
	if f.failAlways {
		return 0, errors.New("database is locked")
	}
	if f.failures > 0 {
		f.failures--
		return 0, errors.New("database is locked")
	}
	return f.removed, nil
}

func (f *fakeCleaner) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeCleaner) CallTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Time, len(f.callTimes))
	copy(out, f.callTimes)
	return out
}

func waitForCleanerCalls(t *testing.T, f *fakeCleaner, want int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := f.Calls(); got >= want {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	return f.Calls()
}

func TestBindingCleanupLoopRunsPeriodically(t *testing.T) {
	f := &fakeCleaner{removed: 2}
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		bindingCleanupLoop(ctx, f, 15*time.Millisecond, logger)
		close(done)
	}()

	if got := waitForCleanerCalls(t, f, 3, 2*time.Second); got < 3 {
		t.Fatalf("expected at least 3 cleanup passes, got %d", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup loop did not stop after context cancellation")
	}
	final := f.Calls()
	time.Sleep(50 * time.Millisecond)
	if f.Calls() != final {
		t.Fatalf("loop kept cleaning after shutdown: %d -> %d", final, f.Calls())
	}
	if !strings.Contains(buf.String(), "removed 2 stale proxy binding(s)") {
		t.Fatalf("expected removal log line, got: %s", buf.String())
	}
}

// TestBindingCleanupLoopSurvivesFailures: a failed pass must be logged at
// error level and must neither panic nor stop the loop — the next tick runs
// normally.
func TestBindingCleanupLoopSurvivesFailures(t *testing.T) {
	f := &fakeCleaner{failures: 2}
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		bindingCleanupLoop(ctx, f, 10*time.Millisecond, logger)
		close(done)
	}()

	// Two failing passes plus at least one successful retry.
	if got := waitForCleanerCalls(t, f, 3, 2*time.Second); got < 3 {
		t.Fatalf("expected 2 failed passes plus a retry, got %d calls", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup loop did not stop after context cancellation")
	}

	logs := buf.String()
	if strings.Count(logs, "[cleanup] ERROR") != 2 {
		t.Fatalf("expected one error log per failed pass, got: %s", logs)
	}
	if !strings.Contains(logs, "database is locked") {
		t.Fatalf("error log missing the underlying reason: %s", logs)
	}
}

func TestBindingCleanupLoopStopsImmediatelyWhenCancelled(t *testing.T) {
	f := &fakeCleaner{}
	logger := log.New(&bytes.Buffer{}, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		bindingCleanupLoop(ctx, f, 10*time.Millisecond, logger)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup loop did not return for a cancelled context")
	}
	if f.Calls() != 0 {
		t.Fatalf("loop cleaned after cancellation: %d calls", f.Calls())
	}
}

// TestBindingCleanupLoopNeverRetriesWithinATick pins the failure cadence:
// when the database is unavailable the loop must not hammer it. Exactly one
// call happens per tick (no immediate retry burst), and nothing runs before
// the first tick fires.
func TestBindingCleanupLoopNeverRetriesWithinATick(t *testing.T) {
	f := &fakeCleaner{failAlways: true}
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	const interval = 40 * time.Millisecond
	go func() {
		bindingCleanupLoop(ctx, f, interval, logger)
		close(done)
	}()

	// The first pass must wait for the first tick, not run at startup.
	time.Sleep(interval / 2)
	if got := f.Calls(); got != 0 {
		t.Fatalf("cleanup ran before the first tick: %d calls", got)
	}

	if got := waitForCleanerCalls(t, f, 3, 2*time.Second); got < 3 {
		t.Fatalf("expected at least 3 cleanup passes, got %d", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup loop did not stop after context cancellation")
	}

	times := f.CallTimes()
	for i := 1; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		if gap < interval-5*time.Millisecond {
			t.Fatalf("gap between pass %d and %d was %s — a failing cleanup retried within the tick interval", i-1, i, gap)
		}
	}
	if got := strings.Count(buf.String(), "[cleanup] ERROR"); got != len(times) {
		t.Fatalf("expected one error log per failed pass (%d), got %d: %s", len(times), got, buf.String())
	}
}
