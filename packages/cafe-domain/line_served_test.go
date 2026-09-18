package cafedomain_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// selfOrderFixture seeds a resident with an approved lease at a unit, an open
// tab on it and one menu item served at that unit — everything a self-service
// Charge needs. Returns the tab, the item and the applicationFor link the
// self leg declares.
func selfOrderFixture(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, leaseID, unitID, label string) (string, string, string) {
	t.Helper()
	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, leaseID, domainConsumerID)
	unitKey := seedLocation(t, ctx, conn, unitID)
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	tabKey := openTab(t, ctx, conn, cp, cons, label+"tab", leaseKey)
	itemKey := createMenuItem(t, ctx, conn, cp, cons, label+"itm", "Latte", 450, unitKey)
	return tabKey, itemKey, "lnk.leaseapp." + leaseID + ".applicationFor.identity." + domainConsumerID
}

// selfOrder submits Charge{tabKey, menuItemKey} as the resident on their own
// tab and asserts acceptance.
func selfOrder(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, tabKey, itemKey, applicationForLnk, submittedAt string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainConsumerKey,
		SubmittedAt:   submittedAt,
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","menuItemKey":"` + itemKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status", itemKey, itemKey + ".price"},
			OptionalReads: []string{applicationForLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// staffCharge submits an off-menu Charge as the operator and asserts acceptance.
func staffCharge(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, tabKey string, amountCents int, submittedAt string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   submittedAt,
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":` + strconv.Itoa(amountCents) + `}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// markLineServedEnv is the MarkLineServed envelope a staff caller submits:
// the tab + its .status declared, the holdsRole enumeration the confinement
// walk needs.
func markLineServedEnv(label, tabKey, lineID, actorKey, submittedAt string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "MarkLineServed",
		Actor:         actorKey,
		SubmittedAt:   submittedAt,
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","lineId":"` + lineID + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
}

func markLineServedRejectedWith(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, tabKey, lineID, want string) {
	t.Helper()
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, markLineServedEnv(label, tabKey, lineID, domainActorKey, "2026-07-22T12:30:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("%s: outcome = %q error = %+v, want rejected", label, outcome, reply.Error)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, want) {
		t.Fatalf("%s: rejected with %+v, want a message carrying %q", label, reply.Error, want)
	}
}

func tabLines(t *testing.T, ctx context.Context, conn *substrate.Conn, tabKey string) (map[string]any, []map[string]any) {
	t.Helper()
	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	raw, _ := statusData["lines"].([]any)
	lines := make([]map[string]any, 0, len(raw))
	for _, l := range raw {
		m, _ := l.(map[string]any)
		lines = append(lines, m)
	}
	return statusData, lines
}

// TestCharge_SelfOrder_LineIsOrderedNotServed: a resident's self-order records
// when it was ordered and nothing about being handed over — that line is the
// desk's work until MarkLineServed.
func TestCharge_SelfOrder_LineIsOrderedNotServed(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "selfordered")
	tabKey, itemKey, appFor := selfOrderFixture(t, ctx, conn, cp, cons, "BBCAFEDMNSRVALEASEHJ", "BBCAFEDMNSRVAUNTHJKM", "cdsrva0000000001")

	selfOrder(t, ctx, conn, cp, cons, "cdsrvaorder000000001", tabKey, itemKey, appFor, "2026-07-22T12:10:00Z")

	_, lines := tabLines(t, ctx, conn, tabKey)
	if len(lines) != 1 {
		t.Fatalf("status.lines has %d entries, want 1", len(lines))
	}
	if got, want := lines[0]["orderedAt"], "2026-07-22T12:10:00Z"; got != want {
		t.Fatalf("lines[0].orderedAt = %v, want %q (the Charge's own submittedAt)", got, want)
	}
	if _, has := lines[0]["servedAt"]; has {
		t.Fatalf("lines[0] carries servedAt %v on a self-order — a self-ordered line is unserved until the desk marks it", lines[0]["servedAt"])
	}
	if _, has := lines[0]["servedBy"]; has {
		t.Fatalf("lines[0] carries servedBy on a self-order")
	}
}

