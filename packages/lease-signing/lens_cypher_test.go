package leasesigning

// Rule-engine proof of the leaseApplicationComplete convergence cypher.
//
// These tests drive the lens spec (leaseApplicationCompleteSpec) through the
// `full` rule engine directly — the same engine selected at activation via
// engine:"full" — against an embedded NATS Core/Adjacency KV. They prove the
// cypher is ONE ROW PER ANCHOR (the §0.C guard would fail closed otherwise) and
// that the gap columns are strict bools that flip on a direct .outcome /
// .signature / .ssn write (AC #1 reprojection-on-linked-constituent + AC #4
// direct-write; the live bridge round-trip is 14.5).
//
// NOTE: these assert the engine PROJECTION (the rule-engine row). The bucket
// round-trip of the SCALAR convergence columns through the actorAggregate
// projection EnvelopeFn (scalar passthrough — Contract #6 §6.13, CAR E6) is
// proven in internal/refractor's lease-signing scalar convergence e2e; the cypher
// itself is proven here.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
)

func cypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	opts := &natsserver.Options{JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true, Port: natsserver.RANDOM_PORT}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close(); s.Shutdown() })
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// cNanoID returns a deterministic 20-char Contract #1 NanoID from a logical name.
func cNanoID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte(name) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

type lensFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string // logicalName -> bare NanoID
	types         map[string]string // bare NanoID -> type
}

