package leasesigning

// Rule-engine proof of the backgroundCheckFreshness convergence cypher — the
// service-instance-anchored lens whose only job is to give a background check's
// freshness window a timer on the entity the window belongs to.
//
// The row it projects is read by Weaver's temporal lane, not by a gap
// dispatcher: freshUntil arms the @at, the fired timer's MarkExpired records the
// lapse on the instance, and the four readers of bgcheck freshness in this
// package (readinessWithItems' freshBgComplete, spliced into three lenses, and
// renewalComplete's bgcheckValidUntil) read that recorded fact. So the vectors
// that matter here are the ones that decide whether a timer arms at all.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// projectBgFreshness runs backgroundCheckFreshnessSpec anchored on the named
// service instance. No $now is supplied at all: the cypher references none, and
// passing one would let a clock-reading regression pass unnoticed.
func (f *lensFixture) projectBgFreshness(t *testing.T, instName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(backgroundCheckFreshnessSpec)
	require.NoError(t, err, "backgroundCheckFreshness cypher must parse on the full engine")
	instKey := "vtx.service." + f.ids[instName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": instKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// completedBgcheck seeds one completed background-check instance under the given
// logical name, with the given validUntil.
func completedBgcheck(t *testing.T, f *lensFixture, name, validUntil string) {
	t.Helper()
	f.vtxWithClass(t, name, "service", "service.backgroundCheck.instance")
	f.aspect(t, name, "outcome", "outcome", map[string]any{
		"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": validUntil,
	})
}

// TestBackgroundCheckFreshness_ProjectsDeadlineWhenNoLapseRecorded is the arming
// vector: a completed check no timer has fired on projects its own validUntil as
// freshUntil, which is what Weaver schedules the @at from. Without a scalar here
// there is nothing to arm and the whole family falls back to an incidental CDC
// touch.
func TestBackgroundCheckFreshness_ProjectsDeadlineWhenNoLapseRecorded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const validUntil = "2026-06-18T00:05:00Z"
	completedBgcheck(t, f, "bg1", validUntil)

	rows := f.projectBgFreshness(t, "bg1")
	require.Len(t, rows, 1, "exactly one row per instance anchor")
	v := rows[0].Values
	require.Equal(t, validUntil, v["freshUntil"], "an unlapsed check projects its own deadline")
	_, isString := v["freshUntil"].(string)
	require.True(t, isString, "freshUntil must be a scalar string so scheduleFreshness can parse it as RFC3339")
	require.Equal(t, "vtx.service."+f.ids["bg1"], v["entityKey"], "the row names the instance the marker will land on")
	require.Equal(t, false, v["violating"], "a declared false, not an omitted column — Weaver reads it off the row body")
}

// TestBackgroundCheckFreshness_PastDeadlineProjectedVerbatim is the DEPLOY-WINDOW
// input, and the one place a "null when the deadline is already past" guard would
// be tempting. A check whose window lapsed while no target was watching carries no
// marker, so it reads FRESH to every consumer; the only thing that closes that
// window is this row projecting the past instant, Weaver publishing the overdue
// @at, and NATS releasing it immediately. Nulling a past deadline here arms
// nothing and the lapse is never recorded at all. The end-to-end recovery is
// driven by internal/leaseconvergence's
// TestLeaseConvergence_BgcheckFreshness_LapsedBeforeTheTargetExisted.
func TestBackgroundCheckFreshness_PastDeadlineProjectedVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const longPast = "2020-06-01T00:00:00Z"
	completedBgcheck(t, f, "bg1", longPast)

	v := f.projectBgFreshness(t, "bg1")[0].Values
	require.Equal(t, longPast, v["freshUntil"],
		"an already-past deadline with no recorded lapse projects VERBATIM — the overdue @at is the only path to recording it")
}

// TestBackgroundCheckFreshness_NullOnceLapseRecorded is the disarm vector: once
// the timer has fired and MarkExpired has recorded the instant under this
// target's key, freshUntil goes null. That null is load-bearing — a past deadline
// projected verbatim re-arms an overdue @at that fires immediately on every
// delivery, and there is nothing left to wait for once the lapse is recorded.
func TestBackgroundCheckFreshness_NullOnceLapseRecorded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const validUntil = "2026-06-18T00:05:00Z"
	completedBgcheck(t, f, "bg1", validUntil)
	recordBgcheckLapse(t, f, "bg1", validUntil)

	v := f.projectBgFreshness(t, "bg1")[0].Values
	require.Nil(t, v["freshUntil"], "a recorded lapse at the deadline clears the timer")
}

