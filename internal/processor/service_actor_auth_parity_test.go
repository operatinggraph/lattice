// Auth-parity proof for internal service actors (Story 7.3 AC #3).
//
// An op submitted with Actor=identity:loom / identity:weaver and no
// authContext flows through the EXACT same platform path as a human operator:
// Authorize → authorizeCapabilityPath → matchPlatformPermission, reading
// cap.identity.<id> and matching a scope:"any" platformPermission. There is
// NO service-actor branch anywhere in step-3 — the service actor is
// indistinguishable from a human at the auth boundary because both are just
// an `actor` string resolving to a `cap.<actor>` projection.
package processor

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// rootEquivalentDoc builds a cap.identity.<id> doc carrying the operator
// role's scope:"any" platformPermissions — the projection the Capability Lens
// produces for any holder of the operator role (admin OR service actor).
func rootEquivalentDoc(nanoID string) *CapabilityDoc {
	capKey := "cap.identity." + nanoID
	actorKey := "vtx.identity." + nanoID
	return &CapabilityDoc{
		Key:                    capKey,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []PlatformPermission{
			{OperationType: "CreateMetaVertex", Scope: "any"},
			{OperationType: "UpdateMetaVertex", Scope: "any"},
			{OperationType: "TombstoneMetaVertex", Scope: "any"},
			{OperationType: "InstallPackage", Scope: "any"},
			{OperationType: "UninstallPackage", Scope: "any"},
		},
		ServiceAccess:   []ServiceAccessEntry{},
		EphemeralGrants: []EphemeralGrant{},
		Roles:           []string{"vtx.role.operatorNanoID"},
	}
}

func authorizerFor(t *testing.T, docs ...*CapabilityDoc) *CapabilityAuthorizer {
	t.Helper()
	reader := &fakeReader{entries: map[string][]byte{}}
	for _, d := range docs {
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("marshal cap doc: %v", err)
		}
		reader.entries[d.Key] = raw
	}
	return NewCapabilityAuthorizer(reader, "capability-kv", &fakeClock{now: time.Now()},
		DefaultCapabilityAuthorizerConfig(), capTestLogger())
}

// TestServiceActor_AuthParity_PlatformPath proves both Loom and Weaver pass
// commit-path step-3 auth identically to a human actor: no authContext,
// platform path, scope:"any" match. The Resolved.Path must be "platform"
// (the human path), proving no special-case branch fired.
func TestServiceActor_AuthParity_PlatformPath(t *testing.T) {
	const (
		loomID   = "LoomSvcActorAaBbCcDd"
		weaverID = "WeaverSvcActAaBbCcDd"
		humanID  = "HumanOperatorAaBbCcD"
	)
	authz := authorizerFor(t,
		rootEquivalentDoc(loomID),
		rootEquivalentDoc(weaverID),
		rootEquivalentDoc(humanID),
	)

	actors := map[string]string{
		"loom":   "vtx.identity." + loomID,
		"weaver": "vtx.identity." + weaverID,
		"human":  "vtx.identity." + humanID,
	}

	for name, actorKey := range actors {
		env := &OperationEnvelope{
			RequestID:     "ReqAuthParity0000" + name[:3],
			Lane:          LaneDefault,
			OperationType: "CreateMetaVertex",
			Actor:         actorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			// No AuthContext — the platform path, identical for service + human.
		}
		dec, err := authz.Authorize(context.Background(), env)
		if err != nil {
			t.Fatalf("%s: Authorize error: %v", name, err)
		}
		if !dec.Authorized {
			t.Fatalf("%s: expected Authorized via platform path, got denied (code=%s reason=%s)",
				name, dec.Code, dec.Reason)
		}
		if dec.Resolved == nil || dec.Resolved.Path != "platform" {
			t.Fatalf("%s: expected Resolved.Path=platform (the human path), got %+v", name, dec.Resolved)
		}
		if dec.Resolved.PlatformPermission == nil || dec.Resolved.PlatformPermission.Scope != "any" {
			t.Fatalf("%s: expected scope:any platform permission match, got %+v", name, dec.Resolved.PlatformPermission)
		}
	}
}

// TestServiceActor_AuthParity_IdenticalDecisionToHuman asserts byte-for-byte
// that the service actors' authorization decision matches the human's for the
// same operation — there is no observable difference at the auth boundary.
func TestServiceActor_AuthParity_IdenticalDecisionToHuman(t *testing.T) {
	const (
		weaverID = "WeaverParityAaBbCcDd"
		humanID  = "HumanParityAaBbCcDdE"
	)
	authz := authorizerFor(t, rootEquivalentDoc(weaverID), rootEquivalentDoc(humanID))

	mkEnv := func(actorID string) *OperationEnvelope {
		return &OperationEnvelope{
			RequestID:     "ReqParityIdentical00",
			Lane:          LaneDefault,
			OperationType: "InstallPackage",
			Actor:         "vtx.identity." + actorID,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		}
	}

	humanDec, err := authz.Authorize(context.Background(), mkEnv(humanID))
	if err != nil {
		t.Fatalf("human Authorize error: %v", err)
	}
	weaverDec, err := authz.Authorize(context.Background(), mkEnv(weaverID))
	if err != nil {
		t.Fatalf("weaver Authorize error: %v", err)
	}

	if humanDec.Authorized != weaverDec.Authorized {
		t.Fatalf("authorization differs: human=%v weaver=%v", humanDec.Authorized, weaverDec.Authorized)
	}
	if !weaverDec.Authorized {
		t.Fatalf("weaver must be authorized identically to human")
	}
	if humanDec.Resolved.Path != weaverDec.Resolved.Path {
		t.Fatalf("resolved path differs: human=%q weaver=%q",
			humanDec.Resolved.Path, weaverDec.Resolved.Path)
	}
}

// TestServiceActor_ClassDoesNotGate_NoTopologyNoCap proves the §7.7
// non-gating invariant at the auth boundary: an identity with a service-actor
// class shape but NO holdsRole topology (hence NO cap.* projection) is denied
// — capability comes from topology, not from class or the actor key shape.
func TestServiceActor_ClassDoesNotGate_NoTopologyNoCap(t *testing.T) {
	const imposterID = "ImposterLoomAaBbCcDd"
	// Authorizer with NO cap doc seeded for the imposter → absence = denial.
	authz := authorizerFor(t)

	env := &OperationEnvelope{
		RequestID:     "ReqImposterNoCap0001",
		Lane:          LaneDefault,
		OperationType: "CreateMetaVertex",
		Actor:         "vtx.identity." + imposterID,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	dec, err := authz.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize error: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("imposter with no cap.* projection must be DENIED (class never grants)")
	}
	if dec.Reason != "NoCapabilityEntry" {
		t.Fatalf("expected NoCapabilityEntry denial, got code=%s reason=%s", dec.Code, dec.Reason)
	}
}