func newLensFixture(t *testing.T) *lensFixture {
	adjKV, coreKV := cypherKVs(t)
	return &lensFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *lensFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := cNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *lensFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *lensFixture) edge(t *testing.T, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

// farFutureValidUntil is a validUntil far enough ahead that a bgcheck stamped
// with it is FRESH regardless of when the suite runs (the cypher's
// `validUntil > $now` holds for any realistic wall clock). Used by the
// gap-closure tests that care about completeness, not the freshness boundary.
const farFutureValidUntil = "2099-01-01T00:00:00Z"

func (f *lensFixture) project(t *testing.T, appName string) []ruleengine.ProjectionResult {
	t.Helper()
	return f.projectAt(t, appName, time.Now().UTC().Format(time.RFC3339))
}

// projectAt runs the lens with an INJECTED $now so freshness boundary tests can
// place validUntil before/after the projection instant deterministically. $now
// here is the same param the live pipeline supplies (executeFullForActor sets
// params["now"] = time.Now().UTC().Format(time.RFC3339)).
func (f *lensFixture) projectAt(t *testing.T, appName, now string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(leaseApplicationCompleteSpec)
	require.NoError(t, err, "leaseApplicationComplete cypher must parse on the full engine")
	appKey := "vtx.leaseapp." + f.ids[appName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    appKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// TestLeaseApplicationComplete_ProjectsOneRowPerAnchor (test 1 — AC #1 + §0.C).
// A multi-instance fixture (the applicant has 2 service instances) is the
// fan-out case the one-row-per-anchor guard would trip. The cypher MUST emit
// exactly one row carrying the right gap columns + non-null entityKey/applicant.
func TestLeaseApplicationComplete_ProjectsOneRowPerAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	appKey := f.vtx(t, "app", "leaseapp")
	idKey := f.vtx(t, "alice", "identity")
	// TWO service instances providedTo alice (one bgcheck, one payment), no
	// outcome yet — the multi-instance fan-out case.
	f.vtx(t, "bg1", "service")
	f.aspect(t, "bg1", "family", "family", map[string]any{"value": "backgroundCheck"})
	f.vtx(t, "pay1", "service")
	f.aspect(t, "pay1", "family", "family", map[string]any{"value": "payment"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "exactly one weaver-targets row per leaseapp anchor even with 2 instances (§0.C guard)")
	v := rows[0].Values
	require.Equal(t, appKey, v["entityKey"], "entityKey must be the full leaseapp key")
	require.Equal(t, idKey, v["applicant"], "applicant must be the full identity key")
	require.Equal(t, true, v["missing_onboarding"])
	require.Equal(t, true, v["missing_bgcheck"])
	require.Equal(t, true, v["missing_payment"])
	require.Equal(t, true, v["missing_signature"])
	require.Equal(t, true, v["violating"])
	// the row key (actorKey, the first RETURN column) is the full leaseapp key;
	// the bare-NanoID convergence key is derived by BuildKey from it (14.2).
	require.Equal(t, appKey, v["actorKey"])
}

// TestLeaseApplicationComplete_OutcomeFlipsGap_DirectWrite (test 2 — AC #1 + AC
// #4 + the dependent freshness). From all-gaps-open, recording the bgcheck
// instance's .outcome (status completed, validUntil in the future) flips
// missing_bgcheck false while a payment instance with NO outcome leaves
// missing_payment true; recording the applicant's ssn flips missing_onboarding
// false. Still exactly one row.
func TestLeaseApplicationComplete_OutcomeFlipsGap_DirectWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"}) // onboarded
	f.vtx(t, "bg1", "service")
	f.aspect(t, "bg1", "family", "family", map[string]any{"value": "backgroundCheck"})
	// A FRESH completed bgcheck: validUntil far in the future (time-independent).
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": farFutureValidUntil})
	f.vtx(t, "pay1", "service")
	f.aspect(t, "pay1", "family", "family", map[string]any{"value": "payment"}) // no outcome
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_onboarding"], "ssn present → onboarded")
	require.Equal(t, false, v["missing_bgcheck"], "completed AND fresh bgcheck outcome → not missing")
	require.Equal(t, true, v["missing_payment"], "payment instance with no outcome → still missing")
	require.Equal(t, true, v["missing_signature"], "no signature yet")
	require.Equal(t, true, v["violating"], "payment + signature still open → violating")
}

// TestLeaseApplicationComplete_SignatureFlipsGap (test 8 — the assignTask gap
// closure at the lens level). Writing the leaseapp's .signature aspect flips
// missing_signature false; with all other gaps closed the row goes
// violating:false.
func TestLeaseApplicationComplete_SignatureFlipsGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.vtx(t, "bg1", "service")
	f.aspect(t, "bg1", "family", "family", map[string]any{"value": "backgroundCheck"})
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": farFutureValidUntil})
	f.vtx(t, "pay1", "service")
	f.aspect(t, "pay1", "family", "family", map[string]any{"value": "payment"})
	// Payment is ever-completed: it carries NO validUntil here, proving the lens
	// does not apply the freshness gate to payment.
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_signature"], "signature present → not missing")
	require.Equal(t, false, v["missing_onboarding"])
	require.Equal(t, false, v["missing_bgcheck"])
	require.Equal(t, false, v["missing_payment"], "completed payment with no validUntil → not missing (ever-completed)")
	require.Equal(t, false, v["violating"], "all gaps closed → not violating")
}

// bgFreshnessFixture builds a one-applicant fixture (onboarded, signed) with a
// completed payment (ever-completed, no validUntil) and a completed bgcheck whose
// validUntil is the caller's choice — the multi-instance fan-out the freshness
// tests share. All gaps but bgcheck are closed, so missing_bgcheck alone decides
// `violating`. Returns the app name for projection.
func bgFreshnessFixture(t *testing.T, f *lensFixture, bgValidUntil string) string {
	t.Helper()
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.vtx(t, "bg1", "service")
	f.aspect(t, "bg1", "family", "family", map[string]any{"value": "backgroundCheck"})
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": bgValidUntil})
	f.vtx(t, "pay1", "service")
	f.aspect(t, "pay1", "family", "family", map[string]any{"value": "payment"})
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")
	return "app"
}

// TestLeaseApplicationComplete_FreshBgcheck (freshness predicate case a). A
// completed bgcheck whose validUntil is AFTER the injected $now counts toward
// convergence: missing_bgcheck false, exactly one row even with the
// bgcheck+payment fan-out.
func TestLeaseApplicationComplete_FreshBgcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	const validUntil = "2026-06-18T00:05:00Z" // 5 minutes after now → fresh
	app := bgFreshnessFixture(t, f, validUntil)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1, "exactly one row per anchor (bgcheck+payment fan-out)")
	v := rows[0].Values
	require.Equal(t, false, v["missing_bgcheck"], "completed bgcheck with validUntil > now → not missing")
	require.Equal(t, false, v["missing_payment"])
	require.Equal(t, false, v["violating"], "all gaps closed → not violating")
}

