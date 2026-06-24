// Package bypass — Phase 1 Gate 3: Capability Lens adversarial test suite.
//
// Vector #7 — Cross-service access bleed.
//
// Attack: A penthouse resident (svcActor) has service access projected to
// cap.svc.<actor> for the executive-cleaning service (X) — derived from the
// unit/building topology by service-location's capabilityServiceAccess lens.
// They attempt to invoke an operation against a DIFFERENT service (Y) for which
// no serviceAccess entry exists, or an operation NOT in X's allowedOperations.
// The service path's matchServiceAccess (Contract #6 §6.5) must deny both:
// service-not-projected → AuthContextMismatch, op-not-allowed → AuthDenied.
//
// A second actor (svcActorAbsent) has NO cap.svc.<actor> projection at all and
// drives the service path — deny-by-absence (Contract #6 §6.8): the service key
// is missing, so the op is denied with AuthDenied / NoCapabilityEntry.
//
// Fixture (disjoint cap.svc.<actor> entry, service-location's projection):
//
//	svcActor — cap.svc.<actor> serviceAccess:
//	    { service: serviceX, allowedOperations: [BookX, ViewScheduleX] }
//	  (serviceY is NOT present; a multi-level exclusion the lens would compute —
//	   e.g. an unavailableAt building-level marker — is here represented as the
//	   absence of serviceY from the projected serviceAccess[].)
//
// Test cases:
//
//	Positive:       svcActor + {Service: serviceX} + BookX        → ALLOWED
//	Cross-service:  svcActor + {Service: serviceY} (not projected) → DENIED/AuthContextMismatch
//	Op-not-allowed: svcActor + {Service: serviceX} + NotAllowedOp  → DENIED/AuthDenied
//	Deny-by-absence: svcActorAbsent (no cap.svc doc) + {Service: serviceX} → DENIED/NoCapabilityEntry
//
// DEFENDED when: the positive path authorizes AND both the cross-service and
// op-not-allowed paths deny with the correct codes AND an actor with no cap.svc
// projection denies by absence.
//
// Report row:
//
//	Vector #7 | Cross-service access bleed | DEFENDED | CapabilityAuthorizer matchServiceAccess (§6.5/§6.8)
package bypass

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// Service-plane fixture identifiers for Vector #7. The service-access grants
// live in the disjoint cap.svc.<actor> entry produced by service-location's
// capabilityServiceAccess lens (Contract #6 §6.1 / §6.5); the service branch of
// step-3 reads that key.
const (
	svcBleedActorID   = "CAdvSvc1AbCdEfGhJkLm" // 20 chars — penthouse resident with service access
	svcBleedAbsentID  = "CAdvSvc2AbCdEfGhJkLm" // 20 chars — actor with NO cap.svc projection
	svcBleedServiceX  = "vtx.service.CAdvSvcXAbCdEfGhJkLm"
	svcBleedServiceY  = "vtx.service.CAdvSvcYAbCdEfGhJkLm"
	svcBleedOpAllowed = "BookExecutiveCleaning"
	svcBleedOpDenied  = "ScrubTheFountains"

	svcBleedReqPos    = "CdV7PosRq2345678912a" // positive op
	svcBleedReqCross  = "CdV7CrossRq234567890" // cross-service op
	svcBleedReqOpDeny = "CdV7OpDenyRq23456789" // op-not-allowed op
	svcBleedReqAbsent = "CdV7AbsentRq23456789" // deny-by-absence op
)

// svcBleedCapKey is the disjoint service-access entry key for svcActor.
func svcBleedCapKey() string { return "cap.svc.identity." + svcBleedActorID }

// buildSvcAccessDoc builds svcActor's cap.svc.<actor> entry: access to
// serviceX (with BookX + ViewScheduleX) and NOTHING for serviceY.
func buildSvcAccessDoc() *processor.CapabilityDoc {
	actorKey := "vtx.identity." + svcBleedActorID
	return &processor.CapabilityDoc{
		Key:         svcBleedCapKey(),
		Actor:       actorKey,
		Version:     "1.0",
		ProjectedAt: time.Now().UTC().Format(time.RFC3339Nano),
		// Access to exactly one service. serviceY is absent — the cross-service
		// attempt has nothing to match against.
		ServiceAccess: []processor.ServiceAccessEntry{
			{
				Service:     svcBleedServiceX,
				ResolvedVia: []string{"vtx.unit.penthouse"},
				AllowedOperations: []processor.AllowedOperation{
					{OperationType: svcBleedOpAllowed},
					{OperationType: "ViewScheduleExecutiveCleaning"},
				},
			},
		},
	}
}

// setupV7Harness provisions Capability KV with svcActor's cap.svc.<actor> entry.
// svcActorAbsent is deliberately NOT seeded (deny-by-absence case).
func setupV7Harness(t *testing.T) (context.Context, *substrate.Conn, *processor.CapabilityAuthorizer) {
	t.Helper()
	ctx, conn := setupCapAdvHarness(t)

	doc := buildSvcAccessDoc()
	raw, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, capadvCapBucket, doc.Key, raw); err != nil {
		t.Fatalf("v7: seed service-access cap doc: %v", err)
	}

	cfg := processor.DefaultCapabilityAuthorizerConfig()
	authz, err := processor.NewCapabilityAuthorizer(conn, capadvCapBucket, nil, cfg, bypassLogger())
	if err != nil {
		t.Fatalf("NewCapabilityAuthorizer: %v", err)
	}
	return ctx, conn, authz
}

