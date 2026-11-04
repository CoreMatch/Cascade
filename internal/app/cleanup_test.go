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
	mu       sync.Mutex
	calls    int
	failures int
	removed  int64
}

func (f *fakeCleaner) CleanupStaleProxyBindings() (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
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