// TestLeaseApplicationComplete_FailedBgcheck pins that a terminal FAILED outcome
// is NOT counted toward convergence. A FRESH (validUntil > now) bgcheck whose
// .outcome.status is "failed" — a definitive business rejection, now a producible
// terminal state — keeps missing_bgcheck (hence violating) TRUE. The fresh
// validUntil isolates the status from the freshness predicate: freshness is not
// the reason the instance is excluded, the status is. Guards against a refactor of
// the cypher's `outcome.status = 'completed'` CASE (e.g. to `IS NOT NULL`) that
// would silently count a failed outcome as converged — every other lens test uses
// completed/absent and would not catch it.
func TestLeaseApplicationComplete_FailedBgcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	const validUntil = "2026-06-18T00:05:00Z" // fresh (> now): the status, not staleness, excludes it
	app := bgFreshnessFixture(t, f, validUntil)
	// Overwrite the fixture's completed bgcheck with a terminal FAILURE (still fresh).
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "failed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": validUntil})

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_bgcheck"], "a FAILED bgcheck (even fresh) is not converged → gap stays open")
	require.Equal(t, false, v["missing_payment"], "the fixture's payment is completed → not missing")
	require.Equal(t, true, v["violating"], "missing_bgcheck alone keeps the application violating")
}

// TestLeaseApplicationComplete_FailedPayment pins the same predicate on the
// payment CASE (a distinct cypher line from bgcheck): a payment instance whose
// .outcome.status is "failed" is NOT counted, so missing_payment (hence violating)
// stays TRUE while the bgcheck gap is closed.
func TestLeaseApplicationComplete_FailedPayment(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	app := bgFreshnessFixture(t, f, farFutureValidUntil) // bgcheck completed + fresh → not the gap
	// Overwrite the fixture's completed payment with a terminal FAILURE.
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "failed", "completedAt": "2026-06-02T00:00:00Z"})

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_bgcheck"], "the fixture's bgcheck is completed + fresh → not missing")
	require.Equal(t, true, v["missing_payment"], "a FAILED payment is not converged → gap stays open")
	require.Equal(t, true, v["violating"], "missing_payment alone keeps the application violating")
}

// TestLeaseApplicationComplete_StaleBgcheck (freshness predicate case b — the
// core of the refinement). A completed bgcheck whose validUntil is AT/BEFORE $now
// no longer counts: missing_bgcheck RE-OPENS to true whenever the row is
// (re)evaluated (a stale background check is a missing background check). The
// eager auto-reopen-at-expiry via a §10.2 freshUntil column is exercised by the
// lease-convergence e2e; here we prove the predicate alone re-opens the gap at the injected $now.
func TestLeaseApplicationComplete_StaleBgcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	const validUntil = "2026-06-17T23:55:00Z" // 5 minutes BEFORE now → stale
	app := bgFreshnessFixture(t, f, validUntil)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_bgcheck"], "stale bgcheck (validUntil <= now) → gap RE-OPENS")
	require.Equal(t, false, v["missing_payment"], "payment unaffected by bgcheck staleness")
	require.Equal(t, true, v["violating"], "re-opened bgcheck gap → violating again")
}

// TestLeaseApplicationComplete_StaleBgcheck_BoundaryEqualsNow pins the boundary:
// validUntil EXACTLY equal to $now is stale (the cypher's strict `>` excludes the
// equal instant), so the gap is open.
func TestLeaseApplicationComplete_StaleBgcheck_BoundaryEqualsNow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	app := bgFreshnessFixture(t, f, now) // validUntil == now

	v := f.projectAt(t, app, now)[0].Values
	require.Equal(t, true, v["missing_bgcheck"], "validUntil == now is NOT fresh (strict > boundary)")
}

// TestLeaseApplicationComplete_PaymentIgnoresValidUntil (freshness case c). A
// completed payment whose validUntil is in the PAST still closes missing_payment:
// the freshness policy is bgcheck-only; payment is ever-completed. The bgcheck
// here is fresh so missing_bgcheck stays false — only the payment branch is under
// test. Exactly one row across the multi-instance fan-out.
func TestLeaseApplicationComplete_PaymentIgnoresValidUntil(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.vtx(t, "bg1", "service")
	f.aspect(t, "bg1", "family", "family", map[string]any{"value": "backgroundCheck"})
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-17T00:00:00Z", "validUntil": "2026-06-18T00:05:00Z"})
	f.vtx(t, "pay1", "service")
	f.aspect(t, "pay1", "family", "family", map[string]any{"value": "payment"})
	// Payment completed long ago with a PAST validUntil — the lens must ignore it.
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-01-01T00:00:00Z", "validUntil": "2026-01-01T00:05:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.projectAt(t, "app", now)
	require.Len(t, rows, 1, "exactly one row per anchor")
	v := rows[0].Values
	require.Equal(t, false, v["missing_payment"], "payment with a PAST validUntil still counts (ever-completed)")
	require.Equal(t, false, v["missing_bgcheck"], "the bgcheck is fresh")
	require.Equal(t, false, v["violating"])
}

