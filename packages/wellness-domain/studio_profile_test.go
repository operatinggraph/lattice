// The studio's recorded no-show policy, studioProfile.noShowFeeCents, at its
// two writers: CreateStudio's optional param and SetStudioProfile, the
// merge-edit of a live studio's .profile under TombstoneStudio's grant and
// locatedAt confinement. Each confinement case leads with the positive sibling
// — the same edit at the staffer's OWN building, which must be Accepted — so a
// Rejected is the guard talking and not a broken path. The reader's half (what
// SetBookingAttendance bills off the policy) lives in noshow_fee_test.go.
package wellnessdomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// createStudioWith submits CreateStudio as the operator with an arbitrary
// payload (the name plus whatever extra fields the case supplies) and returns
// the minted key, the outcome, and the rejection's message ("" when accepted).
func createStudioWith(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label string, payloadMap map[string]any) (string, processor.MessageOutcome, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload, _ := json.Marshal(payloadMap)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateStudio",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "studio",
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("CreateStudio", domainActorKey, wellnessdomain.OpMetas())},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	why := ""
	if reply != nil && reply.Error != nil {
		why = reply.Error.Message
	}
	return "vtx.studio." + nanoIDFromRequestID(reqID), outcome, why
}

// setStudioProfileAs submits SetStudioProfile as an arbitrary actor, declaring
// what the descriptor declares: the studio and its .profile as required reads,
// the confinement's payload-hubbed locatedAt walk, and the actor-hubbed
// operator probe. fields is the payload beyond studioKey.
func setStudioProfileAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, studioKey, actorKey string, fields map[string]any) (processor.MessageOutcome, string) {
	t.Helper()
	payloadMap := map[string]any{"studioKey": studioKey}
	for k, v := range fields {
		payloadMap[k] = v
	}
	payload, _ := json.Marshal(payloadMap)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetStudioProfile",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "studio",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads: []string{studioKey, studioKey + ".profile"},
			Enumerations: append(testutil.DeclaredEnumerations("SetStudioProfile", actorKey, wellnessdomain.OpMetas()),
				processor.EnumerationHint{Hub: studioKey, Relation: "locatedAt", Direction: "out"}),
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	why := ""
	if reply != nil && reply.Error != nil {
		why = reply.Error.Message
	}
	return outcome, why
}

// studioProfile reads the studio's .profile data as stored.
func studioProfile(t *testing.T, ctx context.Context, conn *substrate.Conn, studioKey string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, studioKey+".profile")
	data, _ := doc["data"].(map[string]any)
	return data
}

// spStaffCapDoc is the front-of-house cap doc with SetStudioProfile's own
// scope=any row added — the grant the script must confine, since scope=any
// carries no platform-checked target.
func spStaffCapDoc() *processor.CapabilityDoc {
	capDoc := wcStaffCapDoc()
	capDoc.PlatformPermissions = append(capDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "SetStudioProfile", Scope: "any"})
	return capDoc
}

// TestCreateStudio_RecordsNoShowFee: the policy lands on .profile beside the
// name when supplied, including an explicit 0 (fee-free IS a policy); a studio
// created without one carries no field at all (no policy recorded).
func TestCreateStudio_RecordsNoShowFee(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdcsfee")

	withFee, outcome, why := createStudioWith(t, ctx, conn, cp, cons, "wdcsfeestudioa000001",
		map[string]any{"name": "Flow Room", "noShowFeeCents": 1000})
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateStudio with noShowFeeCents 1000 = %v (%s), want Accepted", outcome, why)
	}
	profile := studioProfile(t, ctx, conn, withFee)
	if profile["name"] != "Flow Room" || profile["noShowFeeCents"] != 1000.0 {
		t.Fatalf("profile = %v, want {name: Flow Room, noShowFeeCents: 1000}", profile)
	}

	free, outcome, why := createStudioWith(t, ctx, conn, cp, cons, "wdcsfeestudiob000001",
		map[string]any{"name": "Free Room", "noShowFeeCents": 0})
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateStudio with noShowFeeCents 0 = %v (%s), want Accepted — fee-free is a policy", outcome, why)
	}
	if got, present := studioProfile(t, ctx, conn, free)["noShowFeeCents"]; !present || got != 0.0 {
		t.Fatalf("profile.noShowFeeCents = %v (present=%v), want a recorded 0", got, present)
	}

	none, outcome, why := createStudioWith(t, ctx, conn, cp, cons, "wdcsfeestudioc000001",
		map[string]any{"name": "Quiet Room"})
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateStudio with no fee = %v (%s), want Accepted", outcome, why)
	}
	if got, present := studioProfile(t, ctx, conn, none)["noShowFeeCents"]; present {
		t.Fatalf("profile.noShowFeeCents = %v, want absent — no policy recorded", got)
	}
}