// TestCharge_Staff_LineIsServedAtRingUp: a POS ring-up is handed over at the
// counter, so the staff leg stamps the line served the moment it is charged.
func TestCharge_Staff_LineIsServedAtRingUp(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "staffserved")
	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNSRVBLEASEHJ")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdsrvbtab00000000001", leaseKey)

	staffCharge(t, ctx, conn, cp, cons, "cdsrvbcharge00000001", tabKey, 300, "2026-07-22T12:11:00Z")

	_, lines := tabLines(t, ctx, conn, tabKey)
	if len(lines) != 1 {
		t.Fatalf("status.lines has %d entries, want 1", len(lines))
	}
	if got, want := lines[0]["orderedAt"], "2026-07-22T12:11:00Z"; got != want {
		t.Fatalf("lines[0].orderedAt = %v, want %q", got, want)
	}
	if got, want := lines[0]["servedAt"], "2026-07-22T12:11:00Z"; got != want {
		t.Fatalf("lines[0].servedAt = %v, want %q (a staff ring-up is served at ring-up)", got, want)
	}
	if got, want := lines[0]["servedBy"], domainActorKey; got != want {
		t.Fatalf("lines[0].servedBy = %v, want %q", got, want)
	}
}

// TestMarkLineServed_StampsOneLineOnly: the desk hands over line-1 of two
// self-orders — servedAt is the op's own submittedAt, servedBy its actor,
// line-2 stays unserved, and the total and memo do not move.
func TestMarkLineServed_StampsOneLineOnly(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "markserved")
	tabKey, itemKey, appFor := selfOrderFixture(t, ctx, conn, cp, cons, "BBCAFEDMNSRVCLEASEHJ", "BBCAFEDMNSRVCUNTHJKM", "cdsrvc0000000001")
	selfOrder(t, ctx, conn, cp, cons, "cdsrvcorder100000001", tabKey, itemKey, appFor, "2026-07-22T12:10:00Z")
	selfOrder(t, ctx, conn, cp, cons, "cdsrvcorder200000001", tabKey, itemKey, appFor, "2026-07-22T12:12:00Z")

	before, _ := tabLines(t, ctx, conn, tabKey)

	testutil.PublishOp(t, conn, markLineServedEnv("cdsrvcserve100000001", tabKey, "line-1", domainActorKey, "2026-07-22T12:15:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	status, lines := tabLines(t, ctx, conn, tabKey)
	// Every other .status key rides through the OCC upsert untouched: a
	// dropped staleAt would re-arm the stale-tab lens's backfill, a dropped
	// openedAt would unsort the desk, a dropped leaseAppKey would orphan
	// every later confinement walk.
	for _, k := range []string{"openedAt", "staleAt", "leaseAppKey"} {
		if got, want := status[k], before[k]; got != want || got == nil {
			t.Fatalf("status.%s = %v after MarkLineServed, want %v carried unchanged", k, got, want)
		}
	}
	if got, _ := status["totalCents"].(float64); got != 900 {
		t.Fatalf("status.totalCents = %v, want 900 — MarkLineServed must not move the total", got)
	}
	if got, want := status["itemsMemo"], "Latte, Latte"; got != want {
		t.Fatalf("status.itemsMemo = %v, want %q — MarkLineServed must not touch the memo", got, want)
	}
	if got, _ := status["value"].(string); got != "open" {
		t.Fatalf("status.value = %q, want open", got)
	}
	if len(lines) != 2 {
		t.Fatalf("status.lines has %d entries, want 2", len(lines))
	}
	if got, want := lines[0]["servedAt"], "2026-07-22T12:15:00Z"; got != want {
		t.Fatalf("lines[0].servedAt = %v, want %q (MarkLineServed's own submittedAt, not the order's)", got, want)
	}
	if got, want := lines[0]["servedBy"], domainActorKey; got != want {
		t.Fatalf("lines[0].servedBy = %v, want %q", got, want)
	}
	if got, want := lines[0]["orderedAt"], "2026-07-22T12:10:00Z"; got != want {
		t.Fatalf("lines[0].orderedAt = %v, want %q — the serve must not rewrite when it was ordered", got, want)
	}
	if got, want := lines[0]["orderedBy"], domainConsumerKey; got != want {
		t.Fatalf("lines[0].orderedBy = %v, want %q — the serve must not rewrite who ordered", got, want)
	}
	if got, _ := lines[0]["voided"].(bool); got {
		t.Fatalf("lines[0].voided = true after a serve")
	}
	if _, has := lines[1]["servedAt"]; has {
		t.Fatalf("lines[1] carries servedAt %v — only line-1 was handed over", lines[1]["servedAt"])
	}
}

// TestMarkLineServed_Refusals: the four recorded-state refusals, each on a
// tab whose positive sibling (line-1 served above) already proved the path.
func TestMarkLineServed_Refusals(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "markrefused")
	tabKey, itemKey, appFor := selfOrderFixture(t, ctx, conn, cp, cons, "BBCAFEDMNSRVDLEASEHJ", "BBCAFEDMNSRVDUNTHJKM", "cdsrvd0000000001")
	selfOrder(t, ctx, conn, cp, cons, "cdsrvdorder100000001", tabKey, itemKey, appFor, "2026-07-22T12:10:00Z")
	selfOrder(t, ctx, conn, cp, cons, "cdsrvdorder200000001", tabKey, itemKey, appFor, "2026-07-22T12:11:00Z")
	selfOrder(t, ctx, conn, cp, cons, "cdsrvdorder300000001", tabKey, itemKey, appFor, "2026-07-22T12:12:00Z")

	// Positive sibling first.
	testutil.PublishOp(t, conn, markLineServedEnv("cdsrvdserve100000001", tabKey, "line-1", domainActorKey, "2026-07-22T12:15:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// A second hand-over of the same line.
	markLineServedRejectedWith(t, ctx, conn, cp, cons, "cdsrvdserve1again001", tabKey, "line-1", "LineAlreadyServed")

	// A voided line has nothing to hand over.
	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsrvdvoid2000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:16:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","lineId":"line-2"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, voidEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	markLineServedRejectedWith(t, ctx, conn, cp, cons, "cdsrvdserve2voided01", tabKey, "line-2", "LineVoided")

	// No such line.
	markLineServedRejectedWith(t, ctx, conn, cp, cons, "cdsrvdserve9unknown1", tabKey, "line-9", "UnknownChargeLine")

	// Nothing above wrote: line-3 is still unserved, line-1 keeps its first stamp.
	_, lines := tabLines(t, ctx, conn, tabKey)
	if got, want := lines[0]["servedAt"], "2026-07-22T12:15:00Z"; got != want {
		t.Fatalf("lines[0].servedAt = %v, want %q — a refused re-serve must not re-stamp", got, want)
	}
	if _, has := lines[2]["servedAt"]; has {
		t.Fatalf("lines[2] carries servedAt after refused submissions naming other lines")
	}

	// Settled: the tab is frozen. Line-3 is handed over first — Settle
	// refuses UnservedLines over a line still to make — and the refusal on
	// the closed tab is still TabNotOpen: require_open_status runs before the
	// line lookup, so the served line never reaches LineAlreadyServed.
	testutil.PublishOp(t, conn, markLineServedEnv("cdsrvdserve300000001", tabKey, "line-3", domainActorKey, "2026-07-22T12:40:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	settleEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsrvdsettle00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T13:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: tabKey, Relation: "chargedTo", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, settleEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	markLineServedRejectedWith(t, ctx, conn, cp, cons, "cdsrvdserve3settled1", tabKey, "line-3", "TabNotOpen")
}

// TestMarkLineServed_RejectsForConsumer: handing an order over is the desk's
// act — the resident who ordered it holds no MarkLineServed grant.
func TestMarkLineServed_RejectsForConsumer(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "markconsumer")
	tabKey, itemKey, appFor := selfOrderFixture(t, ctx, conn, cp, cons, "BBCAFEDMNSRVELEASEHJ", "BBCAFEDMNSRVEUNTHJKM", "cdsrve0000000001")
	selfOrder(t, ctx, conn, cp, cons, "cdsrveorder100000001", tabKey, itemKey, appFor, "2026-07-22T12:10:00Z")

	env := markLineServedEnv("cdsrveserveself00001", tabKey, "line-1", domainConsumerKey, "2026-07-22T12:15:00Z")
	env.ContextHint.Enumerations = nil
	env.AuthContext = &processor.AuthContext{Target: domainConsumerKey}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	_, lines := tabLines(t, ctx, conn, tabKey)
	if _, has := lines[0]["servedAt"]; has {
		t.Fatalf("a consumer's MarkLineServed wrote servedAt %v", lines[0]["servedAt"])
	}
}

// TestVoidCharge_KeepsOrderedAt: a void copies the line whole — what the
// line recorded about being ordered survives it.
func TestVoidCharge_KeepsOrderedAt(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "voidkeeps")
	tabKey, itemKey, appFor := selfOrderFixture(t, ctx, conn, cp, cons, "BBCAFEDMNSRVFLEASEHJ", "BBCAFEDMNSRVFUNTHJKM", "cdsrvf0000000001")
	selfOrder(t, ctx, conn, cp, cons, "cdsrvforder100000001", tabKey, itemKey, appFor, "2026-07-22T12:10:00Z")
	testutil.PublishOp(t, conn, markLineServedEnv("cdsrvfserve100000001", tabKey, "line-1", domainActorKey, "2026-07-22T12:15:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsrvfvoid1000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:20:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","lineId":"line-1"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, voidEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	_, lines := tabLines(t, ctx, conn, tabKey)
	if got, _ := lines[0]["voided"].(bool); !got {
		t.Fatalf("lines[0].voided = false after VoidCharge")
	}
	for _, k := range []string{"orderedAt", "servedAt", "servedBy", "orderedBy"} {
		if _, has := lines[0][k]; !has {
			t.Fatalf("lines[0] lost %s on void — VoidCharge must copy the line whole", k)
		}
	}
}

// TestDescriptorDrivenMarkLineServed proves the shipped MarkLineServed op-meta
// declares enough for a descriptor-driven client (Facet's staff world): every
// ContextHint key is substituted from Dispatch.Reads, the enumeration from
// Dispatch.Enumerations, and the hand-over lands.
func TestDescriptorDrivenMarkLineServed(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "descriptorserve")
	tabKey, itemKey, appFor := selfOrderFixture(t, ctx, conn, cp, cons, "BBCAFEDMNSRVGLEASEHJ", "BBCAFEDMNSRVGUNTHJKM", "cdsrvg0000000001")
	selfOrder(t, ctx, conn, cp, cons, "cdsrvgorder100000001", tabKey, itemKey, appFor, "2026-07-22T12:10:00Z")

	d := dispatchFor(t, "MarkLineServed")
	if d.AuthContext != "standing" || d.TargetField != "tabKey" || d.Class != "tab" {
		t.Fatalf("MarkLineServed dispatch = %+v, want a standing tab dispatch targeting tabKey", d)
	}
	payload := map[string]string{"tabKey": tabKey, "lineId": "line-1"}
	var reads []string
	for _, tmpl := range d.Reads {
		reads = append(reads, substituteDispatch(tmpl, domainActorKey, payload))
	}
	var enums []processor.EnumerationHint
	for _, e := range d.Enumerations {
		enums = append(enums, processor.EnumerationHint{Hub: substituteDispatch(e.Hub, domainActorKey, payload), Relation: e.Relation, Direction: e.Direction})
	}
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsrvgdescserve00001"),
		Lane:          processor.LaneDefault,
		OperationType: "MarkLineServed",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:15:00Z",
		Class:         d.Class,
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","lineId":"line-1"}`),
		ContextHint:   &processor.ContextHint{Reads: reads, Enumerations: enums},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	_, lines := tabLines(t, ctx, conn, tabKey)
	if got, want := lines[0]["servedAt"], "2026-07-22T12:15:00Z"; got != want {
		t.Fatalf("lines[0].servedAt = %v, want %q via the descriptor-declared reads", got, want)
	}
}