// TestLeaseApplicationComplete_NoCompletedBgcheck. A bgcheck instance that is NOT
// yet completed (no .outcome) leaves missing_bgcheck true, and the row still
// projects despite the completed payment sharing the providedTo link.
func TestLeaseApplicationComplete_NoCompletedBgcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.vtx(t, "bg1", "service")
	f.aspect(t, "bg1", "family", "family", map[string]any{"value": "backgroundCheck"}) // no outcome yet
	f.vtx(t, "pay1", "service")
	f.aspect(t, "pay1", "family", "family", map[string]any{"value": "payment"})
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.projectAt(t, "app", now)
	require.Len(t, rows, 1, "row still projects when the bgcheck is not yet completed")
	v := rows[0].Values
	require.Equal(t, true, v["missing_bgcheck"], "bgcheck instance present but no completed outcome → missing")
}

// TestLeaseApplicationComplete_PaymentInstanceNoBgcheck_NoDrop is the regression
// guard for the FR58 double-act path the freshness refinement must NOT reintroduce.
// The applicant has a COMPLETED payment instance and NO bgcheck instance at all —
// the real transient convergence window (payment's instanceOp commits + reprojects
// before bgcheck's). With a dedicated family-filtered bgcheck OPTIONAL MATCH this
// would WHERE-filter the sole providedTo neighbor (the payment) and the full
// engine's null-restore would fail to re-emit the anchor → the row DROPS → Weaver
// reads an entity deletion, clears the leaseapp's gap marks, and on row
// re-appearance re-dispatches a SECOND bgcheck Loom instance (a second external
// call). The single no-WHERE providedTo fan keeps the anchor: exactly one row,
// missing_bgcheck true, missing_payment false.
func TestLeaseApplicationComplete_PaymentInstanceNoBgcheck_NoDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	// Only a COMPLETED payment instance providedTo alice — NO bgcheck instance.
	f.vtx(t, "pay1", "service")
	f.aspect(t, "pay1", "family", "family", map[string]any{"value": "payment"})
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.projectAt(t, "app", now)
	require.Len(t, rows, 1, "the anchor row MUST NOT drop when a payment instance exists but no bgcheck (FR58 double-act guard)")
	v := rows[0].Values
	require.Equal(t, true, v["missing_bgcheck"], "no bgcheck instance → gap open")
	require.Equal(t, false, v["missing_payment"], "completed payment → gap closed")
	// The dedicated bgcheck OPTIONAL MATCH WHERE filters the sole providedTo
	// neighbor (the payment) → the executor null-restore preserves the anchor with
	// bg null → freshUntil projects as a genuine null (no timer for Weaver to arm),
	// the anchor never drops. This is the §0.B engine fix exercised through the lens.
	require.Nil(t, v["freshUntil"], "no fresh bgcheck → freshUntil is null (anchor preserved, no @at armed)")
}

// TestLeaseApplicationComplete_FreshUntilProjected pins the eager-freshness column
// (§10.2): a completed FRESH bgcheck projects its validUntil as the scalar
// freshUntil so Weaver's temporal lane schedules an @at at that instant. The
// column is the bgcheck outcome's validUntil verbatim, projected once per anchor.
func TestLeaseApplicationComplete_FreshUntilProjected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	const validUntil = "2026-06-18T00:05:00Z" // fresh: 5m after now
	app := bgFreshnessFixture(t, f, validUntil)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1, "exactly one row per anchor (bgcheck+payment fan-out, dedicated bg match ≤1 row)")
	v := rows[0].Values
	require.Equal(t, validUntil, v["freshUntil"],
		"a fresh completed bgcheck projects its validUntil as the scalar freshUntil column")
	require.Equal(t, false, v["missing_bgcheck"], "fresh bgcheck → gap closed")
	// freshUntil is a string scalar (not a list) so Weaver's scheduleFreshness
	// parses it as RFC3339 and arms the @at.
	_, isString := v["freshUntil"].(string)
	require.True(t, isString, "freshUntil must be a scalar string, not a list")
}

