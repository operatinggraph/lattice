package servicedomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// seedIdentifiedByLink writes the serviceprovider identifiedBy identity link
// BindServiceProviderIdentity would mint, so these tests prove the profile
// guard rather than the bind ceremony.
func seedIdentifiedByLink(t *testing.T, ctx context.Context, conn *substrate.Conn, spKey, identityKey string) {
	t.Helper()
	spID := spKey[len("vtx.serviceprovider."):]
	identityID := identityKey[len("vtx.identity."):]
	key := "lnk.serviceprovider." + spID + ".identifiedBy.identity." + identityID
	doc := map[string]any{
		"class": "identifiedBy", "isDeleted": false,
		"sourceVertex": spKey, "targetVertex": identityKey,
		"localName": "identifiedBy", "data": map[string]any{},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed link %s: %v", key, err)
	}
}

func profileEnv(t *testing.T, label, spKey, displayName, actorKey string) *processor.OperationEnvelope {
	t.Helper()
	spID := spKey[len("vtx.serviceprovider."):]
	actorID := actorKey[len("vtx.identity."):]
	payload, _ := json.Marshal(map[string]any{"serviceProviderKey": spKey, "displayName": displayName})
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetServiceProviderProfile",
		Actor:         actorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "serviceprovider",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         []string{spKey},
			OptionalReads: []string{"lnk.serviceprovider." + spID + ".identifiedBy.identity." + actorID},
		},
	}
}

func spDisplayName(t *testing.T, ctx context.Context, conn *substrate.Conn, spKey string) string {
	t.Helper()
	doc := readDoc(t, ctx, conn, spKey+".profile")
	data, _ := doc["data"].(map[string]any)
	name, _ := data["displayName"].(string)
	return name
}

// TestSetServiceProviderProfile_ConfinedToTheCallersOwnRecord is the security
// proof for the serviceprovider hat's record-administering op.
//
// The grant it runs under is `provider` at scope=any — and `provider` is the
// GENERIC archetype role that all three bind ops mint (service-domain's
// BindServiceProviderIdentity, clinic's BindProviderIdentity, wellness's
// BindInstructorIdentity). The capability plane therefore cannot tell a bound
// service provider from a bound clinician or instructor: every one of them
// arrives holding exactly the role seeded below. The in-script standing guard is
// the ONLY thing that confines the write.
//
// The negative vector is an actor holding the SAME role but bound elsewhere —
// not an unbound stranger, who would be refused by the capability plane before
// the script ever ran and would prove nothing about the guard.
func TestSetServiceProviderProfile_ConfinedToTheCallersOwnRecord(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "sp-profile")

	mineKey := "vtx.serviceprovider.BBsvcprofmineHJKMNPQ"
	theirsKey := "vtx.serviceprovider.BBsvcproftheirsJKMNP"
	seedVertex(t, ctx, conn, mineKey, "serviceprovider", nil)
	seedVertex(t, ctx, conn, theirsKey, "serviceprovider", nil)
	seedProfile := func(spKey, name string) {
		doc := map[string]any{
			"class": "serviceProviderProfile", "isDeleted": false,
			"vertexKey": spKey, "localName": "profile",
			"data": map[string]any{"displayName": name},
		}
		b, _ := json.Marshal(doc)
		if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, spKey+".profile", b); err != nil {
			t.Fatalf("seed profile %s: %v", spKey, err)
		}
	}
	seedProfile(mineKey, "Mine")
	seedProfile(theirsKey, "Theirs")

	// Two logins, each holding the identical generic `provider` role and the
	// identical standing grant. Mine is bound to a serviceprovider; the other
	// stands in for a bound clinician or instructor, who holds the same role
	// from a different domain's bind ceremony.
	providerRole := "vtx.role." + pkgmgr.RoleID("identity-domain", "provider")
	seedProviderLogin := func(actorID string) string {
		actorKey := "vtx.identity." + actorID
		seedVertex(t, ctx, conn, actorKey, "identity", map[string]any{"state": "claimed"})
		testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
			Key:                    "cap.identity." + actorID,
			Actor:                  actorKey,
			Version:                "1.0",
			ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
			ProjectedFromRevisions: map[string]uint64{actorKey: 1},
			Lanes:                  []string{"default"},
			PlatformPermissions: []processor.PlatformPermission{
				{OperationType: "SetServiceProviderProfile", Scope: "any"},
			},
			ServiceAccess:   []processor.ServiceAccessEntry{},
			EphemeralGrants: []processor.EphemeralGrant{},
			Roles:           []string{providerRole},
		})
		return actorKey
	}
	mineActorKey := seedProviderLogin("BBsvcprofmineactrHJK")
	otherArchetypeKey := seedProviderLogin("BBsvcprofothractrJKM")

	seedIdentifiedByLink(t, ctx, conn, mineKey, mineActorKey)

	// A bound service provider edits their own profile.
	testutil.PublishOp(t, conn, profileEnv(t, "svcprofmine000000001", mineKey, "Kai's Laundry Co.", mineActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := spDisplayName(t, ctx, conn, mineKey); got != "Kai's Laundry Co." {
		t.Fatalf("a bound service provider must be able to edit their own profile: displayName = %q", got)
	}

	// The same provider, someone else's record.
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		profileEnv(t, "svcproftheirs0000001", theirsKey, "Hijacked", mineActorKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("editing another provider's profile = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "may not set the profile of service provider") {
		t.Fatalf("rejection should be the standing-guard denial, got %+v", reply.Error)
	}
	if got := spDisplayName(t, ctx, conn, theirsKey); got != "Theirs" {
		t.Fatalf("another provider's profile was written: displayName = %q, want %q", got, "Theirs")
	}

	// The cross-archetype vector: the same generic `provider` role, bound to no
	// serviceprovider at all. This is the shape a bound clinician arrives in,
	// and it must be refused by the binding, not by the role.
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		profileEnv(t, "svcprofcross00000001", mineKey, "Hijacked", otherArchetypeKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a provider-role holder bound elsewhere = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "may not set the profile of service provider") {
		t.Fatalf("rejection should be the standing-guard denial, got %+v", reply.Error)
	}
	if got := spDisplayName(t, ctx, conn, mineKey); got != "Kai's Laundry Co." {
		t.Fatalf("a cross-archetype caller overwrote the profile: displayName = %q", got)
	}
}

// TestSetServiceProviderProfile_RejectsAnEmptyDisplayName pins the field
// edge-manifest's edgeIdentity lens projects as the hat's own chip label. The
// .profile aspect is REPLACED wholesale, so a blank displayName would leave the
// hat nameless.
func TestSetServiceProviderProfile_RejectsAnEmptyDisplayName(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "sp-profile-blank")

	spKey := "vtx.serviceprovider.BBsvcprofbnkHJKMNPQR"
	seedVertex(t, ctx, conn, spKey, "serviceprovider", nil)
	doc := map[string]any{
		"class": "serviceProviderProfile", "isDeleted": false,
		"vertexKey": spKey, "localName": "profile",
		"data": map[string]any{"displayName": "Kai"},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, spKey+".profile", b); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	const actorID = "BBsvcprofbnkactrHJKM"
	actorKey := "vtx.identity." + actorID
	seedVertex(t, ctx, conn, actorKey, "identity", map[string]any{"state": "claimed"})
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + actorID,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "SetServiceProviderProfile", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "provider")},
	})
	seedIdentifiedByLink(t, ctx, conn, spKey, actorKey)

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		profileEnv(t, "svcprofblank00000001", spKey, "   ", actorKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a blank displayName = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "displayName: required non-empty string") {
		t.Fatalf("rejection should name the empty displayName, got %+v", reply.Error)
	}
	if got := spDisplayName(t, ctx, conn, spKey); got != "Kai" {
		t.Fatalf("the profile was overwritten by a rejected edit: displayName = %q", got)
	}
}

