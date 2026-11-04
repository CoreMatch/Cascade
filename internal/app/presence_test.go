package app

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRegistrar records presence renewals and fails on demand.
type fakeRegistrar struct {
	mu       sync.Mutex
	calls    int
	failures int
	lastTTL  int
}

func (f *fakeRegistrar) RegisterPresence(ttlSeconds int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastTTL = ttlSeconds
	if f.failures > 0 {
		f.failures--
		return fmt.Errorf("request failed (status 503): {\"code\":\"SERVICE_UNAVAILABLE\",\"message\":\"core busy\"}")
	}
	return nil
}

func (f *fakeRegistrar) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// waitForCalls polls until the registrar has been called want times or the
// deadline passes.
func waitForCalls(t *testing.T, f *fakeRegistrar, want int, timeout time.Duration) int {
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

func TestPresenceLoopRenewsBeforeExpiry(t *testing.T) {
	f := &fakeRegistrar{}
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		presenceLoop(ctx, f, 120, 15*time.Millisecond, logger)
		close(done)
	}()

	// With a 15ms cadence several renewals must land well before the
	// (notional) TTL expires.
	if got := waitForCalls(t, f, 3, 2*time.Second); got < 3 {
		t.Fatalf("expected at least 3 renewals, got %d", got)
	}
	if f.lastTTL != 120 {
		t.Fatalf("renewal sent ttl_seconds = %d, want 120", f.lastTTL)
	}

	// Cancelling stops the loop; no further renewals may happen.
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("presence loop did not stop after context cancellation")
	}
	final := f.Calls()
	time.Sleep(50 * time.Millisecond)
	if f.Calls() != final {
		t.Fatalf("loop kept renewing after shutdown: %d -> %d", final, f.Calls())
	}
}

func TestPresenceLoopRetriesFailuresWithBackoff(t *testing.T) {
	origStart, origMax := presenceBackoffStart, presenceBackoffMax
	presenceBackoffStart, presenceBackoffMax = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { presenceBackoffStart, presenceBackoffMax = origStart, origMax })

	f := &fakeRegistrar{failures: 2}
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		presenceLoop(ctx, f, 120, 10*time.Millisecond, logger)
		close(done)
	}()

	// Two failed attempts must be retried until one succeeds.
	if got := waitForCalls(t, f, 3, 2*time.Second); got < 3 {
		t.Fatalf("expected 2 failed attempts plus a retry, got %d calls", got)
	}

	// Stop the loop before the test restores the backoff variables.
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("presence loop did not stop after context cancellation")
	}

	logs := buf.String()
	for _, want := range []string{"[presence] ERROR", "503", "SERVICE_UNAVAILABLE"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("failure log missing %q: %s", want, logs)
		}
	}
	if strings.Count(logs, "[presence] ERROR") < 2 {
		t.Fatalf("expected one error log per consecutive failure, got: %s", logs)
	}
	for _, leak := range []string{"client_secret", "password", "Bearer"} {
		if strings.Contains(logs, leak) {
			t.Fatalf("presence log leaks credential material %q: %s", leak, logs)
		}
	}
}

func TestPresenceLoopStopsImmediatelyWhenCancelled(t *testing.T) {
	f := &fakeRegistrar{}
	logger := log.New(&bytes.Buffer{}, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		presenceLoop(ctx, f, 120, 10*time.Millisecond, logger)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("presence loop did not return for a cancelled context")
	}
	if f.Calls() != 0 {
		t.Fatalf("loop renewed after cancellation: %d calls", f.Calls())
	}
}

func TestPresenceIntervalAndBackoff(t *testing.T) {
	if got := presenceInterval(120); got != 60*time.Second {
		t.Fatalf("presenceInterval(120) = %s, want 60s", got)
	}
	if got := presenceInterval(1); got != 500*time.Millisecond {
		t.Fatalf("presenceInterval(1) = %s, want 500ms", got)
	}
	if got := presenceInterval(0); got != 0 {
		t.Fatalf("presenceInterval(0) = %s, want 0 (never expires)", got)
	}
	if got := presenceInterval(-5); got != 0 {
		t.Fatalf("presenceInterval(-5) = %s, want 0 (never expires)", got)
	}

	origStart, origMax := presenceBackoffStart, presenceBackoffMax
	presenceBackoffStart, presenceBackoffMax = 5*time.Second, 60*time.Second
	t.Cleanup(func() { presenceBackoffStart, presenceBackoffMax = origStart, origMax })
	if got := nextPresenceBackoff(5 * time.Second); got != 10*time.Second {
		t.Fatalf("backoff doubled = %s, want 10s", got)
	}
	if got := nextPresenceBackoff(45 * time.Second); got != 60*time.Second {
		t.Fatalf("backoff capped = %s, want 60s", got)
	}
}