// TestBackgroundCheckFreshness_MarkerBehindDeadlineStillArms is the re-arm
// vector: the marker is permanent and nothing clears it, so a check re-issued
// with a later deadline must arm again off the stored comparison alone, with no
// clearing write. Testing the marker's PRESENCE instead would leave the instance
// permanently disarmed.
func TestBackgroundCheckFreshness_MarkerBehindDeadlineStillArms(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const validUntil = "2026-07-01T00:00:00Z"
	completedBgcheck(t, f, "bg1", validUntil)
	recordBgcheckLapse(t, f, "bg1", "2026-06-18T00:00:00Z")

	v := f.projectBgFreshness(t, "bg1")[0].Values
	require.Equal(t, validUntil, v["freshUntil"], "a lapse the current deadline has outrun does not disarm it")
}

// TestBackgroundCheckFreshness_BoundaryMarkerEqualsDeadline pins which side of
// the `>=` the equal instant falls on: the timer fires AT the deadline and
// records that instant, so equality is the ordinary lapse, not an edge case that
// leaves the row armed forever.
func TestBackgroundCheckFreshness_BoundaryMarkerEqualsDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const validUntil = "2026-06-18T00:05:00Z"
	completedBgcheck(t, f, "bg1", validUntil)
	recordBgcheckLapse(t, f, "bg1", validUntil)

	v := f.projectBgFreshness(t, "bg1")[0].Values
	require.Nil(t, v["freshUntil"], "marker == deadline is a lapse (>= boundary)")
}

// TestBackgroundCheckFreshness_SiblingTargetLapseDoesNotDisarm is the isolation
// vector. One entity can carry several targets' fires in one marker aspect,
// keyed per target; reading the aspect's presence, or its entity-wide expiredAt
// maximum, would let another target's timer disarm this one.
func TestBackgroundCheckFreshness_SiblingTargetLapseDoesNotDisarm(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const validUntil = "2026-06-18T00:05:00Z"
	completedBgcheck(t, f, "bg1", validUntil)
	recordSiblingLapse(t, f, "bg1", "leaseExpiry", "2099-01-01T00:00:00Z")

	v := f.projectBgFreshness(t, "bg1")[0].Values
	require.Equal(t, validUntil, v["freshUntil"], "another target's recorded fire is not this target's lapse")
}

// TestBackgroundCheckFreshness_NoRowForOtherFamilies: payment and docGen
// instances key under the same `service` type, so the anchor enumeration reaches
// them; the class filter is what keeps them from projecting a row (and, with
// EmptyBehavior delete, from leaving one behind).
func TestBackgroundCheckFreshness_NoRowForOtherFamilies(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z", "validUntil": "2026-07-01T00:00:00Z"})

	require.Empty(t, f.projectBgFreshness(t, "pay1"), "a payment instance carries no bgcheck freshness window")
}

// TestBackgroundCheckFreshness_NoRowBeforeTheOutcomeLands: a dispatched check
// with no outcome yet has no deadline to arm at, so it projects nothing rather
// than a row with a null freshUntil.
func TestBackgroundCheckFreshness_NoRowBeforeTheOutcomeLands(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg1", "dispatch", "dispatch", map[string]any{"vendorRef": "vendor-ref-1", "adapter": "backgroundCheck"})

	require.Empty(t, f.projectBgFreshness(t, "bg1"), "no completed outcome → no window → no row")
}

// TestBackgroundCheckFreshness_NoRowForAFailedOutcome: a terminal failure closes
// nothing and starts no window; only a completed check has a freshness horizon.
func TestBackgroundCheckFreshness_NoRowForAFailedOutcome(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "failed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": "2026-07-01T00:00:00Z"})

	require.Empty(t, f.projectBgFreshness(t, "bg1"), "a failed check has no window to keep current")
}

