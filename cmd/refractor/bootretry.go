package main

import (
	"context"
	"errors"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// bootRetryAttempts and bootRetryBaseDelay bound retryTransientBoot's total
// wait. Every call on this path runs serially on CoreKVSource's (or the
// hot-reload dispatcher's) single dispatch goroutine, so the ceiling has to
// stay small even at the full ~60-lens registry the lens-registry-restart-
// integrity design measured: worst case is bootRetryAttempts-1 waits of up
// to bootRetryBaseDelay*attempt, well under a second total, paid only on the
// error path.
const (
	bootRetryAttempts  = 3
	bootRetryBaseDelay = 250 * time.Millisecond
)

// retryTransientBoot retries fn up to bootRetryAttempts times when it fails
// with a transient NATS RTT error — context.DeadlineExceeded, or a
// connection-level fault substrate.IsConnectionError recognizes. Those are
// indistinguishable, from the caller's side, from the permanent config
// errors the same calls can return (unknown adapter target, a reserved
// bucket, a rejected stream create) except by classification: a permanent
// error still returns unchanged on the first attempt, so a real
// misconfiguration fails exactly as fast as before this existed. ctx
// cancellation (process shutdown) aborts the wait immediately.
func retryTransientBoot(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < bootRetryAttempts; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if !isTransientBootErr(err) || attempt == bootRetryAttempts-1 {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(bootRetryBaseDelay * time.Duration(attempt+1)):
		}
	}
	return err
}

// isTransientBootErr reports whether err is the class of NATS RTT blip a
// boot or hot-reload burst can produce transiently — worth a retry, unlike
// a permanent config error that would fail identically on every attempt.
func isTransientBootErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || substrate.IsConnectionError(err)
}