// TestCreateStudio_MalformedNoShowFeeRefused: the value is validated at the
// mint, since SetBookingAttendance derives a member's charge from it — a
// negative, fractional, string or boolean amount is refused InvalidArgument and
// no studio is minted.
func TestCreateStudio_MalformedNoShowFeeRefused(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdcsbadfee")

	cases := []struct {
		name string
		fee  any
	}{
		{"negative", -1},
		{"fractional", 12.5},
		{"string", "twenty"},
		{"boolean", true},
	}
	for i, tc := range cases {
		label := "wdcsbadfeestudio" + string(rune('a'+i)) + "0001"
		studio, outcome, why := createStudioWith(t, ctx, conn, cp, cons, label,
			map[string]any{"name": "Bad Room", "noShowFeeCents": tc.fee})
		if outcome != processor.OutcomeRejected {
			t.Fatalf("CreateStudio with a %s noShowFeeCents = %v, want Rejected", tc.name, outcome)
		}
		if !strings.Contains(why, "InvalidArgument: noShowFeeCents") {
			t.Errorf("%s: refused with %q, want the mint's own InvalidArgument: noShowFeeCents", tc.name, why)
		}
		if keyExists(t, ctx, conn, studio) {
			t.Errorf("%s: the refused CreateStudio minted %s", tc.name, studio)
		}
	}
}

// TestSetStudioProfile_FrontOfHouseAtTheStudiosBuilding: a staffer edits the
// policy of a studio at their own building; the name they did not send is
// carried forward off the declared profile read, and the event fires.
func TestSetStudioProfile_FrontOfHouseAtTheStudiosBuilding(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdspown")
	wcSeedStaff(t, ctx, conn) // staff A worksAt building A; buildings A + B exist
	testutil.SeedCapDoc(t, ctx, conn, spStaffCapDoc())

	studio := createStudio(t, ctx, conn, cp, cons, "wdspownstudioa000001", "Studio A")
	wfSeedStudioAt(t, ctx, conn, studio, wcBuildingAKey, wcBuildingAID)

	label := "wdspownedita00000001"
	got, why := setStudioProfileAs(t, ctx, conn, cp, cons, label, studio, wcStaffKey,
		map[string]any{"noShowFeeCents": 1000})
	if got != processor.OutcomeAccepted {
		t.Fatalf("staff SetStudioProfile at its OWN building = %v (%s), want Accepted "+
			"(the positive sibling — if this fails the negatives prove nothing)", got, why)
	}
	profile := studioProfile(t, ctx, conn, studio)
	if profile["name"] != "Studio A" {
		t.Errorf("profile.name = %v, want Studio A carried forward — the edit named no name", profile["name"])
	}
	if profile["noShowFeeCents"] != 1000.0 {
		t.Errorf("profile.noShowFeeCents = %v, want 1000", profile["noShowFeeCents"])
	}
	assertTrackerEvent(t, ctx, conn, testutil.GenReqID(label), "wellness.studioProfileSet")

	// A second edit that renames keeps the policy the first one recorded.
	if got, why := setStudioProfileAs(t, ctx, conn, cp, cons, "wdspownedita00000002", studio, wcStaffKey,
		map[string]any{"name": "Studio A Prime"}); got != processor.OutcomeAccepted {
		t.Fatalf("staff rename = %v (%s), want Accepted", got, why)
	}
	profile = studioProfile(t, ctx, conn, studio)
	if profile["name"] != "Studio A Prime" || profile["noShowFeeCents"] != 1000.0 {
		t.Errorf("profile after rename = %v, want {name: Studio A Prime, noShowFeeCents: 1000}", profile)
	}
}