// TestSetServiceProviderProfile_OperatorPassesUnbound proves the guard's FIRST
// binder accepts. Every other test exercises actor_holds_operator only in its
// False direction, so a helper broken to always return False would still let
// them all pass while silently locking operators out of the op.
func TestSetServiceProviderProfile_OperatorPassesUnbound(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "sp-profile-op")

	spKey := "vtx.serviceprovider.BBsvcprofopHJKMNPQRS"
	seedVertex(t, ctx, conn, spKey, "serviceprovider", nil)
	doc := map[string]any{
		"class": "serviceProviderProfile", "isDeleted": false,
		"vertexKey": spKey, "localName": "profile",
		"data": map[string]any{"displayName": "Before"},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, spKey+".profile", b); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	// An operator bound to NO serviceprovider at all: the binding branch cannot
	// carry this call, so only the operator branch can.
	const actorID = "BBsvcprofopactrHJKMN"
	actorKey := "vtx.identity." + actorID
	seedVertex(t, ctx, conn, actorKey, "identity", map[string]any{"state": "claimed"})
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + actorID,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "SetServiceProviderProfile", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	})

	// actor_holds_operator resolves the role from the GRAPH, not the cap doc,
	// so the holdsRole link has to actually exist.
	roleID := bootstrap.RoleOperatorKey[len("vtx.role."):]
	linkKey := "lnk.identity." + actorID + ".holdsRole.role." + roleID
	linkDoc := map[string]any{
		"class": "holdsRole", "isDeleted": false,
		"sourceVertex": actorKey, "targetVertex": bootstrap.RoleOperatorKey,
		"localName": "holdsRole", "data": map[string]any{},
	}
	lb, _ := json.Marshal(linkDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, linkKey, lb); err != nil {
		t.Fatalf("seed holdsRole link: %v", err)
	}

	testutil.PublishOp(t, conn, profileEnv(t, "svcprofoperator00001", spKey, "After", actorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := spDisplayName(t, ctx, conn, spKey); got != "After" {
		t.Fatalf("an operator bound to no serviceprovider must pass: displayName = %q", got)
	}
}
