package projectionhealth_test

import (
	"context"
	"testing"

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