// --- renewalComplete's bgcheckValidUntil ------------------------------------
//
// renewalComplete resolves the SAME window off the SAME instance class, but
// through an aggregate reached from a different anchor (renewal → leaseapp →
// applicant ← providedTo — service). It is the fourth reader of the recorded
// lapse and the one the shared readiness fragment does not cover, so its
// polarity is pinned separately rather than inferred from the other three.

// projectRenewalComplete runs renewalCompleteSpec anchored on the named renewal.
// No $now is supplied: the cypher references none, and passing one would let a
// clock-reading regression pass unnoticed.
func (f *lensFixture) projectRenewalComplete(t *testing.T, renewalName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(renewalCompleteSpec)
	require.NoError(t, err, "renewalComplete cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": "vtx.renewal." + f.ids[renewalName],
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// seedRenewalWithBgcheck seeds an open renewal cycle whose tenant holds one
// completed background check with the given deadline.
func seedRenewalWithBgcheck(t *testing.T, f *lensFixture, validUntil string) {
	t.Helper()
	f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")
	completedBgcheck(t, f, "bg1", validUntil)
	f.edge(t, "providedTo", "bg1", "tina")
}

// TestRenewalComplete_BgcheckValidUntil_UnlapsedCounts is the positive vector:
// with no recorded lapse the aggregate carries the deadline, which is what the
// planner's goal atom reads as "the tenant holds a current check". The deadline
// is in the PAST relative to any wall clock the suite runs at, so a clock-reading
// form would null this column — which is what makes the vector discriminating.
func TestRenewalComplete_BgcheckValidUntil_UnlapsedCounts(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const validUntil = "2020-06-01T00:00:00Z"
	seedRenewalWithBgcheck(t, f, validUntil)

	rows := f.projectRenewalComplete(t, "rn")
	require.Len(t, rows, 1, "exactly one row per renewal anchor")
	require.Equal(t, validUntil, rows[0].Values["bgcheckValidUntil"],
		"no recorded lapse -> the check is current, whatever the wall clock says")
}

// TestRenewalComplete_BgcheckValidUntil_LapsedIsNull is the negative vector, and
// the deadline is in the FAR FUTURE so only the recorded lapse can null the
// column. A null bgcheckValidUntil is what leaves the goal's `present` atom unmet
// and puts refreshBgcheck on the plan.
func TestRenewalComplete_BgcheckValidUntil_LapsedIsNull(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedRenewalWithBgcheck(t, f, farFutureValidUntil)
	recordBgcheckLapse(t, f, "bg1", farFutureValidUntil)

	v := f.projectRenewalComplete(t, "rn")[0].Values
	require.Nil(t, v["bgcheckValidUntil"], "a recorded lapse at the deadline drops the window")
	require.Equal(t, true, v["missing_renewalComplete"], "no current check -> the goal is unmet and the row violates")
}

// TestRenewalComplete_BgcheckValidUntil_ReArmed: the marker is permanent, so a
// re-checked tenant whose new deadline outruns the recorded instant must read
// current again with no clearing write.
func TestRenewalComplete_BgcheckValidUntil_ReArmed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedRenewalWithBgcheck(t, f, farFutureValidUntil)
	recordBgcheckLapse(t, f, "bg1", "2026-06-18T00:00:00Z")

	v := f.projectRenewalComplete(t, "rn")[0].Values
	require.Equal(t, farFutureValidUntil, v["bgcheckValidUntil"],
		"a lapse the current deadline has outrun is not a lapse of THIS window")
}

// TestRenewalComplete_BgcheckValidUntil_SiblingTargetLapse: the isolation vector
// on this reader too — another target's recorded fire on the same instance says
// nothing about the background check's window.
func TestRenewalComplete_BgcheckValidUntil_SiblingTargetLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedRenewalWithBgcheck(t, f, farFutureValidUntil)
	recordSiblingLapse(t, f, "bg1", "leaseExpiry", "2100-01-01T00:00:00Z")

	v := f.projectRenewalComplete(t, "rn")[0].Values
	require.Equal(t, farFutureValidUntil, v["bgcheckValidUntil"],
		"another target's recorded fire is not this target's lapse")
}

