//go:build leaseshortwindow

// Package leaseconvergence_test — the supersededBackgroundChecks e2e proof
// (bgcheck-supersession-convergence-rule-design.md §11.2 Inc 2's "live close"):
// the whole vertical, including the eager-freshness re-open this design builds
// on, converges to retiring the OLDER of two completed background checks on one
// applicant through the ordinary Weaver directOp convergence path — never
// through the reply op.
package leaseconvergence_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// vertexTombstoned reports whether the vertex/link at key is a live
// tombstone body — Contract #1's body-preserving retirement: `op:tombstone`
// flips only isDeleted, so class/data/sourceVertex/targetVertex all survive
// underneath it. False for an absent key too (a key that never existed is not
// a tombstone).
func (h *harness) vertexTombstoned(key string) bool {
	entry, err := h.conn.KVGet(h.ctx, bootstrap.CoreKVBucket, key)
	if err != nil || entry == nil {
		return false
	}
	var env struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if json.Unmarshal(entry.Value, &env) != nil {
		return false
	}
	return env.IsDeleted
}

// linkLive reports whether the link at key exists and is not a tombstone
// body.
func (h *harness) linkLive(key string) bool {
	entry, err := h.conn.KVGet(h.ctx, bootstrap.CoreKVBucket, key)
	if err != nil || entry == nil {
		return false
	}
	var env struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if json.Unmarshal(entry.Value, &env) != nil {
		return false
	}
	return !env.IsDeleted
}

// instanceOfLinkKeyFor returns the vtx.service.<handle>'s live instanceOf
// link key (lnk.service.<handle>.instanceOf.meta.<metaID>), scanning Core KV
// for its shape exactly as serviceOutcomes scans providedTo links. Empty if
// none is found (the link has no other stable name to guess at — the meta's
// own NanoID is minted at package install and not otherwise exposed to this
// harness).
func (h *harness) instanceOfLinkKeyFor(handle string) string {
	h.t.Helper()
	keys, err := h.conn.KVListKeys(h.ctx, bootstrap.CoreKVBucket)
	if err != nil {
		return ""
	}
	for _, k := range keys {
		type1, id1, name, targetType, _, ok := substrate.ParseLinkKey(k)
		if !ok || type1 != "service" || id1 != handle || name != "instanceOf" || targetType != "meta" {
			continue
		}
		return k
	}
	return ""
}

// supersededEvent is one committed lease.serviceInstanceSuperseded event's
// payload (scripts.go's tombstone arm mints exactly one per successful
// commit — a rejected submission, e.g. UnknownInstance on a stale row, never
// reaches this event).
type supersededEvent struct {
	InstanceKey  string `json:"instanceKey"`
	SupersededBy string `json:"supersededBy"`
	SubjectKey   string `json:"subjectKey"`
}

// liveTargetRow is weaverTargetRow plus the retraction reading this target
// needs: nil for an absent key AND for a retracted one.
//
// A retraction here leaves a BODY under the same key — {isDeleted:true,
// projectedAt, projectionSeq} — and it is not the adapter's delete mode that
// decides so. supersededBackgroundChecks declares EmptyBehavior "delete", which
// makes its compiled plan RequiresGuard (projection/plan.go,
// empty.go's RequiresGuardedTombstone), so the lens installs with the monotonic
// projection-write guard on, and a guarded delete is ALWAYS a soft tombstone
// carrying the watermark "regardless of the lens's deleteMode"
// (adapter/natskv.go's deleteRow) — the high-water mark has to survive physical
// absence or a lower-seq replay could resurrect the row. adapter.DeleteModeHard,
// which this harness passes at construction (harness_test.go), is overridden for
// exactly these lenses. The shared dev stack shows the same bodies (design §7.2).
func (h *harness) liveTargetRow(targetID, entityID string) map[string]any {
	h.t.Helper()
	row := h.weaverTargetRow(targetID, entityID)
	if row == nil || rowBool(row, "isDeleted") {
		return nil
	}
	return row
}

// supersededEventsEventually waits for at least one committed
// lease.serviceInstanceSuperseded event and returns every one committed by then.
// What the wait rules out is an un-caught-up stream: supersededEvents drains an
// ordered consumer until it runs dry, so calling it too early returns an empty
// slice that an exactly-once assertion would read as "zero committed" — the same
// verdict a broken dispatch produces. It is NOT what bounds a storm; the
// assertSteadyState window the caller holds before counting is, since a second
// dispatch inside it would have to commit under the same watch.
func (h *harness) supersededEventsEventually(deadline time.Duration) []supersededEvent {
	h.t.Helper()
	var events []supersededEvent
	require.Eventuallyf(h.t, func() bool {
		events = h.supersededEvents()
		return len(events) >= 1
	}, deadline, 200*time.Millisecond,
		"at least one lease.serviceInstanceSuperseded event must commit before its count can mean anything")
	return events
}

