package processor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/substrate"
)

const ttlTestHealthBucket = "health-kv"

func setupTTLHarness(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = jsstore.Dir(t)
	s := natsserver.RunServer(&opts)
	t.Cleanup(s.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: s.ClientURL(), Name: "health-ttl-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)

	_, err = conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         ttlTestHealthBucket,
		LimitMarkerTTL: time.Second, // enables AllowMsgTTL so KVPutWithTTL works in tests
	})
	if err != nil {
		t.Fatalf("create health-kv bucket: %v", err)
	}
	return ctx, conn
}

// newShortIntervalHeartbeater builds a heartbeater with an interval below the
// NFR-O1 10s production floor (NewHealthHeartbeater clamps to it) so TTL tests
// run fast — constructed directly since the floor is a production concern, not
// a mechanism constraint under test here.
func newShortIntervalHeartbeater(conn *substrate.Conn, instance string, interval time.Duration, logger *slog.Logger) *HealthHeartbeater {
	return &HealthHeartbeater{
		conn:          conn,
		bucket:        ttlTestHealthBucket,
		instance:      instance,
		startedAt:     time.Now(),
		interval:      interval,
		metrics:       &Metrics{},
		logger:        logger,
		lagThreshold:  defaultLaneLagThreshold,
		openIssues:    map[string]string{},
		ttlMultiplier: 1,
	}
}

