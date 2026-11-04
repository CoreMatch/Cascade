package app

import (
	"context"
	"log"
	"time"
)

// bindingCleanupInterval is the cadence of the stale proxy-binding cleanup.
// Bindings are only eligible once idle for 30 days (store policy), so a
// daily pass is plenty. The variable exists so tests can shrink the wait;
// production uses the default.
var bindingCleanupInterval = 24 * time.Hour

// staleBindingCleaner is the subset of the database store the cleanup loop
// needs; tests substitute a fake.
type staleBindingCleaner interface {
	CleanupStaleProxyBindings() (int64, error)
}

// bindingCleanupLoop periodically deletes stale proxy bindings (guest
// bindings idle beyond the store's policy window). It runs on its own tick
// with its own error handling: a failed pass is logged at error level and
// retried on the next tick; it never panics and never affects the other
// loops. The loop returns when ctx is done.
func bindingCleanupLoop(ctx context.Context, db staleBindingCleaner, interval time.Duration, logger *log.Logger) {
	if db == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		removed, err := db.CleanupStaleProxyBindings()
		if err != nil {
			logger.Printf("[cleanup] ERROR stale proxy binding cleanup failed: %v", err)
			continue
		}
		if removed > 0 {
			logger.Printf("[cleanup] removed %d stale proxy binding(s)", removed)
		}
	}
}