// TestSetStudioProfile_FrontOfHouseElsewhereRefused: a studio at another
// building is denied by the confinement guard, before any mutation; an
// unlocated studio (empty candidate list) is denied too, never fallen open.
func TestSetStudioProfile_FrontOfHouseElsewhereRefused(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdspelse")
	wcSeedStaff(t, ctx, conn) // staff A worksAt building A; buildings A + B exist
	testutil.SeedCapDoc(t, ctx, conn, spStaffCapDoc())

	studioB := createStudio(t, ctx, conn, cp, cons, "wdspelsestudiob000001", "Studio B")
	wfSeedStudioAt(t, ctx, conn, studioB, wcBuildingBKey, wcBuildingBID)
	got, why := setStudioProfileAs(t, ctx, conn, cp, cons, "wdspelseeditb00000001", studioB, wcStaffKey,
		map[string]any{"noShowFeeCents": 1000})
	if got != processor.OutcomeRejected {
		t.Fatalf("staff SetStudioProfile at ANOTHER building = %v, want Rejected", got)
	}
	if !strings.Contains(why, "does not worksAt") {
		t.Errorf("refused with %q, want the confinement guard's message", why)
	}
	if got, present := studioProfile(t, ctx, conn, studioB)["noShowFeeCents"]; present {
		t.Errorf("the denied edit wrote noShowFeeCents = %v; it must be denied before any mutation", got)
	}

	placeless := createStudio(t, ctx, conn, cp, cons, "wdspelsestudion000001", "Nowhere Studio")
	got, why = setStudioProfileAs(t, ctx, conn, cp, cons, "wdspelseeditn00000001", placeless, wcStaffKey,
		map[string]any{"noShowFeeCents": 1000})
	if got != processor.OutcomeRejected {
		t.Fatalf("staff SetStudioProfile on a studio with NO location = %v, want Rejected — an empty "+
			"candidate list must deny, not fall open", got)
	}
	if !strings.Contains(why, "does not worksAt") {
		t.Errorf("refused with %q, want the confinement guard's message", why)
	}
}

// TestSetStudioProfile_OperatorAccepted: the operator is workplace-exempt and
// edits an unlocated studio; both fields land in one edit; a payload naming
// neither field is refused rather than committing a no-op upsert.
func TestSetStudioProfile_OperatorAccepted(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdspoper")

	studio := createStudio(t, ctx, conn, cp, cons, "wdspoperstudioa000001", "Nowhere Studio")
	got, why := setStudioProfileAs(t, ctx, conn, cp, cons, "wdspoperedit1a000001", studio, domainActorKey,
		map[string]any{"name": "Somewhere Studio", "noShowFeeCents": 0})
	if got != processor.OutcomeAccepted {
		t.Fatalf("operator SetStudioProfile on an unlocated studio = %v (%s), want Accepted — the "+
			"operator is workplace-exempt; only the staff path is confined", got, why)
	}
	profile := studioProfile(t, ctx, conn, studio)
	if profile["name"] != "Somewhere Studio" || profile["noShowFeeCents"] != 0.0 {
		t.Errorf("profile = %v, want {name: Somewhere Studio, noShowFeeCents: 0}", profile)
	}

	got, why = setStudioProfileAs(t, ctx, conn, cp, cons, "wdspoperedit2a000001", studio, domainActorKey,
		map[string]any{})
	if got != processor.OutcomeRejected || !strings.Contains(why, "InvalidArgument: at least one of name, noShowFeeCents") {
		t.Errorf("SetStudioProfile naming neither field = %v (%s), want Rejected InvalidArgument", got, why)
	}
}