// svcBleedEnv builds a service-path operation envelope: authContext.Service set
// selects the service branch, which reads cap.svc.<actor>.
func svcBleedEnv(reqID, actorID, opType, service string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: opType,
		Actor:         "vtx.identity." + actorID,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		AuthContext: &processor.AuthContext{
			Service: service,
		},
	}
}

// TestCapAdv_V7_ServiceAccess_PositivePath verifies that svcActor can invoke
// BookExecutiveCleaning against serviceX — the service is in serviceAccess[] and
// the operation is in its allowedOperations. This is the positive baseline.
func TestCapAdv_V7_ServiceAccess_PositivePath(t *testing.T) {
	ctx, _, authz := setupV7Harness(t)

	env := svcBleedEnv(svcBleedReqPos, svcBleedActorID, svcBleedOpAllowed, svcBleedServiceX)

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v7 Positive: Authorize error: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("v7 Positive: FAILED — svcActor→serviceX→BookExecutiveCleaning should be ALLOWED; got denied: code=%s reason=%s",
			dec.Code, dec.Reason)
	}
	if dec.Resolved == nil || dec.Resolved.Path != "service" {
		t.Fatalf("v7 Positive: expected Path=service; got %+v", dec.Resolved)
	}

	t.Logf("v7 Positive: svcActor→serviceX→BookExecutiveCleaning ALLOWED ✓ (path: %s)", dec.Resolved.Path)
}

// TestCapAdv_V7_CrossService_Denied verifies that svcActor CANNOT invoke an
// operation against serviceY — serviceY is not in their serviceAccess[] at all.
// Expected: AuthContextMismatch (service not in serviceAccess). This is the
// cross-service bleed defense: holding access to serviceX grants nothing for any
// other service.
func TestCapAdv_V7_CrossService_Denied(t *testing.T) {
	ctx, _, authz := setupV7Harness(t)

	// Same operation name, wrong (unprojected) service.
	env := svcBleedEnv(svcBleedReqCross, svcBleedActorID, svcBleedOpAllowed, svcBleedServiceY)

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v7 CrossService: Authorize error: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("v7 CrossService: EXPOSED — svcActor invoked against serviceY without a projection; cross-service bleed detected")
	}
	if dec.Code != processor.ErrCodeAuthContextMismatch {
		t.Fatalf("v7 CrossService: expected AuthContextMismatch, got: %s (reason: %s)", dec.Code, dec.Reason)
	}

	t.Logf("v7 CrossService: DEFENDED — svcActor→serviceY denied with AuthContextMismatch ✓")
}

// TestCapAdv_V7_OpNotAllowed_Denied verifies that svcActor CANNOT invoke an
// operation that is NOT in serviceX's allowedOperations, even though the service
// itself is projected. Expected: AuthDenied (operationType not in
// serviceAccess.allowedOperations). The service match does not widen the op set.
func TestCapAdv_V7_OpNotAllowed_Denied(t *testing.T) {
	ctx, _, authz := setupV7Harness(t)

	// Correct (projected) service, operation outside its allowedOperations.
	env := svcBleedEnv(svcBleedReqOpDeny, svcBleedActorID, svcBleedOpDenied, svcBleedServiceX)

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v7 OpNotAllowed: Authorize error: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("v7 OpNotAllowed: EXPOSED — svcActor invoked an op not in serviceX's allowedOperations; op-scope bleed detected")
	}
	if dec.Code != processor.ErrCodeAuthDenied {
		t.Fatalf("v7 OpNotAllowed: expected AuthDenied, got: %s (reason: %s)", dec.Code, dec.Reason)
	}

	t.Logf("v7 OpNotAllowed: DEFENDED — svcActor→serviceX→ScrubTheFountains denied with AuthDenied ✓")
}

// TestCapAdv_V7_DenyByAbsence verifies that an actor with NO cap.svc.<actor>
// projection denies on the service path by absence (Contract #6 §6.8): the
// service key is missing, so the op is denied with AuthDenied / NoCapabilityEntry
// — never authorized by default.
func TestCapAdv_V7_DenyByAbsence(t *testing.T) {
	ctx, _, authz := setupV7Harness(t)

	// svcActorAbsent was never seeded — no cap.svc.identity.<absentID> key.
	env := svcBleedEnv(svcBleedReqAbsent, svcBleedAbsentID, svcBleedOpAllowed, svcBleedServiceX)

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v7 DenyByAbsence: absent service key must NOT return error; got %v", err)
	}
	if dec.Authorized {
		t.Fatalf("v7 DenyByAbsence: EXPOSED — an actor with no cap.svc projection authorized a service op; deny-by-absence breached")
	}
	if dec.Code != processor.ErrCodeAuthDenied || dec.Reason != "NoCapabilityEntry" {
		t.Fatalf("v7 DenyByAbsence: expected AuthDenied/NoCapabilityEntry, got: code=%s reason=%s", dec.Code, dec.Reason)
	}

	t.Logf("v7 DenyByAbsence: DEFENDED — actor with no cap.svc projection denied by absence (AuthDenied/NoCapabilityEntry) ✓")
}
