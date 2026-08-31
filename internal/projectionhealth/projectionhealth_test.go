package projectionhealth_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/projectionhealth"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func startConnWithHealthKV(t *testing.T) *substrate.Conn {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	_, nc := natsfixture.Server(t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: bootstrap.HealthKVBucket})
	require.NoError(t, err)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	return conn
}

func TestCheck_NilConn(t *testing.T) {
	st := projectionhealth.Check(context.Background(), nil, "some-rule")
	if st.Known {
		t.Fatalf("Known = true for a nil conn, want false")
	}
}

func TestCheck_NoEntryYet(t *testing.T) {
	conn := startConnWithHealthKV(t)
	st := projectionhealth.Check(context.Background(), conn, "never-reported-rule")
	if st.Known {
		t.Fatalf("Known = true for a rule that never reported, want false")
	}
}

func TestCheck_ActiveRule(t *testing.T) {
	conn := startConnWithHealthKV(t)
	kv, err := conn.OpenKV(context.Background(), bootstrap.HealthKVBucket)
	require.NoError(t, err)
	r := health.New(kv, "clinicAppointmentsRead")
	require.NoError(t, r.SetActive(context.Background()))

	st := projectionhealth.Check(context.Background(), conn, "clinicAppointmentsRead")
	if !st.Known {
		t.Fatalf("Known = false for an active rule, want true")
	}
	if st.Paused {
		t.Fatalf("Paused = true for an active rule, want false")
	}
}

func TestCheck_PausedRule(t *testing.T) {
	conn := startConnWithHealthKV(t)
	kv, err := conn.OpenKV(context.Background(), bootstrap.HealthKVBucket)
	require.NoError(t, err)
	r := health.New(kv, "clinicAppointmentsRead")
	require.NoError(t, r.SetPaused(context.Background(), health.PauseReasonStructural, "some failure"))

	st := projectionhealth.Check(context.Background(), conn, "clinicAppointmentsRead")
	if !st.Known {
		t.Fatalf("Known = false for a paused rule, want true")
	}
	if !st.Paused {
		t.Fatalf("Paused = false for a rule SetPaused was called on, want true")
	}
	if st.PauseReason != health.PauseReasonStructural {
		t.Fatalf("PauseReason = %q, want %q", st.PauseReason, health.PauseReasonStructural)
	}
}

func TestCheck_StalledLag(t *testing.T) {
	orig := projectionhealth.StallThreshold
	projectionhealth.StallThreshold = 50 * time.Millisecond
	defer func() { projectionhealth.StallThreshold = orig }()

	conn := startConnWithHealthKV(t)
	kv, err := conn.OpenKV(context.Background(), bootstrap.HealthKVBucket)
	require.NoError(t, err)
	r := health.New(kv, "leaseApplicationsRead")
	require.NoError(t, r.SetActive(context.Background()))
	// Stamp an old lagProgressAt (older than the shrunk StallThreshold) with a
	// nonzero lag — the backlog has stopped shrinking.
	oldProgress := time.Now().Add(-time.Hour)
	require.NoError(t, r.SetProjectionProgress(context.Background(), 42, time.Time{}, oldProgress, 0, time.Time{}))

	st := projectionhealth.Check(context.Background(), conn, "leaseApplicationsRead")
	if !st.Known {
		t.Fatalf("Known = false, want true")
	}
	if st.Paused {
		t.Fatalf("Paused = true, want false")
	}
	if !st.Stalled {
		t.Fatalf("Stalled = false, want true")
	}
	if st.StallReason == "" {
		t.Fatalf("StallReason = %q, want non-empty", st.StallReason)
	}
}

func TestCheck_DrainingLagNotStalled(t *testing.T) {
	orig := projectionhealth.StallThreshold
	projectionhealth.StallThreshold = time.Hour
	defer func() { projectionhealth.StallThreshold = orig }()

	conn := startConnWithHealthKV(t)
	kv, err := conn.OpenKV(context.Background(), bootstrap.HealthKVBucket)
	require.NoError(t, err)
	r := health.New(kv, "landlordLeaseApplicationsRead")
	require.NoError(t, r.SetActive(context.Background()))
	// A fresh lagProgressAt — lag is still actively draining.
	freshProgress := time.Now()
	require.NoError(t, r.SetProjectionProgress(context.Background(), 42, time.Time{}, freshProgress, 0, time.Time{}))

	st := projectionhealth.Check(context.Background(), conn, "landlordLeaseApplicationsRead")
	if !st.Known {
		t.Fatalf("Known = false, want true")
	}
	if st.Stalled {
		t.Fatalf("Stalled = true for a fresh lagProgressAt, want false")
	}
}