// TestLeaseApplicationComplete_FreshUntilNullWhenStale: a STALE bgcheck (its
// validUntil already past $now) is excluded by the dedicated bgcheck match's
// WHERE, so freshUntil is null (Weaver clears any standing @at — there is no
// future deadline to arm) and the gap re-opens. One row, anchor preserved.
func TestLeaseApplicationComplete_FreshUntilNullWhenStale(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	const validUntil = "2026-06-17T23:55:00Z" // stale: 5m before now
	app := bgFreshnessFixture(t, f, validUntil)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["freshUntil"], "a stale bgcheck is filtered → freshUntil null (no @at to arm)")
	require.Equal(t, true, v["missing_bgcheck"], "stale bgcheck → gap re-opens")
}

// TestLeaseApplicationComplete_FreshUntilNullBeforeOnboarding: with all gaps open
// (no PII, no bgcheck, no payment, no signature) freshUntil is null and the
// anchor still projects exactly one row — the eager column never drops the
// fresh-application row.
func TestLeaseApplicationComplete_FreshUntilNullBeforeOnboarding(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.edge(t, "applicationFor", "app", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "a fresh all-gaps-open application projects exactly one row")
	v := rows[0].Values
	require.Nil(t, v["freshUntil"], "no completed bgcheck → freshUntil null")
	require.Equal(t, true, v["violating"])
	require.Equal(t, true, v["missing_bgcheck"])
}

// inflightBgFixture seeds an applicant (PII recorded, signed — so onboarding and
// signature are not the gap under test) whose single bgcheck service instance
// carries a .dispatch marker (present iff withDispatch) and the given .outcome
// status (nil → omit the .outcome aspect entirely). It is the in-flight
// suppression fixture: the bgcheck gap stays open (no completed outcome), and the
// dispatch/outcome shape drives inflight_bgcheck. The predicate is presence-based
// (a .dispatch present + no .outcome), so no deadline is modeled. Returns the
// leaseapp logical name.
func inflightBgFixture(t *testing.T, f *lensFixture, withDispatch bool, outcomeStatus *string) string {
	t.Helper()
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.vtx(t, "bg1", "service")
	f.aspect(t, "bg1", "family", "family", map[string]any{"value": "backgroundCheck"})
	if withDispatch {
		f.aspect(t, "bg1", "dispatch", "dispatch", map[string]any{
			"vendorRef": "vendor-ref-1", "adapter": "backgroundCheck", "replyOp": "RecordLeaseServiceOutcome",
			"submittedAt": "2026-06-17T00:00:00Z", "nextPollAt": "2026-06-18T00:01:00Z", "deadline": "2026-06-18T00:05:00Z",
		})
	}
	if outcomeStatus != nil {
		f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": *outcomeStatus, "completedAt": "2026-06-17T12:00:00Z"})
	}
	// A completed payment so missing_payment is not the only open gap — the row
	// stays violating purely on the bgcheck dimension.
	f.vtx(t, "pay1", "service")
	f.aspect(t, "pay1", "family", "family", map[string]any{"value": "payment"})
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")
	return "app"
}

// TestLeaseApplicationComplete_InflightBgcheck pins the in-flight suppression
// companion: a bgcheck with a .dispatch marker PRESENT and NO .outcome projects
// inflight_bgcheck=true while the gap stays open (missing_bgcheck=true,
// violating=true). Weaver reads inflight_bgcheck to skip re-dispatch of the
// legitimately-pending call. The row also carries the constant maxretries_<g>
// caps (the budget bound, baked from retry_budget.go).
func TestLeaseApplicationComplete_InflightBgcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	app := inflightBgFixture(t, f, true, nil)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1, "exactly one row per anchor with a dispatch marker in the fan")
	v := rows[0].Values
	require.Equal(t, true, v["inflight_bgcheck"], "a .dispatch marker present, no .outcome → in flight")
	require.Equal(t, false, v["inflight_payment"], "payment has no dispatch marker → not in flight")
	require.Equal(t, true, v["missing_bgcheck"], "an in-flight call is NOT a completed one → gap stays open")
	require.Equal(t, true, v["violating"], "the gap stays violating while the call is in flight")
	requireMaxRetriesColumns(t, v)
}