// TestRenewalComplete_ArmsNoTimerOfItsOwn pins that this lens projects no
// freshUntil, in the cypher AND in the descriptor Weaver actually reads the row
// through. The window it reports on belongs to a background-check instance
// reached across providedTo, and that instance carries its own target: a timer
// here could only mark the RENEWAL, which is neither where the deadline lapses
// nor where any reader looks. Both halves are asserted because either alone is
// insufficient — a column absent from the descriptor never reaches Weaver even
// if the cypher returns it, and a column declared with no cypher term reaches it
// as a null.
func TestRenewalComplete_ArmsNoTimerOfItsOwn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	var cols []string
	for _, l := range Lenses() {
		if l.CanonicalName == "renewalComplete" {
			cols = l.Output.BodyColumns
		}
	}
	require.NotEmpty(t, cols, "renewalComplete must be declared")
	require.NotContains(t, cols, "freshUntil",
		"renewalComplete declares no freshUntil — Weaver's temporal lane must find no deadline to arm on the renewal")

	f := newLensFixture(t)
	seedRenewalWithBgcheck(t, f, farFutureValidUntil)

	rows := f.projectRenewalComplete(t, "rn")
	require.Len(t, rows, 1)
	require.NotContains(t, rows[0].Values, "freshUntil",
		"the cypher returns no freshUntil either, so the column cannot reappear through a descriptor edit alone")
	require.Equal(t, farFutureValidUntil, rows[0].Values["bgcheckValidUntil"],
		"the window is still REPORTED here — only the timer moved to the instance that owns it")
}

// TestRenewalComplete_BgcheckValidUntil_TakesTheLatestUnlapsed: the aggregate is
// a max over the tenant's checks, so a lapsed one sitting beside a current one
// must not decide the column — the re-dispatch shape the live loop produces.
func TestRenewalComplete_BgcheckValidUntil_TakesTheLatestUnlapsed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const lapsedDeadline = "2099-01-01T00:00:00Z"
	const currentDeadline = "2100-01-01T00:00:00Z"
	seedRenewalWithBgcheck(t, f, lapsedDeadline)
	recordBgcheckLapse(t, f, "bg1", lapsedDeadline)
	completedBgcheck(t, f, "bg2", currentDeadline)
	f.edge(t, "providedTo", "bg2", "tina")

	rows := f.projectRenewalComplete(t, "rn")
	require.Len(t, rows, 1, "still one row per anchor across the two-instance fan-out")
	require.Equal(t, currentDeadline, rows[0].Values["bgcheckValidUntil"],
		"the max folds over UNLAPSED checks only, so the lapsed one never decides the column")
}

// inflightBgcheck seeds a background-check instance the adapter has accepted —
// a vendorRef recorded, no outcome yet — which is the presence-based "in flight"
// this package's in-flight columns all count.
func inflightBgcheck(t *testing.T, f *lensFixture, name string) {
	t.Helper()
	f.vtxWithClass(t, name, "service", "service.backgroundCheck.instance")
	f.aspect(t, name, "dispatch", "dispatch", map[string]any{"vendorRef": "vendor-ref-1", "adapter": "backgroundCheck"})
}