// supersededEvents reads every committed lease.serviceInstanceSuperseded
// event off core-events, in commit order. Reading the durable EVENT stream
// (rather than counting Weaver's fire-and-forget ops.system submissions, the
// way startMarkExpiredCounter counts MarkExpired) is what makes this an
// EXACTLY-ONCE-COMMITTED witness: a submission Weaver retries after a
// RevisionConflict, or one the Processor rejects UnknownInstance against a
// stale row (design §5 row 10), never mints this event, so a retried dispatch
// cannot inflate the count the way counting ops.system publishes could.
func (h *harness) supersededEvents() []supersededEvent {
	h.t.Helper()
	cons, err := h.conn.JetStream().OrderedConsumer(h.ctx, bootstrap.CoreEventsStreamName,
		jetstream.OrderedConsumerConfig{
			FilterSubjects: []string{"events.lease.serviceInstanceSuperseded"},
			DeliverPolicy:  jetstream.DeliverAllPolicy,
		})
	if err != nil {
		return nil
	}
	var out []supersededEvent
	for {
		msg, err := cons.Next(jetstream.FetchMaxWait(300 * time.Millisecond))
		if err != nil {
			return out
		}
		var env struct {
			Payload supersededEvent `json:"payload"`
		}
		if json.Unmarshal(msg.Data(), &env) == nil {
			out = append(out, env.Payload)
		}
	}
}

