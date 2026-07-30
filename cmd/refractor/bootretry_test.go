package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryTransientBoot_PermanentErrorReturnsOnFirstAttempt(t *testing.T) {
	sentinel := errors.New("unknown adapter target")
	calls := 0
	err := retryTransientBoot(context.Background(), func() error {
		calls++
		return sentinel
	})
	assert.Equal(t, 1, calls, "a permanent config error must not pay any retry — it fails identically every time")
	assert.ErrorIs(t, err, sentinel)
}

func TestRetryTransientBoot_TransientErrorRetriesThenSucceeds(t *testing.T) {
	calls := 0
	err := retryTransientBoot(context.Background(), func() error {
		calls++
		if calls < bootRetryAttempts {
			return context.DeadlineExceeded
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, bootRetryAttempts, calls,
		"a lens must not be permanently stranded by a single transient blip during a boot/hot-reload burst")
}

func TestRetryTransientBoot_ConnectionErrorIsTransientToo(t *testing.T) {
	calls := 0
	err := retryTransientBoot(context.Background(), func() error {
		calls++
		if calls == 1 {
			return nats.ErrDisconnected
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

func TestRetryTransientBoot_ExhaustsAttemptsAndReturnsLastError(t *testing.T) {
	calls := 0
	err := retryTransientBoot(context.Background(), func() error {
		calls++
		return context.DeadlineExceeded
	})
	assert.Equal(t, bootRetryAttempts, calls, "must give up after bootRetryAttempts, not spin forever")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRetryTransientBoot_CtxCancelAbortsTheWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	start := time.Now()
	err := retryTransientBoot(ctx, func() error {
		calls++
		if calls == 1 {
			cancel()
		}
		return context.DeadlineExceeded
	})
	assert.Less(t, time.Since(start), bootRetryBaseDelay,
		"a cancelled ctx (process shutdown) must abort the backoff wait immediately, not sleep out the full delay")
	assert.Equal(t, 1, calls)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestIsTransientBootErr(t *testing.T) {
	assert.True(t, isTransientBootErr(context.DeadlineExceeded))
	assert.True(t, isTransientBootErr(nats.ErrConnectionClosed))
	assert.False(t, isTransientBootErr(errors.New("unknown adapter target \"bogus\"")))
}
