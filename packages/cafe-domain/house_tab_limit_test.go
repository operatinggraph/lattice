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

// setCafePolicy submits SetCafePolicy{locationKey, tabLimitCents} as the
// operator, declaring the location in Reads and its .cafePolicy as the
// class-(d) OptionalRead every dispatcher declares (opmetas.go). Returns the
// outcome and the script's own failure message.
func setCafePolicy(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, locationKey string, tabLimitCents string) (processor.MessageOutcome, string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetCafePolicy",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-18T11:00:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"locationKey":"` + locationKey + `","tabLimitCents":` + tabLimitCents + `}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{locationKey},
			OptionalReads: []string{locationKey + ".cafePolicy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, msg
}

func mustSetCafePolicy(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, locationKey string, tabLimitCents int) {
	t.Helper()
	if got, msg := setCafePolicy(t, ctx, conn, cp, cons, label, locationKey, strconv.Itoa(tabLimitCents)); got != processor.OutcomeAccepted {
		t.Fatalf("%s: SetCafePolicy{%s, %d} = %v (%s), want Accepted", label, locationKey, tabLimitCents, got, msg)
	}
}

func policyLimit(t *testing.T, ctx context.Context, conn *substrate.Conn, locationKey string) (float64, uint64) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, locationKey+".cafePolicy")
	if err != nil {
		t.Fatalf("KVGet %s.cafePolicy: %v", locationKey, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s.cafePolicy: %v", locationKey, err)
	}
	if cls, _ := doc["class"].(string); cls != "cafeHousePolicy" {
		t.Fatalf("%s.cafePolicy class = %q, want cafeHousePolicy", locationKey, cls)
	}
	data, _ := doc["data"].(map[string]any)
	limit, _ := data["tabLimitCents"].(float64)
	return limit, entry.Revision
}