// TestLeaseConvergence_Supersession_RetiresTheOlderCheck is the design's
// state-table row 3 driven end-to-end: a completed background check (A) gets
// a strictly-later completed sibling (B) on the SAME applicant — minted by
// this package's own eager-freshness re-open, never a harness fabrication —
// and Weaver's ordinary directOp convergence retires A through the shipped
// TombstoneSupersededLeaseServiceInstance, never through the reply op.
//
// backgroundCheckFreshness is not optional (it is what re-opens missing_bgcheck
// and mints B in the first place, exactly as
// TestLeaseConvergence_BgcheckFreshness_EagerReopen proves on its own);
// supersededBackgroundChecks is the lens this design adds.
func TestLeaseConvergence_Supersession_RetiresTheOlderCheck(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	if !shortFreshnessWindow {
		t.Skip("supersession leg requires -tags leaseshortwindow (a window short enough to watch a lapse)")
	}
	h := newHarness(t, withExtraLenses("backgroundCheckFreshness", "supersededBackgroundChecks"))
	appKey, appID, applicantKey := h.seedApplicant()
	applicantID := applicantKey[len("vtx.identity."):]

	h.driveApplicantSteps(appKey, applicantKey)
	h.decideLandlord(appKey, "approved")
	h.drainUntilConverged(appID, 30*time.Second)

	// Exactly one completed check (A) after the initial converge — the anchor
	// this design's row 2 says projects no row on its own.
	before := h.bgcheckHandles(applicantID)
	require.Len(t, before, 1, "exactly one bgcheck instance after the initial converge")
	aHandle := before[0]
	aKey := "vtx.service." + aHandle

	// No standing row at install/before any lapse (design §7.1's Phase-0
	// expectation, re-asserted per-test): the lens is violating-rows-only.
	require.Nil(t, h.weaverTargetRow("supersededBackgroundChecks", aHandle),
		"the current, not-yet-superseded check projects no row (violating-rows-only, §4.1)")

	// One eager-freshness cycle: A's window lapses, MarkExpired records it,
	// missing_bgcheck re-opens, Weaver re-dispatches, and a SECOND completed
	// check (B) lands on the same applicant — the fact this design's lens
	// projects a row from, never fabricated by this test.
	h.assertEagerReopenCycle(appID, applicantID, 1)

	after := h.bgcheckHandles(applicantID)
	require.Len(t, after, 2, "A (now lapsed) and B (freshly completed) both providedTo the applicant")
	bHandle := ""
	for _, handle := range after {
		if handle != aHandle {
			bHandle = handle
		}
	}
	require.NotEmpty(t, bHandle, "a second, distinct bgcheck instance must exist after the eager re-open")
	bKey := "vtx.service." + bHandle

	// The instanceOf link key is deterministic (Contract #1) but names a meta
	// NanoID minted at package install, so it is read off Core KV once rather
	// than guessed. In CORE KV a tombstone is a body — op:tombstone flips
	// isDeleted and carries class/endpoints over — so the key stays discoverable
	// by shape whether supersession has already run by the time this reads or
	// not; assertEagerReopenCycle's own re-converge wait leaves no guarantee it
	// has NOT already run.
	instanceOfLinkKey := h.instanceOfLinkKeyFor(aHandle)
	require.NotEmpty(t, instanceOfLinkKey, "A's own instanceOf link must be discoverable")

	supersedesLinkKey := "lnk.service." + bHandle + ".supersedes.service." + aHandle

	// What this test proves is the DISPATCH, not the row: on the likely
	// interleaving the retirement rides the same delivery as the re-converge, so
	// A's row is already gone by the time a test can look, and any assertion
	// shaped "the row is there OR it already did its job" is vacuous. The row's
	// columns are pinned deterministically where the level is fixed — the lens's
	// own rule-engine test over the real cypher (packages/lease-signing's
	// superseded_bgchecks_lens_test.go) — and what only a live stack can show is
	// that Weaver turns that row into this committed op.
	//
	// A's root tombstones, its own instanceOf link tombstones, and the
	// successor mints the supersedes link — all in the SAME batch (§4.3 d) —
	// waited for together.
	require.Eventuallyf(t, func() bool {
		return h.vertexTombstoned(aKey) && !h.linkLive(instanceOfLinkKey) && h.linkLive(supersedesLinkKey)
	}, 60*time.Second, 200*time.Millisecond,
		"A's root and its instanceOf link must tombstone and lnk.service.%s.supersedes.service.%s must go live in the same batch", bHandle, aHandle)

	// A's supersededBackgroundChecks row retracts — as a watermark-carrying
	// tombstone body under the same key, for the reason liveTargetRow states.
	require.Eventuallyf(t, func() bool {
		return h.liveTargetRow("supersededBackgroundChecks", aHandle) == nil
	}, 30*time.Second, 200*time.Millisecond, "A's supersededBackgroundChecks row must retract")

	// A's backgroundCheckFreshness row retracts too (its own anchor tombstoned).
	require.Eventuallyf(t, func() bool {
		return h.liveTargetRow("backgroundCheckFreshness", aHandle) == nil
	}, 30*time.Second, 200*time.Millisecond, "A's backgroundCheckFreshness row must retract once A tombstones")

	// B's root stays live and its OWN backgroundCheckFreshness row stands
	// (nothing here ever touches the live successor's root or freshness row).
	require.False(t, h.vertexTombstoned(bKey), "B must stay live -- nothing here is later than B")
	require.NotNil(t, h.liveTargetRow("backgroundCheckFreshness", bHandle), "B's own freshness row must stand")

	// The application stays converged throughout and after.
	h.assertSteadyState(appID, 3*time.Second)

	// What the retirement is FOR, read off the row Weaver reads: the application
	// still counts a fresh completed check after A is gone, so its bgcheck gap
	// stays shut. The design states this as freshBgComplete == 1; that scalar is
	// an internal WITH value leaseApplicationComplete's output descriptor does
	// not project (lenses.go's BodyColumns), so missing_bgcheck — computed from
	// it, and the column Weaver actually dispatches off — is the reachable form.
	appRow := h.liveTargetRow("leaseApplicationComplete", appID)
	require.NotNil(t, appRow, "the application's own row must stand after the retirement")
	require.Falsef(t, rowBool(appRow, "missing_bgcheck"),
		"retiring A must leave the application counting B as its fresh completed check; row=%v", appRow)

	// Exactly ONE TombstoneSupersededLeaseServiceInstance ever COMMITTED,
	// naming this exact pair — never a storm, never a rejected dispatch that
	// also happened to fire. The steady-state window just held is what makes
	// "one" mean no storm; the wait inside the witness only rules out a stream
	// this test read before it caught up.
	events := h.supersededEventsEventually(30 * time.Second)
	require.Lenf(t, events, 1, "exactly one lease.serviceInstanceSuperseded event must have committed; got %+v", events)
	require.Equal(t, aKey, events[0].InstanceKey)
	require.Equal(t, bKey, events[0].SupersededBy)
	require.Equal(t, applicantKey, events[0].SubjectKey)
}
