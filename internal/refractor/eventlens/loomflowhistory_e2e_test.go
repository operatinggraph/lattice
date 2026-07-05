// TestManager_LoomFlowHistoryLens_E2E drives the eventlens.Manager against
// the REAL orchestration-base loomFlowHistory LensSpec (Chronicler Fire 2,
// the first production consumer of the Fire 1 eventStream primitive) over a
// real JetStream core-events stream — proving the shipped Source/Project the
// package declares (not a parallel test fixture) converges correctly
// end-to-end (orchestration-history-read-model-design.md §5).
//
// pkgmgr.SourceConfig/EventProjection/ColumnMapping and their
// internal/refractor/lens counterparts are deliberately independent Go
// types (pkgmgr must not import internal/refractor/lens — see
// pkgmgr.LensSpec.Source's doc comment), so this test round-trips the
// package's Source through JSON exactly as the real install→load path does:
// pkgmgr.lensSpecBody marshals it into the aspect data an installed lens is
// stored as; Refractor's CoreKVSource unmarshals that JSON into
// lens.SourceConfig. This is the seam ColumnMapping's two independent
// (Un)MarshalJSON implementations must agree across.
package eventlens

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/substrate"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
)

func TestManager_LoomFlowHistoryLens_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loomFlowHistory e2e test in -short mode")
	}

	var pkgLensSpec pkgmgr.LensSpec
	for _, l := range orchestrationbase.Lenses() {
		if l.CanonicalName == "loomFlowHistory" {
			pkgLensSpec = l
		}
	}
	require.NotNil(t, pkgLensSpec.Source, "loomFlowHistory lens must declare a Source")
	require.Len(t, pkgLensSpec.IntoKey, 1)

	// Round-trip pkgLensSpec.Source through JSON into the Refractor-side
	// lens.SourceConfig, exactly as the real install→load path does.
	sourceJSON, err := json.Marshal(pkgLensSpec.Source)
	require.NoError(t, err)
	var lensSource lens.SourceConfig
	require.NoError(t, json.Unmarshal(sourceJSON, &lensSource))
	require.Equal(t, "payload.instanceId", lensSource.Project.Columns["instance_id"].Path,
		"bare-path columns must survive the pkgmgr -> JSON -> lens round-trip")

	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t), NoLog: true, NoSigs: true}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	js := conn.JetStream()
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     bootstrap.CoreEventsStreamName,
		Subjects: []string{bootstrap.EventsWildcardSubject},
	})
	require.NoError(t, err)

	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: pkgLensSpec.Bucket})
	require.NoError(t, err)
	kv, err := conn.OpenKV(ctx, pkgLensSpec.Bucket)
	require.NoError(t, err)

	nkv, err := adapter.New(kv, pkgLensSpec.IntoKey, adapter.DeleteModeHard)
	require.NoError(t, err)
	nkv.SetGuarded(true)

	mgr, err := New(Config{
		Conn:         conn,
		EventsStream: bootstrap.CoreEventsStreamName,
		Subject:      lensSource.Subjects[0],
		Durable:      "refractor-loomflowhistory-e2e",
		KeyField:     pkgLensSpec.IntoKey[0],
		Project:      lensSource.Project,
		Adapter:      nkv,
	})
	require.NoError(t, err)

	mgrCtx, mgrCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() { defer close(doneCh); _ = mgr.Run(mgrCtx) }()
	t.Cleanup(func() { mgrCancel(); <-doneCh })

	publish := func(eventType string, payload map[string]any) {
		body, jerr := json.Marshal(Event{EventType: eventType, Domain: "loom", Payload: payload, Timestamp: "2026-07-05T10:00:00Z"})
		require.NoError(t, jerr)
		// Contract #3 / Architecture Decision #2: the outbox publishes to
		// subject "events." + the event's class (eventType), e.g.
		// "events.loom.patternStarted" — matching the lens's
		// "events.loom.>" filter.
		_, perr := js.Publish(ctx, "events."+eventType, body)
		require.NoError(t, perr)
	}

	// Flow 1: starts, then completes.
	publish("loom.patternStarted", map[string]any{
		"instanceId": "inst-e2e-1", "patternRef": "onboarding-v1", "subjectKey": "identity.applicant-1",
	})
	publish("loom.patternCompleted", map[string]any{"instanceId": "inst-e2e-1"})

	require.Eventually(t, func() bool {
		row, ok, gErr := nkv.GetRow(ctx, map[string]any{"instance_id": "inst-e2e-1"})
		return gErr == nil && ok && row["status"] == "complete"
	}, 10*time.Second, 100*time.Millisecond, "loomFlowHistory row did not converge to complete")

	row, ok, err := nkv.GetRow(ctx, map[string]any{"instance_id": "inst-e2e-1"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "onboarding-v1", row["pattern_ref"])
	require.Equal(t, "identity.applicant-1", row["subject_key"])
	require.Equal(t, "2026-07-05T10:00:00Z", row["started_at"])
	require.Equal(t, "2026-07-05T10:00:00Z", row["ended_at"])

	// Flow 2: starts, then fails — proves failure_reason + status="failed".
	publish("loom.patternStarted", map[string]any{
		"instanceId": "inst-e2e-2", "patternRef": "onboarding-v1", "subjectKey": "identity.applicant-2",
	})
	publish("loom.patternFailed", map[string]any{"instanceId": "inst-e2e-2", "reason": "vendor timeout"})

	require.Eventually(t, func() bool {
		row, ok, gErr := nkv.GetRow(ctx, map[string]any{"instance_id": "inst-e2e-2"})
		return gErr == nil && ok && row["status"] == "failed"
	}, 10*time.Second, 100*time.Millisecond, "loomFlowHistory row did not converge to failed")

	row2, ok, err := nkv.GetRow(ctx, map[string]any{"instance_id": "inst-e2e-2"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "vendor timeout", row2["failure_reason"])
}