// selfOrderExpect submits Charge{tabKey, menuItemKey} as the resident on
// their own tab and returns the outcome + the script's failure message — the
// rejection sibling of line_served_test.go's selfOrder.
func selfOrderExpect(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, tabKey, itemKey, applicationForLnk string) (processor.MessageOutcome, string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-18T12:10:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","menuItemKey":"` + itemKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status", itemKey, itemKey + ".price"},
			OptionalReads: []string{applicationForLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, msg
}

// houseFixture seeds a resident with an approved lease at a unit inside a
// building, an open tab and a $4.50 menu item served at the building, and
// returns (tab, item, applicationFor link, unit key, building key).
func houseFixture(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	leaseID, unitID, buildingID, label string) (string, string, string, string, string) {
	t.Helper()
	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, leaseID, domainConsumerID)
	unitKey := seedLocation(t, ctx, conn, unitID)
	buildingKey := "vtx.building." + buildingID
	seedVertex(t, ctx, conn, buildingKey, "building", map[string]any{})
	testutil.SeedLink(t, ctx, conn, "lnk.unit."+unitID+".containedIn.building."+buildingID, "containedIn", unitKey, buildingKey)
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	tabKey := openTab(t, ctx, conn, cp, cons, label+"tab", leaseKey)
	itemKey := createMenuItem(t, ctx, conn, cp, cons, label+"itm", "Latte", 450, buildingKey)
	return tabKey, itemKey, "lnk.leaseapp." + leaseID + ".applicationFor.identity." + domainConsumerID, unitKey, buildingKey
}

// TestSetCafePolicy_MintsThenUpserts proves the class-(d) write: a location
// with no .cafePolicy gets one minted; a second SetCafePolicy on the same
// location OCC-upserts it at a later revision rather than CreateOnly-
// rejecting; the stored value is the new one.
func TestSetCafePolicy_MintsThenUpserts(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "setpolicymint")

	buildingKey := "vtx.building.BBCAFEHPQLBLDGHJKMNP"
	seedVertex(t, ctx, conn, buildingKey, "building", map[string]any{})

	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdsetpolicymint00001", buildingKey, 5000)
	limit, rev1 := policyLimit(t, ctx, conn, buildingKey)
	if limit != 5000 {
		t.Fatalf("minted tabLimitCents = %v, want 5000", limit)
	}
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdsetpolicymint00002", buildingKey, 0)
	limit, rev2 := policyLimit(t, ctx, conn, buildingKey)
	if limit != 0 {
		t.Fatalf("upserted tabLimitCents = %v, want 0", limit)
	}
	if rev2 <= rev1 {
		t.Fatalf("revision after upsert = %d, want > %d (an OCC upsert, not a no-op)", rev2, rev1)
	}
}

// TestSetCafePolicy_RejectsBadInput proves the value and location guards: a
// negative or fractional limit is InvalidArgument and a non-location key is
// NotALocation — neither writes an aspect. (An absent locationKey is a
// declared read, so it faults HydrationMiss before the script runs — the
// CreateMenuItem posture.)
func TestSetCafePolicy_RejectsBadInput(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "setpolicybad")

	buildingKey := "vtx.building.BBCAFEHPQLBADBHJKMNP"
	seedVertex(t, ctx, conn, buildingKey, "building", map[string]any{})
	seedIdentity(t, ctx, conn, "BBCAFEHPQLNQTLQCHJKM")

	for _, tc := range []struct{ label, location, limit, want string }{
		{"cdsetpolicybad000001", buildingKey, "-1", "InvalidArgument"},
		{"cdsetpolicybad000002", buildingKey, "12.5", "InvalidArgument"},
		{"cdsetpolicybad000003", "vtx.identity.BBCAFEHPQLNQTLQCHJKM", "500", "NotALocation"},
	} {
		got, msg := setCafePolicy(t, ctx, conn, cp, cons, tc.label, tc.location, tc.limit)
		if got != processor.OutcomeRejected || !strings.Contains(msg, tc.want) {
			t.Fatalf("%s: SetCafePolicy{%s, %s} = %v (%s), want Rejected with %s", tc.label, tc.location, tc.limit, got, msg, tc.want)
		}
	}
	if keyExists(t, ctx, conn, buildingKey+".cafePolicy") {
		t.Fatalf("a refused SetCafePolicy wrote %s.cafePolicy", buildingKey)
	}
}

// TestCharge_SelfRefusedOverHouseLimit is the green bar: with a $9.00 limit
// on the building, a resident's first two $4.50 orders land (the second at
// exactly the limit), the third is refused TabLimitExceeded and writes
// nothing, and the desk's off-menu Charge rings past the limit freely.
func TestCharge_SelfRefusedOverHouseLimit(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargelimit")

	tabKey, itemKey, appFor, _, buildingKey := houseFixture(t, ctx, conn, cp, cons,
		"BBCAFEHLJMLEASEHJKMN", "BBCAFEHLJMUNJTHJKMNP", "BBCAFEHLJMBLDGHJKMNP", "cdchargelimit0")
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdchargelimitpol0001", buildingKey, 900)

	selfOrder(t, ctx, conn, cp, cons, "cdchargelimitord0001", tabKey, itemKey, appFor, "2026-07-18T12:10:00Z")
	selfOrder(t, ctx, conn, cp, cons, "cdchargelimitord0002", tabKey, itemKey, appFor, "2026-07-18T12:11:00Z")
	if got := tabTotal(t, ctx, conn, tabKey); got != 900 {
		t.Fatalf("totalCents after two orders = %v, want 900 (exactly the limit is allowed)", got)
	}
	got, msg := selfOrderExpect(t, ctx, conn, cp, cons, "cdchargelimitord0003", tabKey, itemKey, appFor)
	if got != processor.OutcomeRejected || !strings.Contains(msg, "TabLimitExceeded") {
		t.Fatalf("third self-order = %v (%s), want Rejected with TabLimitExceeded", got, msg)
	}
	for _, want := range []string{"$9.00", "$4.50"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("TabLimitExceeded message %q should name %s", msg, want)
		}
	}
	if got := tabTotal(t, ctx, conn, tabKey); got != 900 {
		t.Fatalf("totalCents after the refused order = %v, want 900 (nothing written)", got)
	}

	staffCharge(t, ctx, conn, cp, cons, "cdchargelimitstf0001", tabKey, 500, "2026-07-18T12:12:00Z")
	if got := tabTotal(t, ctx, conn, tabKey); got != 1400 {
		t.Fatalf("totalCents after the desk's charge = %v, want 1400 (the staff leg is never limited)", got)
	}
}

// TestCharge_SelfHouseLimit_TightestOnChain proves the composition rule: a
// $20 building policy under a $9 property policy binds at $9 — the tightest
// limit on the unit's containedIn chain, whichever level records it.
func TestCharge_SelfHouseLimit_TightestOnChain(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargelimitchain")

	tabKey, itemKey, appFor, _, buildingKey := houseFixture(t, ctx, conn, cp, cons,
		"BBCAFEHCHNLEASEHJKMN", "BBCAFEHCHNUNJTHJKMNP", "BBCAFEHCHNBLDGHJKMNP", "cdchargechain0")
	propertyKey := "vtx.property.BBCAFEHCHNPRQPHJKMNP"
	seedVertex(t, ctx, conn, propertyKey, "property", map[string]any{})
	testutil.SeedLink(t, ctx, conn, "lnk.building.BBCAFEHCHNBLDGHJKMNP.containedIn.property.BBCAFEHCHNPRQPHJKMNP", "containedIn", buildingKey, propertyKey)
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdchargechainpol0001", buildingKey, 2000)
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdchargechainpol0002", propertyKey, 900)

	selfOrder(t, ctx, conn, cp, cons, "cdchargechainord0001", tabKey, itemKey, appFor, "2026-07-18T12:10:00Z")
	selfOrder(t, ctx, conn, cp, cons, "cdchargechainord0002", tabKey, itemKey, appFor, "2026-07-18T12:11:00Z")
	got, msg := selfOrderExpect(t, ctx, conn, cp, cons, "cdchargechainord0003", tabKey, itemKey, appFor)
	if got != processor.OutcomeRejected || !strings.Contains(msg, "TabLimitExceeded") || !strings.Contains(msg, "$9.00") {
		t.Fatalf("third self-order under a $9 property policy = %v (%s), want Rejected with TabLimitExceeded naming $9.00", got, msg)
	}
}

// TestCharge_SelfNoPolicyOnChain_Unlimited proves the absent case: a chain
// recording no .cafePolicy has no limit, so the pre-policy behaviour holds.
func TestCharge_SelfNoPolicyOnChain_Unlimited(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargenolimit")

	tabKey, itemKey, appFor, _, _ := houseFixture(t, ctx, conn, cp, cons,
		"BBCAFEHNQPLEASEHJKMN", "BBCAFEHNQPUNJTHJKMNP", "BBCAFEHNQPBLDGHJKMNP", "cdchargenopol0")
	for i := 1; i <= 4; i++ {
		selfOrder(t, ctx, conn, cp, cons, "cdchargenopolord000"+strconv.Itoa(i), tabKey, itemKey, appFor, "2026-07-18T12:1"+strconv.Itoa(i)+":00Z")
	}
	if got := tabTotal(t, ctx, conn, tabKey); got != 1800 {
		t.Fatalf("totalCents with no policy on the chain = %v, want 1800", got)
	}
}

// TestOpenTab_SelfRefusedWhenHouseClosed proves a $0 limit closes
// self-service tabs: the resident's own OpenTab is refused TabLimitExceeded
// and mints nothing, while the desk opens the same lease's tab.
func TestOpenTab_SelfRefusedWhenHouseClosed(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "opentabclosed")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseID := "BBCAFEHCLSLEASEHJKMN"
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, leaseID, domainConsumerID)
	unitKey := seedLocation(t, ctx, conn, "BBCAFEHCLSUNJTHJKMNP")
	buildingKey := "vtx.building.BBCAFEHCLSBLDGHJKMNP"
	seedVertex(t, ctx, conn, buildingKey, "building", map[string]any{})
	testutil.SeedLink(t, ctx, conn, "lnk.unit.BBCAFEHCLSUNJTHJKMNP.containedIn.building.BBCAFEHCLSBLDGHJKMNP", "containedIn", unitKey, buildingKey)
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdopentabclosedpol01", buildingKey, 0)

	applicationForLnk := "lnk.leaseapp." + leaseID + ".applicationFor.identity." + domainConsumerID
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdopentabclosedslf01"),
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseKey},
			OptionalReads: []string{leaseKey + ".cafeOpenTab", applicationForLnk, leaseKey + ".decision", leaseKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: leaseKey, Relation: "heldFor", Direction: "in"},
			},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "TabLimitExceeded") {
		t.Fatalf("self-service OpenTab at a closed house = %v (%+v), want Rejected with TabLimitExceeded", outcome, reply.Error)
	}
	if keyExists(t, ctx, conn, leaseKey+".cafeOpenTab") {
		t.Fatalf("a refused self OpenTab claimed the lease's open-tab guard")
	}

	openTabAcceptedFor(t, ctx, conn, cp, cons, "cdopentabclosedstf01", leaseKey, "the desk opens a tab at a closed house; only self-service is closed")
}

func tabTotal(t *testing.T, ctx context.Context, conn *substrate.Conn, tabKey string) float64 {
	t.Helper()
	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	got, _ := statusData["totalCents"].(float64)
	return got
}

// selfOpenTabExpect submits OpenTab as the resident on their own lease and
// returns the outcome + the script's failure message.
func selfOpenTabExpect(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, leaseKey, leaseID string) (processor.MessageOutcome, string) {
	t.Helper()
	applicationForLnk := "lnk.leaseapp." + leaseID + ".applicationFor.identity." + domainConsumerID
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseKey},
			OptionalReads: []string{leaseKey + ".cafeOpenTab", applicationForLnk, leaseKey + ".decision", leaseKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: leaseKey, Relation: "heldFor", Direction: "in"},
			},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, msg
}

// TestOpenTab_SelfAcceptedUnderPositiveLimit is the positive sibling of the
// closed-house vector: a resident's own OpenTab under a positive limit —
// even one recorded at the property above a building on the chain — is
// accepted; only $0 closes the house. It also proves the self leg's walk
// reaches the property level (the read-drift baseline row for it).
func TestOpenTab_SelfAcceptedUnderPositiveLimit(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "opentabpositive")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseID := "BBCAFEHPQSLEASEHJKMN"
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, leaseID, domainConsumerID)
	unitKey := seedLocation(t, ctx, conn, "BBCAFEHPQSUNJTHJKMNP")
	buildingKey := "vtx.building.BBCAFEHPQSBLDGHJKMNP"
	propertyKey := "vtx.property.BBCAFEHPQSPRQPHJKMNP"
	seedVertex(t, ctx, conn, buildingKey, "building", map[string]any{})
	seedVertex(t, ctx, conn, propertyKey, "property", map[string]any{})
	testutil.SeedLink(t, ctx, conn, "lnk.unit.BBCAFEHPQSUNJTHJKMNP.containedIn.building.BBCAFEHPQSBLDGHJKMNP", "containedIn", unitKey, buildingKey)
	testutil.SeedLink(t, ctx, conn, "lnk.building.BBCAFEHPQSBLDGHJKMNP.containedIn.property.BBCAFEHPQSPRQPHJKMNP", "containedIn", buildingKey, propertyKey)
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdopentabpospol00001", propertyKey, 100)

	if got, msg := selfOpenTabExpect(t, ctx, conn, cp, cons, "cdopentabposslf00001", leaseKey, leaseID); got != processor.OutcomeAccepted {
		t.Fatalf("self OpenTab under a $1.00 property limit = %v (%s), want Accepted (only $0 closes the house)", got, msg)
	}
}

// TestCharge_SelfHouseLimit_DegenerateChainValues pins how house_tab_limit
// reads the chain's degenerate inputs, each toward the pre-policy behaviour
// (no limit) rather than toward denial: a .cafePolicy of a foreign class is
// not a policy (the lens's own class filter), a malformed value is skipped,
// and a property policy above a TOMBSTONED building is unreachable — the
// walk does not pass through a dead node, the cut location_covers and
// cafeLeaseWorkplaces make too. A $9 limit is then recorded on the unit
// itself to prove the same order is refused once a valid policy binds.
func TestCharge_SelfHouseLimit_DegenerateChainValues(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargedegenerate")

	tabKey, itemKey, appFor, unitKey, buildingKey := houseFixture(t, ctx, conn, cp, cons,
		"BBCAFEHDGNLEASEHJKMN", "BBCAFEHDGNUNJTHJKMNP", "BBCAFEHDGNBLDGHJKMNP", "cdchargedegen0")
	propertyKey := "vtx.property.BBCAFEHDGNPRQPHJKMNP"
	seedVertex(t, ctx, conn, propertyKey, "property", map[string]any{})
	testutil.SeedLink(t, ctx, conn, "lnk.building.BBCAFEHDGNBLDGHJKMNP.containedIn.property.BBCAFEHDGNPRQPHJKMNP", "containedIn", buildingKey, propertyKey)
	// A $1 cap at the property that a live chain would bind on.
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdchargedegenpol0001", propertyKey, 100)
	// The building carries a foreign-class .cafePolicy with a tiny limit.
	seedAspect(t, ctx, conn, buildingKey, "cafePolicy", "somethingElse", map[string]any{"tabLimitCents": 100})
	// The unit carries a malformed policy value.
	seedAspect(t, ctx, conn, unitKey, "cafePolicy", "cafeHousePolicy", map[string]any{"tabLimitCents": "one hundred"})
	// The building is tombstoned: the property above it is no longer on the chain.
	doc := map[string]any{"class": "building", "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, buildingKey, b); err != nil {
		t.Fatalf("tombstone building: %v", err)
	}
	// The item must still be reachable: re-serve it at the unit itself.
	itemAtUnit := createMenuItem(t, ctx, conn, cp, cons, "cdchargedegenitm0002", "Latte", 450, unitKey)
	_ = itemKey

	selfOrder(t, ctx, conn, cp, cons, "cdchargedegenord0001", tabKey, itemAtUnit, appFor, "2026-07-18T12:10:00Z")
	selfOrder(t, ctx, conn, cp, cons, "cdchargedegenord0002", tabKey, itemAtUnit, appFor, "2026-07-18T12:11:00Z")
	if got := tabTotal(t, ctx, conn, tabKey); got != 900 {
		t.Fatalf("totalCents = %v, want 900 (a foreign-class aspect, a malformed value and a policy beyond a tombstoned node all read as no limit)", got)
	}

	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdchargedegenpol0002", unitKey, 900)
	got, msg := selfOrderExpect(t, ctx, conn, cp, cons, "cdchargedegenord0003", tabKey, itemAtUnit, appFor)
	if got != processor.OutcomeRejected || !strings.Contains(msg, "TabLimitExceeded") {
		t.Fatalf("third self-order under a repaired $9.00 unit policy = %v (%s), want Rejected with TabLimitExceeded (SetCafePolicy on a unit binds)", got, msg)
	}
}

// seedAccountBalance seeds the cafe-ledger .balance aspect on a café account
// the way CreateAccount mints it and the entry ops carry it — the recorded
// figure house_exposure_balance reads.
func seedAccountBalance(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey string, balanceCents int) {
	t.Helper()
	seedAspect(t, ctx, conn, acctKey, "balance", "cafeAccountBalance", map[string]any{"balanceCents": balanceCents, "cashCents": 0})
}

// TestCharge_SelfRefusedOverExposure is the exposure green bar: the house
// limit bounds the resident's open exposure, the recorded ledger balance plus
// the tab. With a $10.00 limit and $6.00 already owed, a $4.00 self-order
// lands (exactly the limit) and a $4.50 one is refused TabLimitExceeded
// naming the limit, the balance and the item; the desk rings past it.
func TestCharge_SelfRefusedOverExposure(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargeexpo")

	tabKey, itemKey, appFor, _, buildingKey := houseFixture(t, ctx, conn, cp, cons,
		"BBCAFEHXPLEASEHJKMNP", "BBCAFEHXPUNJTHJKMNPQ", "BBCAFEHXPBLDGHJKMNPQ", "cdchargeexpo00")
	leaseKey := "vtx.leaseapp.BBCAFEHXPLEASEHJKMNP"
	acctKey := seedCafeAccount(t, ctx, conn, "BBCAFEHXPACCTHJKMNPQ", leaseKey)
	seedAccountBalance(t, ctx, conn, acctKey, 600)
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdchargeexpopol00001", buildingKey, 1000)
	fourDollarItem := createMenuItem(t, ctx, conn, cp, cons, "cdchargeexpoitm00002", "Scone", 400, buildingKey)

	got, msg := selfOrderExpect(t, ctx, conn, cp, cons, "cdchargeexpoord00001", tabKey, itemKey, appFor)
	if got != processor.OutcomeRejected || !strings.Contains(msg, "TabLimitExceeded") {
		t.Fatalf("a $4.50 self-order over a $6.00 balance under a $10.00 limit = %v (%s), want Rejected with TabLimitExceeded", got, msg)
	}
	for _, want := range []string{"$10.00", "$6.00", "$4.50"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("TabLimitExceeded message %q should name %s", msg, want)
		}
	}
	if got := tabTotal(t, ctx, conn, tabKey); got != 0 {
		t.Fatalf("totalCents after the refused order = %v, want 0 (nothing written)", got)
	}

	selfOrder(t, ctx, conn, cp, cons, "cdchargeexpoord00002", tabKey, fourDollarItem, appFor, "2026-07-18T12:11:00Z")
	if got := tabTotal(t, ctx, conn, tabKey); got != 400 {
		t.Fatalf("totalCents after the $4.00 order = %v, want 400 (balance + tab exactly at the limit is allowed)", got)
	}
	got, msg = selfOrderExpect(t, ctx, conn, cp, cons, "cdchargeexpoord00003", tabKey, fourDollarItem, appFor)
	if got != processor.OutcomeRejected || !strings.Contains(msg, "TabLimitExceeded") {
		t.Fatalf("a second $4.00 self-order at the limit = %v (%s), want Rejected with TabLimitExceeded", got, msg)
	}

	staffCharge(t, ctx, conn, cp, cons, "cdchargeexpostf00001", tabKey, 500, "2026-07-18T12:12:00Z")
	if got := tabTotal(t, ctx, conn, tabKey); got != 900 {
		t.Fatalf("totalCents after the desk's charge = %v, want 900 (the staff leg is never limited)", got)
	}
}

// TestCharge_SelfCreditWidensRoom proves the balance is read signed: a
// $2.50 credit under an $11.00 limit admits three $4.50 self-orders (the
// third landing at exactly the limit net of the credit), where a zero
// balance would refuse the third.
func TestCharge_SelfCreditWidensRoom(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargecredit")

	tabKey, itemKey, appFor, _, buildingKey := houseFixture(t, ctx, conn, cp, cons,
		"BBCAFEHCRLEASEHJKMNP", "BBCAFEHCRUNJTHJKMNPQ", "BBCAFEHCRBLDGHJKMNPQ", "cdchargecredit")
	leaseKey := "vtx.leaseapp.BBCAFEHCRLEASEHJKMNP"
	acctKey := seedCafeAccount(t, ctx, conn, "BBCAFEHCRACCTHJKMNPQ", leaseKey)
	seedAccountBalance(t, ctx, conn, acctKey, -250)
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdchargecreditpol001", buildingKey, 1100)

	selfOrder(t, ctx, conn, cp, cons, "cdchargecreditord001", tabKey, itemKey, appFor, "2026-07-18T12:10:00Z")
	selfOrder(t, ctx, conn, cp, cons, "cdchargecreditord002", tabKey, itemKey, appFor, "2026-07-18T12:11:00Z")
	selfOrder(t, ctx, conn, cp, cons, "cdchargecreditord003", tabKey, itemKey, appFor, "2026-07-18T12:12:00Z")
	if got := tabTotal(t, ctx, conn, tabKey); got != 1350 {
		t.Fatalf("totalCents after three orders = %v, want 1350 (a $2.50 credit widens the $11.00 room to $13.50)", got)
	}
	got, msg := selfOrderExpect(t, ctx, conn, cp, cons, "cdchargecreditord004", tabKey, itemKey, appFor)
	if got != processor.OutcomeRejected || !strings.Contains(msg, "TabLimitExceeded") {
		t.Fatalf("fourth self-order = %v (%s), want Rejected with TabLimitExceeded", got, msg)
	}
}

// TestCharge_SelfHouseLimit_LegacyAccountReadsZero proves an account that
// carries no live .balance (minted before the aspect existed) bounds only
// the tab, exactly as a lease with no account: the exposure reads 0.
func TestCharge_SelfHouseLimit_LegacyAccountReadsZero(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargelegacy")

	tabKey, itemKey, appFor, _, buildingKey := houseFixture(t, ctx, conn, cp, cons,
		"BBCAFEHLGLEASEHJKMNP", "BBCAFEHLGUNJTHJKMNPQ", "BBCAFEHLGBLDGHJKMNPQ", "cdchargelegacy")
	seedCafeAccount(t, ctx, conn, "BBCAFEHLGACCTHJKMNPQ", "vtx.leaseapp.BBCAFEHLGLEASEHJKMNP")
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdchargelegacypol001", buildingKey, 900)

	selfOrder(t, ctx, conn, cp, cons, "cdchargelegacyord001", tabKey, itemKey, appFor, "2026-07-18T12:10:00Z")
	selfOrder(t, ctx, conn, cp, cons, "cdchargelegacyord002", tabKey, itemKey, appFor, "2026-07-18T12:11:00Z")
	if got := tabTotal(t, ctx, conn, tabKey); got != 900 {
		t.Fatalf("totalCents after two orders = %v, want 900 (no .balance reads as nothing owed)", got)
	}
	got, msg := selfOrderExpect(t, ctx, conn, cp, cons, "cdchargelegacyord003", tabKey, itemKey, appFor)
	if got != processor.OutcomeRejected || !strings.Contains(msg, "TabLimitExceeded") {
		t.Fatalf("third self-order = %v (%s), want Rejected with TabLimitExceeded", got, msg)
	}
}

// TestOpenTab_SelfRefusedWhenBalanceAtLimit proves the OpenTab half of the
// exposure bound: a balance already at a $10.00 limit refuses the resident's
// own OpenTab (a tab no line could join) and mints nothing, the desk opens
// the same lease's tab, and a balance one cent under the limit lets the
// resident open one.
func TestOpenTab_SelfRefusedWhenBalanceAtLimit(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "opentabexpo")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseID := "BBCAFEHVXLEASEHJKMNP"
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, leaseID, domainConsumerID)
	unitKey := seedLocation(t, ctx, conn, "BBCAFEHVXUNJTHJKMNPQ")
	buildingKey := "vtx.building.BBCAFEHVXBLDGHJKMNPQ"
	seedVertex(t, ctx, conn, buildingKey, "building", map[string]any{})
	testutil.SeedLink(t, ctx, conn, "lnk.unit.BBCAFEHVXUNJTHJKMNPQ.containedIn.building.BBCAFEHVXBLDGHJKMNPQ", "containedIn", unitKey, buildingKey)
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	acctKey := seedCafeAccount(t, ctx, conn, "BBCAFEHVXACCTHJKMNPQ", leaseKey)
	seedAccountBalance(t, ctx, conn, acctKey, 1000)
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdopentabexpopol0001", buildingKey, 1000)

	applicationForLnk := "lnk.leaseapp." + leaseID + ".applicationFor.identity." + domainConsumerID
	selfOpen := func(label string) (processor.MessageOutcome, *processor.OperationReply) {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "OpenTab",
			Actor:         domainConsumerKey,
			SubmittedAt:   "2026-07-07T12:00:00Z",
			Class:         "tab",
			Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
			ContextHint: &processor.ContextHint{
				Reads:         []string{leaseKey},
				OptionalReads: []string{leaseKey + ".cafeOpenTab", applicationForLnk, leaseKey + ".decision", leaseKey + ".tenancy"},
				Enumerations: []processor.EnumerationHint{
					{Hub: leaseKey, Relation: "heldFor", Direction: "in"},
				},
			},
			AuthContext: &processor.AuthContext{Target: domainConsumerKey},
		}
		return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	}
	outcome, reply := selfOpen("cdopentabexposlf0001")
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "TabLimitExceeded") {
		t.Fatalf("self OpenTab with the balance at the limit = %v (%+v), want Rejected with TabLimitExceeded", outcome, reply.Error)
	}
	for _, want := range []string{"$10.00"} {
		if !strings.Contains(reply.Error.Message, want) {
			t.Fatalf("TabLimitExceeded message %q should name %s", reply.Error.Message, want)
		}
	}
	if keyExists(t, ctx, conn, leaseKey+".cafeOpenTab") {
		t.Fatalf("a refused self OpenTab claimed the lease's open-tab guard")
	}

	seedAccountBalance(t, ctx, conn, acctKey, 999)
	outcome, reply = selfOpen("cdopentabexposlf0002")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self OpenTab one cent under the limit = %v (%+v), want Accepted", outcome, reply.Error)
	}
}

// TestOpenTab_StaffOpensPastBalanceLimit proves the staff leg of the
// OpenTab exposure bound is unrestricted: with the balance at the limit the
// desk opens the lease's tab.
func TestOpenTab_StaffOpensPastBalanceLimit(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "opentabexpostf")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseID := "BBCAFEHVSLEASEHJKMNP"
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, leaseID, domainConsumerID)
	unitKey := seedLocation(t, ctx, conn, "BBCAFEHVSUNJTHJKMNPQ")
	buildingKey := "vtx.building.BBCAFEHVSBLDGHJKMNPQ"
	seedVertex(t, ctx, conn, buildingKey, "building", map[string]any{})
	testutil.SeedLink(t, ctx, conn, "lnk.unit.BBCAFEHVSUNJTHJKMNPQ.containedIn.building.BBCAFEHVSBLDGHJKMNPQ", "containedIn", unitKey, buildingKey)
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	acctKey := seedCafeAccount(t, ctx, conn, "BBCAFEHVSACCTHJKMNPQ", leaseKey)
	seedAccountBalance(t, ctx, conn, acctKey, 5000)
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdopentabexpostfpol1", buildingKey, 1000)

	openTabAcceptedFor(t, ctx, conn, cp, cons, "cdopentabexpostf0001", leaseKey, "the desk opens a tab over a balance past the limit; only self-service is bounded")
}

// TestCharge_SelfHouseLimit_TombstonedBalanceReadsZero proves a tombstoned
// .balance is read as no recorded balance, exactly as an absent one: under a
// $9.00 limit two $4.50 self-orders land regardless of the figure the dead
// document carries.
func TestCharge_SelfHouseLimit_TombstonedBalanceReadsZero(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargetomb")

	tabKey, itemKey, appFor, _, buildingKey := houseFixture(t, ctx, conn, cp, cons,
		"BBCAFEHTBLEASEHJKMNP", "BBCAFEHTBUNJTHJKMNPQ", "BBCAFEHTBBLDGHJKMNPQ", "cdchargetomb00")
	acctKey := seedCafeAccount(t, ctx, conn, "BBCAFEHTBACCTHJKMNPQ", "vtx.leaseapp.BBCAFEHTBLEASEHJKMNP")
	doc, _ := json.Marshal(map[string]any{
		"class": "cafeAccountBalance", "isDeleted": true,
		"vertexKey": acctKey, "localName": "balance", "data": map[string]any{"balanceCents": 5000, "cashCents": 0},
	})
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, acctKey+".balance", doc); err != nil {
		t.Fatalf("seed tombstoned .balance: %v", err)
	}
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdchargetombpol00001", buildingKey, 900)

	selfOrder(t, ctx, conn, cp, cons, "cdchargetombord00001", tabKey, itemKey, appFor, "2026-07-18T12:10:00Z")
	selfOrder(t, ctx, conn, cp, cons, "cdchargetombord00002", tabKey, itemKey, appFor, "2026-07-18T12:11:00Z")
	if got := tabTotal(t, ctx, conn, tabKey); got != 900 {
		t.Fatalf("totalCents after two orders = %v, want 900 (a tombstoned .balance reads as nothing owed)", got)
	}
}

// TestCharge_SelfHouseLimit_ForeignBalanceClassRefused proves the class
// check: a live .balance document of any class other than cafeAccountBalance
// is a data-integrity fault the self-order refuses InvalidState, writing
// nothing, rather than a figure to bound on — or to ignore.
func TestCharge_SelfHouseLimit_ForeignBalanceClassRefused(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargeclass")

	tabKey, itemKey, appFor, _, buildingKey := houseFixture(t, ctx, conn, cp, cons,
		"BBCAFEHFCLEASEHJKMNP", "BBCAFEHFCUNJTHJKMNPQ", "BBCAFEHFCBLDGHJKMNPQ", "cdchargeclass0")
	acctKey := seedCafeAccount(t, ctx, conn, "BBCAFEHFCACCTHJKMNPQ", "vtx.leaseapp.BBCAFEHFCLEASEHJKMNP")
	seedAspect(t, ctx, conn, acctKey, "balance", "somethingElse", map[string]any{"balanceCents": 0})
	mustSetCafePolicy(t, ctx, conn, cp, cons, "cdchargeclasspol0001", buildingKey, 900)

	got, msg := selfOrderExpect(t, ctx, conn, cp, cons, "cdchargeclassord0001", tabKey, itemKey, appFor)
	if got != processor.OutcomeRejected || !strings.Contains(msg, "InvalidState") {
		t.Fatalf("self-order over a foreign-class .balance = %v (%s), want Rejected with InvalidState", got, msg)
	}
	if got := tabTotal(t, ctx, conn, tabKey); got != 0 {
		t.Fatalf("totalCents after the refused order = %v, want 0 (nothing written)", got)
	}
}
