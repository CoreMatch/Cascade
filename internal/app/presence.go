package app

import (
	"context"
	"log"
	"time"
)

// Presence renewal cadence and failure backoff. The variables exist so tests
// can shrink the real-time waits; production uses the defaults.
var (
	presenceBackoffStart = 5 * time.Second
	presenceBackoffMax   = 60 * time.Second
)

// presenceRegistrar is the subset of the HRPAuth client the renewal loop
// needs; tests substitute a fake.
type presenceRegistrar interface {
	RegisterPresence(ttlSeconds int) error
}

// presenceInterval returns the renewal cadence for a given TTL: half the
// TTL, so a renewal lands well before the record expires. A TTL <= 0 means
// the record never expires (per contract), so no renewal is needed.
func presenceInterval(ttlSeconds int) time.Duration {
	if ttlSeconds <= 0 {
		return 0
	}
	return time.Duration(ttlSeconds) * time.Second / 2
}

// nextPresenceBackoff doubles the retry delay, capped at presenceBackoffMax.
func nextPresenceBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > presenceBackoffMax {
		next = presenceBackoffMax
	}
	return next
}

// presenceLoop re-registers the service presence before its TTL expires.
// The normal cadence is presenceInterval; failed renewals are retried with
// exponential backoff and logged at error level. The loop returns when ctx
// is done. The HRPAuth contract has no deregister endpoint, so nothing is
// sent on shutdown; the record simply expires after its TTL.
func presenceLoop(ctx context.Context, registrar presenceRegistrar, ttlSeconds int, interval time.Duration, logger *log.Logger) {
	if registrar == nil || interval <= 0 {
		return
	}
	wait := interval
	backoff := presenceBackoffStart
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err := registrar.RegisterPresence(ttlSeconds); err != nil {
			logger.Printf("[presence] ERROR renewal failed, retrying in %s: %v", backoff, err)
			wait = backoff
			backoff = nextPresenceBackoff(backoff)
			continue
		}
		wait = interval
		backoff = presenceBackoffStart
	}
}