// TestLeaseApplicationComplete_InflightBgcheck_DeadIrrelevant is the FIX-FIRST
// correctness pin: inflight is PRESENCE-based, NOT deadline-bounded. A .dispatch
// present + no .outcome is in flight regardless of the dispatch deadline — so a
// dead/slow bridge that never posts a timeout outcome keeps inflight_bgcheck=true
// (Weaver waits, never double-dispatching against the vendor) rather than flipping
// it false at the deadline. The fixture's deadline is in the past relative to
// $now, yet inflight stays true.
func TestLeaseApplicationComplete_InflightBgcheck_DeadIrrelevant(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	// $now is AFTER the fixture's dispatch deadline (2026-06-18T00:05:00Z): under a
	// deadline-bounded predicate this would read inflight=false (the old
	// double-dispatch hole); under the presence-based predicate it stays true.
	const now = "2026-06-19T00:00:00Z"
	app := inflightBgFixture(t, f, true, nil)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["inflight_bgcheck"], "a still-unresolved call stays in flight past its deadline (no double-dispatch)")
	require.Equal(t, true, v["missing_bgcheck"], "the gap is still open")
	require.Equal(t, true, v["violating"])
}

// TestLeaseApplicationComplete_InflightBgcheck_OutcomePresent: a .dispatch marker
// present but an .outcome already written is NOT in flight — the call resolved
// (the create-only outcome landed), so inflight_bgcheck is false and Weaver
// resumes dispatching a FRESH call. The status here is 'failed' (so the gap stays
// open) to isolate the inflight predicate from the completed-gap-close path.
func TestLeaseApplicationComplete_InflightBgcheck_OutcomePresent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	failed := "failed"
	app := inflightBgFixture(t, f, true, &failed)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["inflight_bgcheck"], "an .outcome present (status != null) → not in flight; re-dispatch resumes")
	require.Equal(t, true, v["missing_bgcheck"], "a failed outcome does not close the gap")
}

// TestLeaseApplicationComplete_InflightBgcheck_NoDispatch: a bgcheck instance with
// NO .dispatch marker (and no .outcome) is not in flight — vendorRef <> null is
// false when the .dispatch aspect is absent, so inflight_bgcheck=false and the gap
// is dispatchable.
func TestLeaseApplicationComplete_InflightBgcheck_NoDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	app := inflightBgFixture(t, f, false, nil)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["inflight_bgcheck"], "no .dispatch aspect (vendorRef = null) → not in flight")
	require.Equal(t, true, v["missing_bgcheck"], "the gap is open and dispatchable")
}

// requireMaxRetriesColumns asserts the lens projects the constant per-gap retry
// caps as numeric columns equal to the package constants (the §E mechanism-B
// budget bound Weaver reads off the row). The full engine returns numeric literals
// as int (a Go int via the parser), but the cross-component path carries JSON
// numbers as float64, so accept either — what matters is the integer value.
func requireMaxRetriesColumns(t *testing.T, v map[string]any) {
	t.Helper()
	requireIntColumn(t, v, "maxretries_bgcheck", maxBgcheckRetries)
	requireIntColumn(t, v, "maxretries_payment", maxPaymentRetries)
}

func requireIntColumn(t *testing.T, v map[string]any, col string, want int) {
	t.Helper()
	got, ok := v[col]
	require.Truef(t, ok, "row must carry the %s column", col)
	switch n := got.(type) {
	case int:
		require.Equalf(t, want, n, "%s", col)
	case int64:
		require.Equalf(t, want, int(n), "%s", col)
	case float64:
		require.Equalf(t, want, int(n), "%s", col)
	default:
		t.Fatalf("%s is %T, not a numeric cap", col, got)
	}
}

// TestLeaseApplicationComplete_MaxRetriesColumns pins the constant retry-cap
// columns: every convergence row carries maxretries_bgcheck / maxretries_payment
// equal to the package constants, regardless of the gap state. They are the
// package-owned budget bound Weaver compares its weaver-state dispatch-count
// against; the budget accounting itself is proven in internal/weaver (the lens no
// longer projects a failed-count).
func TestLeaseApplicationComplete_MaxRetriesColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	// All-gaps-open application — the caps must still be present.
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.edge(t, "applicationFor", "app", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	requireMaxRetriesColumns(t, rows[0].Values)
}