// TestSetStudioProfile_NegativeFeeRefused: the same mint-time validation
// CreateStudio applies — negative, fractional, string and boolean amounts are
// refused InvalidArgument and the stored profile is untouched.
func TestSetStudioProfile_NegativeFeeRefused(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdspneg")

	studio, outcome, why := createStudioWith(t, ctx, conn, cp, cons, "wdspnegstudioa000001",
		map[string]any{"name": "Flow Room", "noShowFeeCents": 1000})
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateStudio = %v (%s), want Accepted", outcome, why)
	}
	cases := []struct {
		name string
		fee  any
	}{
		{"negative", -500},
		{"fractional", 999.5},
		{"string", "free"},
		{"boolean", false},
	}
	for i, tc := range cases {
		got, why := setStudioProfileAs(t, ctx, conn, cp, cons, "wdspnegedit"+string(rune('a'+i))+"00000001",
			studio, domainActorKey, map[string]any{"noShowFeeCents": tc.fee})
		if got != processor.OutcomeRejected {
			t.Fatalf("SetStudioProfile with a %s noShowFeeCents = %v, want Rejected", tc.name, got)
		}
		if !strings.Contains(why, "InvalidArgument: noShowFeeCents") {
			t.Errorf("%s: refused with %q, want InvalidArgument: noShowFeeCents", tc.name, why)
		}
	}
	if got := studioProfile(t, ctx, conn, studio)["noShowFeeCents"]; got != 1000.0 {
		t.Errorf("profile.noShowFeeCents = %v after the refused edits, want the original 1000", got)
	}
}

// TestSetStudioProfile_WrongTypeNameRefused: a non-string name is refused
// InvalidArgument rather than silently read as "not supplied"; a blank one IS
// "not supplied" and the stored name is carried.
func TestSetStudioProfile_WrongTypeNameRefused(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdspname")

	studio := createStudio(t, ctx, conn, cp, cons, "wdspnamestudioa000001", "Flow Room")
	for i, bad := range []any{42, true, []string{"x"}} {
		got, why := setStudioProfileAs(t, ctx, conn, cp, cons, "wdspnameedit"+string(rune('a'+i))+"0000001",
			studio, domainActorKey, map[string]any{"name": bad, "noShowFeeCents": 1000})
		if got != processor.OutcomeRejected || !strings.Contains(why, "InvalidArgument: name") {
			t.Errorf("SetStudioProfile with name %v = %v (%s), want Rejected InvalidArgument: name", bad, got, why)
		}
	}
	if got, present := studioProfile(t, ctx, conn, studio)["noShowFeeCents"]; present {
		t.Errorf("a refused edit wrote noShowFeeCents = %v; it must refuse before any mutation", got)
	}
	if got, why := setStudioProfileAs(t, ctx, conn, cp, cons, "wdspnameeditz0000001", studio, domainActorKey,
		map[string]any{"name": "   ", "noShowFeeCents": 1000}); got != processor.OutcomeAccepted {
		t.Fatalf("SetStudioProfile with a blank name = %v (%s), want Accepted — blank is not supplied", got, why)
	}
	profile := studioProfile(t, ctx, conn, studio)
	if profile["name"] != "Flow Room" || profile["noShowFeeCents"] != 1000.0 {
		t.Errorf("profile = %v, want {name: Flow Room, noShowFeeCents: 1000}", profile)
	}
}