// A heartbeat carries a TTL derived from interval × ttlMultiplier (Contract #5
// §5.6): a dead instance's key self-expires so the Lamplighter's "absent =
// crashed" signal works, while a live, continuously-heartbeating instance's key
// never disappears (each write re-arms the TTL clock).
func TestHeartbeat_TTLExpiryAndRearm(t *testing.T) {
	ctx, conn := setupTTLHarness(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("stopped heartbeat expires within TTL", func(t *testing.T) {
		h := newShortIntervalHeartbeater(conn, "proc-ttl-1", 1*time.Second, logger) // TTL = 1s (at the NATS floor)
		h.emit(ctx, "healthy")

		if _, err := conn.KVGet(ctx, ttlTestHealthBucket, h.healthKey()); err != nil {
			t.Fatalf("key missing right after emit: %v", err)
		}

		deadline := time.Now().Add(10 * time.Second)
		expired := false
		for time.Now().Before(deadline) {
			if _, err := conn.KVGet(ctx, ttlTestHealthBucket, h.healthKey()); errors.Is(err, substrate.ErrKeyNotFound) {
				expired = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !expired {
			t.Fatalf("heartbeat key %q never expired via its TTL", h.healthKey())
		}
	})

	t.Run("continued heartbeating re-arms and survives past the TTL window", func(t *testing.T) {
		h := newShortIntervalHeartbeater(conn, "proc-ttl-2", 1*time.Second, logger) // TTL = 1s

		stop := time.Now().Add(3 * time.Second) // > TTL
		for time.Now().Before(stop) {
			h.emit(ctx, "healthy")
			time.Sleep(300 * time.Millisecond)
		}
		if _, err := conn.KVGet(ctx, ttlTestHealthBucket, h.healthKey()); err != nil {
			t.Fatalf("re-armed key should still be present past one TTL window: %v", err)
		}
	})

	t.Run("multiplier=0 disables TTL (sticky key)", func(t *testing.T) {
		h := newShortIntervalHeartbeater(conn, "proc-ttl-3", 1*time.Second, logger)
		h.SetTTLMultiplier(0)
		h.emit(ctx, "healthy")

		time.Sleep(2 * time.Second) // > the 1s NATS TTL floor, would've expired if TTL were on
		if _, err := conn.KVGet(ctx, ttlTestHealthBucket, h.healthKey()); err != nil {
			t.Fatalf("multiplier=0 must disable TTL, but key is gone: %v", err)
		}
	})
}

// heartbeatTTL derives TTL = interval × ttlMultiplier, defaulting to
// healthkv.DefaultTTLMultiplier (10) unless overridden.
func TestHeartbeatTTL_Derivation(t *testing.T) {
	h := newTestHeartbeater()
	if got, want := h.heartbeatTTL(), 100*time.Second; got != want {
		t.Fatalf("default heartbeatTTL() = %v, want %v (10s interval × default multiplier 10)", got, want)
	}

	h.SetTTLMultiplier(2)
	if got, want := h.heartbeatTTL(), 20*time.Second; got != want {
		t.Fatalf("heartbeatTTL() after SetTTLMultiplier(2) = %v, want %v", got, want)
	}

	h.SetTTLMultiplier(-1)
	if got, want := h.heartbeatTTL(), time.Duration(0); got != want {
		t.Fatalf("negative multiplier must clamp to 0, heartbeatTTL() = %v, want %v", got, want)
	}
}

// newTestHealthAlertEmitter builds a HealthAlertEmitter with a caller-set
// diagnosticTTL — constructed directly (same package) since the constructors
// return the narrower Emitter interfaces and default to
// healthkv.DefaultDiagnosticTTL, too slow for a TTL-expiry test.
func newTestHealthAlertEmitter(conn *substrate.Conn, instance string, ttl time.Duration, logger *slog.Logger) *HealthAlertEmitter {
	return &HealthAlertEmitter{conn: conn, bucket: ttlTestHealthBucket, instance: instance, logger: logger, diagnosticTTL: ttl}
}

// Category B — sparse per-instance diagnostic breadcrumbs (malformed-operation
// markers, claim-attempts, commit-conflicts) carry a fixed diagnosticTTL so a
// dead instance's breadcrumbs clear within the window instead of persisting
// forever (health-kv-ttl-orphan-expiry-design.md Fire 1's second half).
func TestDiagnosticKeys_TTLExpiryAndRearm(t *testing.T) {
	ctx, conn := setupTTLHarness(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("malformed-operation marker expires within its fixed TTL (not re-armed)", func(t *testing.T) {
		h := newShortIntervalHeartbeater(conn, "proc-diag-1", 10*time.Second, logger)
		h.SetDiagnosticTTL(1 * time.Second)
		h.EmitMalformedOperation(ctx, "req-1", "bad envelope")
		key := "health.processor.proc-diag-1.malformed-operation.req-1"

		if _, err := conn.KVGet(ctx, ttlTestHealthBucket, key); err != nil {
			t.Fatalf("key missing right after emit: %v", err)
		}
		deadline := time.Now().Add(10 * time.Second)
		expired := false
		for time.Now().Before(deadline) {
			if _, err := conn.KVGet(ctx, ttlTestHealthBucket, key); errors.Is(err, substrate.ErrKeyNotFound) {
				expired = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !expired {
			t.Fatalf("malformed-operation key %q never expired via its TTL", key)
		}
	})

	t.Run("diagnosticTTL=0 disables TTL (sticky key)", func(t *testing.T) {
		h := newShortIntervalHeartbeater(conn, "proc-diag-2", 10*time.Second, logger)
		h.SetDiagnosticTTL(0)
		h.EmitMalformedOperation(ctx, "req-1", "bad envelope")
		key := "health.processor.proc-diag-2.malformed-operation.req-1"

		time.Sleep(2 * time.Second)
		if _, err := conn.KVGet(ctx, ttlTestHealthBucket, key); err != nil {
			t.Fatalf("diagnosticTTL=0 must disable TTL, but key is gone: %v", err)
		}
	})

	t.Run("claim-attempts counter re-arms across repeated attempts, then expires once they stop", func(t *testing.T) {
		e := newTestHealthAlertEmitter(conn, "proc-diag-3", 1*time.Second, logger)
		key := "health.processor.proc-diag-3.claim-attempts.success"

		stop := time.Now().Add(3 * time.Second) // > TTL
		for time.Now().Before(stop) {
			e.RecordClaimAttempt(ctx, "success")
			time.Sleep(300 * time.Millisecond)
		}
		if _, err := conn.KVGet(ctx, ttlTestHealthBucket, key); err != nil {
			t.Fatalf("re-armed claim-attempts key should still be present past one TTL window: %v", err)
		}

		deadline := time.Now().Add(10 * time.Second)
		expired := false
		for time.Now().Before(deadline) {
			if _, err := conn.KVGet(ctx, ttlTestHealthBucket, key); errors.Is(err, substrate.ErrKeyNotFound) {
				expired = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !expired {
			t.Fatalf("claim-attempts key %q never expired once attempts stopped", key)
		}
	})

	t.Run("commit-conflicts counter carries a TTL too", func(t *testing.T) {
		e := newTestHealthAlertEmitter(conn, "proc-diag-4", 1*time.Second, logger)
		key := "health.processor.proc-diag-4.commit-conflicts"

		e.RecordCommitConflict(ctx, CommitConflictInfo{ConflictingKey: "vtx.identity.abc", Lane: "default", OperationType: "UpdateThing"})
		if _, err := conn.KVGet(ctx, ttlTestHealthBucket, key); err != nil {
			t.Fatalf("key missing right after emit: %v", err)
		}
		deadline := time.Now().Add(10 * time.Second)
		expired := false
		for time.Now().Before(deadline) {
			if _, err := conn.KVGet(ctx, ttlTestHealthBucket, key); errors.Is(err, substrate.ErrKeyNotFound) {
				expired = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !expired {
			t.Fatalf("commit-conflicts key %q never expired via its TTL", key)
		}
	})
}