// TestRenewalComplete_InflightIsLegScoped pins the shape of
// inflight_renewalComplete, which is NOT the bare in-flight fact the static
// target's inflight_bgcheck is. renewalComplete is a goal target over a mixed
// catalog — refreshBgcheck dispatches externally, verifyGuarantor / setTerms /
// signRenewal are human tasks — and Weaver answers its suppression gate on this
// column before it binds a leg. So the column is the in-flight fan conjoined
// with the external leg's unmet effect (bgcheckValidUntil = null): true only
// while the check is what the chain is still waiting on.
//
// Every vector asserts a REAL Go false rather than require.False, because
// Weaver's boolColumn reads the value off the row body and an omitted or null
// column is a different fact from a declared false — the same null-folding
// lesson renewalCompleteSpec's hasGuarantor term carries.
func TestRenewalComplete_InflightIsLegScoped(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	// (i) A check in flight with no current window is the state the column
	// exists for: refreshBgcheck is the leg the chain is waiting on, and a
	// second call against the same vendor must be suppressed.
	t.Run("in flight with no current check", func(t *testing.T) {
		f := newLensFixture(t)
		f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")
		inflightBgcheck(t, f, "bg1")
		f.edge(t, "providedTo", "bg1", "tina")

		v := f.projectRenewalComplete(t, "rn")[0].Values
		require.Nil(t, v["bgcheckValidUntil"], "an undelivered check opens no window")
		require.Equal(t, true, v["inflight_renewalComplete"],
			"the external leg is the one the chain is waiting on -> suppress a second dispatch")
	})

	// (ii) The vector that pins the SCOPING. With a current window the fresh
	// leg is a human one (setTerms has no pre and the lowest cost), and the
	// suppression gate reads this column before any leg is bound — so a bare
	// (bgInflight > 0) here would park the landlord's and the tenant's tasks
	// behind a stray check running for the same tenant, one the static
	// leaseApplicationComplete target may well have dispatched.
	t.Run("in flight beside a current check", func(t *testing.T) {
		f := newLensFixture(t)
		f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")
		completedBgcheck(t, f, "bg1", farFutureValidUntil)
		f.edge(t, "providedTo", "bg1", "tina")
		inflightBgcheck(t, f, "bg2")
		f.edge(t, "providedTo", "bg2", "tina")

		v := f.projectRenewalComplete(t, "rn")[0].Values
		require.Equal(t, farFutureValidUntil, v["bgcheckValidUntil"], "the completed check holds the window open")
		require.Equal(t, false, v["inflight_renewalComplete"],
			"the external leg's effect is already met, so a check still in flight must not suppress the HUMAN legs")
	})

	// (iii) The payoff state. A check that concluded WITHOUT success is not in
	// flight, so the reclaim is free to mint a fresh claimId and call the vendor
	// again, bounded by maxretries_renewalComplete.
	t.Run("concluded without success", func(t *testing.T) {
		f := newLensFixture(t)
		f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")
		f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
		f.aspect(t, "bg1", "dispatch", "dispatch", map[string]any{"vendorRef": "vendor-ref-1", "adapter": "backgroundCheck"})
		f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "failed", "completedAt": "2026-06-01T00:00:00Z"})
		f.edge(t, "providedTo", "bg1", "tina")

		v := f.projectRenewalComplete(t, "rn")[0].Values
		require.Nil(t, v["bgcheckValidUntil"], "a failed check opens no window")
		require.Equal(t, false, v["inflight_renewalComplete"],
			"a concluded-without-success check is not in flight -> the leg may be retried")
	})

	// (iv) The real-false vector. With no instance at all the count folds over
	// an empty set, and what it folds TO matters: Refractor writes every
	// declared BodyColumn into the envelope, nil included, and Weaver reads an
	// absent-or-nil column as a readable false. staleMark answers !false, so a
	// column that ever went null would hand the external leg an unconditional
	// fresh-claimId reclaim with no in-flight protection at all — the failure
	// direction is fail-OPEN, not fail-closed.
	t.Run("no instance at all", func(t *testing.T) {
		f := newLensFixture(t)
		f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")

		rows := f.projectRenewalComplete(t, "rn")
		require.Len(t, rows, 1)
		require.Equal(t, false, rows[0].Values["inflight_renewalComplete"],
			"a declared false, not an omitted column — the empty count must fold to a real boolean")
	})

	// (v) The vendorRef term. An instance the adapter never accepted carries no
	// .dispatch, and §10.3's in-flight fact is presence-based on exactly that
	// marker — so this is a check that is NOT outstanding and the leg may be
	// retried. Without this vector the term is inert: every other vector either
	// seeds .dispatch or seeds no instance at all.
	t.Run("instance the adapter never accepted", func(t *testing.T) {
		f := newLensFixture(t)
		f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")
		f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
		f.edge(t, "providedTo", "bg1", "tina")

		v := f.projectRenewalComplete(t, "rn")[0].Values
		require.Equal(t, false, v["inflight_renewalComplete"],
			"no .dispatch marker means the vendor never took the call — nothing is in flight to wait on")
	})

	// (vi) The class term. The providedTo fan carries EVERY service instance a
	// tenant has, not just background checks — leaseApplicationComplete counts
	// payInflight over this identical binding — so without the class predicate a
	// payment in flight would suppress the renewal's whole gap, human legs
	// included, for a fact that has nothing to do with this chain.
	t.Run("a payment in flight is not this gap's remediation", func(t *testing.T) {
		f := newLensFixture(t)
		f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")
		f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
		f.aspect(t, "pay1", "dispatch", "dispatch", map[string]any{"vendorRef": "vendor-ref-2", "adapter": "collectPayment"})
		f.edge(t, "providedTo", "pay1", "tina")

		v := f.projectRenewalComplete(t, "rn")[0].Values
		require.Equal(t, false, v["inflight_renewalComplete"],
			"a payment on the same tenant is not the background check this gap's external leg is waiting on")
	})
}