func TestCheck_ZeroLagNotStalled(t *testing.T) {
	orig := projectionhealth.StallThreshold
	projectionhealth.StallThreshold = 50 * time.Millisecond
	defer func() { projectionhealth.StallThreshold = orig }()

	conn := startConnWithHealthKV(t)
	kv, err := conn.OpenKV(context.Background(), bootstrap.HealthKVBucket)
	require.NoError(t, err)
	r := health.New(kv, "clinicAppointmentsRead")
	require.NoError(t, r.SetActive(context.Background()))
	// Zero lag with an old lagProgressAt must never read as stalled — there is
	// no outstanding backlog to be stuck.
	oldProgress := time.Now().Add(-time.Hour)
	require.NoError(t, r.SetProjectionProgress(context.Background(), 0, time.Time{}, oldProgress, 0, time.Time{}))

	st := projectionhealth.Check(context.Background(), conn, "clinicAppointmentsRead")
	if !st.Known {
		t.Fatalf("Known = false, want true")
	}
	if st.Stalled {
		t.Fatalf("Stalled = true for zero ConsumerLag, want false")
	}
}

func TestCheck_PausedTakesPrecedenceOverStall(t *testing.T) {
	orig := projectionhealth.StallThreshold
	projectionhealth.StallThreshold = 50 * time.Millisecond
	defer func() { projectionhealth.StallThreshold = orig }()

	conn := startConnWithHealthKV(t)
	kv, err := conn.OpenKV(context.Background(), bootstrap.HealthKVBucket)
	require.NoError(t, err)
	r := health.New(kv, "clinicAppointmentsRead")
	require.NoError(t, r.SetActive(context.Background()))
	oldProgress := time.Now().Add(-time.Hour)
	require.NoError(t, r.SetProjectionProgress(context.Background(), 42, time.Time{}, oldProgress, 0, time.Time{}))
	require.NoError(t, r.SetPaused(context.Background(), health.PauseReasonStructural, "some failure"))

	st := projectionhealth.Check(context.Background(), conn, "clinicAppointmentsRead")
	if !st.Known {
		t.Fatalf("Known = false, want true")
	}
	if !st.Paused {
		t.Fatalf("Paused = false, want true")
	}
	if st.Stalled {
		t.Fatalf("Stalled = true for a paused rule, want false (paused takes precedence)")
	}
	if st.StallReason != "" {
		t.Fatalf("StallReason = %q, want empty when paused", st.StallReason)
	}
}

func TestCheck_StalledAckPending(t *testing.T) {
	orig := projectionhealth.StallThreshold
	projectionhealth.StallThreshold = 50 * time.Millisecond
	defer func() { projectionhealth.StallThreshold = orig }()

	conn := startConnWithHealthKV(t)
	kv, err := conn.OpenKV(context.Background(), bootstrap.HealthKVBucket)
	require.NoError(t, err)
	r := health.New(kv, "clinicAppointmentsRead")
	require.NoError(t, r.SetActive(context.Background()))
	// Zero lag (drained) but a stuck ack floor with nonzero ack-pending — the
	// consumer has been handed everything and cannot finish it.
	oldFloorProgress := time.Now().Add(-time.Hour)
	require.NoError(t, r.SetProjectionProgress(context.Background(), 0, time.Time{}, time.Time{}, 7, oldFloorProgress))

	st := projectionhealth.Check(context.Background(), conn, "clinicAppointmentsRead")
	if !st.Known {
		t.Fatalf("Known = false, want true")
	}
	if !st.Stalled {
		t.Fatalf("Stalled = false for a stuck nonzero AckPending, want true")
	}
	if st.StallReason == "" {
		t.Fatalf("StallReason = %q, want non-empty", st.StallReason)
	}
}

func TestCheck_FreshAckFloorNotStalled(t *testing.T) {
	orig := projectionhealth.StallThreshold
	projectionhealth.StallThreshold = time.Hour
	defer func() { projectionhealth.StallThreshold = orig }()

	conn := startConnWithHealthKV(t)
	kv, err := conn.OpenKV(context.Background(), bootstrap.HealthKVBucket)
	require.NoError(t, err)
	r := health.New(kv, "clinicAppointmentsRead")
	require.NoError(t, r.SetActive(context.Background()))
	freshFloorProgress := time.Now()
	require.NoError(t, r.SetProjectionProgress(context.Background(), 0, time.Time{}, time.Time{}, 7, freshFloorProgress))

	st := projectionhealth.Check(context.Background(), conn, "clinicAppointmentsRead")
	if !st.Known {
		t.Fatalf("Known = false, want true")
	}
	if st.Stalled {
		t.Fatalf("Stalled = true for a fresh AckFloorProgressAt, want false")
	}
}