// TestSetStudioProfile_MalformedStoredPolicyRefusedOnRename: a stored policy
// that is not a non-negative whole number is never carried forward under a
// fresh revision — a rename-only edit is refused InvalidState naming the
// repair, and an edit that supplies the fee repairs it (the name along with
// it, when both are sent).
func TestSetStudioProfile_MalformedStoredPolicyRefusedOnRename(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdspbadstored")

	studio := createStudio(t, ctx, conn, cp, cons, "wdspbadstudioa000001", "Flow Room")
	seedAspect(t, ctx, conn, studio, "profile", "studioProfile", map[string]any{"name": "Flow Room", "noShowFeeCents": "twenty"})

	got, why := setStudioProfileAs(t, ctx, conn, cp, cons, "wdspbadrename0000001", studio, domainActorKey,
		map[string]any{"name": "Flow Room Prime"})
	if got != processor.OutcomeRejected {
		t.Fatalf("rename-only edit over a malformed stored policy = %v, want Rejected", got)
	}
	if !strings.Contains(why, "InvalidState") || !strings.Contains(why, "supply noShowFeeCents to repair it") {
		t.Errorf("refused with %q, want InvalidState naming the repair", why)
	}
	profile := studioProfile(t, ctx, conn, studio)
	if profile["name"] != "Flow Room" || profile["noShowFeeCents"] != "twenty" {
		t.Errorf("profile = %v after the refused rename, want unchanged", profile)
	}

	if got, why := setStudioProfileAs(t, ctx, conn, cp, cons, "wdspbadrepair0000001", studio, domainActorKey,
		map[string]any{"name": "Flow Room Prime", "noShowFeeCents": 1500}); got != processor.OutcomeAccepted {
		t.Fatalf("edit supplying the fee over a malformed stored policy = %v (%s), want Accepted — that is the repair", got, why)
	}
	profile = studioProfile(t, ctx, conn, studio)
	if profile["name"] != "Flow Room Prime" || profile["noShowFeeCents"] != 1500.0 {
		t.Errorf("profile = %v after the repair, want {name: Flow Room Prime, noShowFeeCents: 1500}", profile)
	}
}

// TestSetStudioProfile_UnknownStudioRefused: a studio that never existed is
// refused before the script runs (the declared studio read misses hydration,
// naming the key); a retired one hydrates as a tombstone and the script
// refuses it UnknownStudio; a key of another type is refused at parse.
func TestSetStudioProfile_UnknownStudioRefused(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdspunk")

	absent := "vtx.studio.BBWELLSPUNKNWNHJKMNP"
	got, why := setStudioProfileAs(t, ctx, conn, cp, cons, "wdspunkedita00000001", absent, domainActorKey,
		map[string]any{"noShowFeeCents": 1000})
	if got != processor.OutcomeRejected || !strings.Contains(why, absent) {
		t.Errorf("SetStudioProfile on an absent studio = %v (%s), want Rejected naming %s", got, why, absent)
	}

	retired := createStudio(t, ctx, conn, cp, cons, "wdspunkstudior000001", "Retired Room")
	if got, why := tombstoneStudioAs(t, ctx, conn, cp, cons,
		"wdspunkretirer000001", retired, domainActorKey, "2026-07-08T08:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("TombstoneStudio = %v (%s), want Accepted", got, why)
	}
	got, why = setStudioProfileAs(t, ctx, conn, cp, cons, "wdspunkeditr00000001", retired, domainActorKey,
		map[string]any{"noShowFeeCents": 1000})
	if got != processor.OutcomeRejected || !strings.Contains(why, "UnknownStudio") {
		t.Errorf("SetStudioProfile on a retired studio = %v (%s), want Rejected UnknownStudio", got, why)
	}

	got, why = setStudioProfileAs(t, ctx, conn, cp, cons, "wdspunkeditw00000001", "vtx.session.BBWELLSPUNKNWNHJKMNP", domainActorKey,
		map[string]any{"noShowFeeCents": 1000})
	if got != processor.OutcomeRejected || !strings.Contains(why, "InvalidArgument: studioKey") {
		t.Errorf("SetStudioProfile on a non-studio key = %v (%s), want Rejected InvalidArgument: studioKey", got, why)
	}
}