// TestRenewalComplete_DeclaresTheInflightCompanion pins the DESCRIPTOR half:
// the cypher's column only reaches Weaver if the output descriptor names it,
// and staleMark's first guard reads exactly that declaration — an undeclared
// inflight_<g> makes the predicate answer false before it ever classifies the
// gap's dispatch. Its maxretries_<g> partner is asserted beside it because
// §10.3's pair is what a bounded external retry needs; either alone is inert.
func TestRenewalComplete_DeclaresTheInflightCompanion(t *testing.T) {
	var cols []string
	for _, l := range Lenses() {
		if l.CanonicalName == "renewalComplete" {
			cols = l.Output.BodyColumns
		}
	}
	require.NotEmpty(t, cols, "renewalComplete must be declared")
	require.Contains(t, cols, "inflight_renewalComplete",
		"the suppression/stale companion must be declared, not merely returned by the cypher")
	require.Contains(t, cols, "maxretries_renewalComplete",
		"the §10.3 companion pair — the in-flight fact plus the retry cap that bounds the reclaim")
}

// --- the derived-anchor payoff, statically -----------------------------------

// TestPostgresReadModels_DerivationRefusalReasons pins WHY each of the two
// Postgres read models is or is not eligible for Refractor's per-anchor derived
// evaluation, because the two answers are different and only one of them moved.
//
// The plain pipeline refuses a lens that RETURNS $now or $projectedAt: a
// per-anchor evaluation reproduces the clock differently from the whole-corpus
// rescan it replaces, so the rescan stays. leaseApplicationsRead's only clock
// reference was the bgcheck freshness term inside readinessWithItems; with that
// term reading a recorded fact the lens references no clock parameter at all,
// which is the condition the refusal tests.
//
// landlordLeaseApplicationsRead splices the SAME fragment and so also loses its
// clock reference — and stays refused anyway, one conjunct earlier and in the
// INDEX rather than the licence: it declares DiffRetraction, and a per-anchor
// seeded row set reads to applyDiffRetraction as "every OTHER anchor's rows are
// gone". Its three encrypted contact columns are not what refuses it — a Secure
// Lens is licensed on the same conjuncts as any other plain lens
// (secure-plain-lens-retraction-and-audit-design.md §4.2), because the
// re-entrant evaluation never decrypts. Asserting both lenses is what keeps a
// narrowing that should not have happened from passing unnoticed.
func TestPostgresReadModels_DerivationRefusalReasons(t *testing.T) {
	eng := full.New()
	compiled := func(spec string) *full.CompiledRule {
		t.Helper()
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		fullCR, ok := cr.(*full.CompiledRule)
		require.True(t, ok, "the read models compile on the full engine")
		return fullCR
	}

	for _, name := range []string{"leaseApplicationsRead", "landlordLeaseApplicationsRead"} {
		var spec string
		var diffRetraction bool
		for _, l := range Lenses() {
			if l.CanonicalName == name {
				spec = l.Spec
				diffRetraction = l.DiffRetraction
			}
		}
		require.NotEmptyf(t, spec, "%s must be declared", name)
		cr := compiled(spec)
		for _, param := range []string{"now", "projectedAt"} {
			referenced, exhaustive := cr.ReferencesParam(param)
			require.Truef(t, exhaustive, "%s: the query shape must be provably free of $%s", name, param)
			require.Falsef(t, referenced, "%s must reference no $%s — freshness is a recorded fact, not a clock reading", name, param)
		}
		if name == "landlordLeaseApplicationsRead" {
			require.Truef(t, diffRetraction, "%s stays refused on the DiffRetraction conjunct, which the index asks before the licence is reached", name)
		} else {
			require.Falsef(t, diffRetraction, "%s declares no target diff, so the clock conjunct is the one that decided its verdict", name)
		}
	}
}
