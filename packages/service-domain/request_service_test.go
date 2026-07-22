// RequestService integration tests — the platform's first service-path
// consumer op (edge-manifest Fire 1, G8). Unlike every other op in this
// package, RequestService carries no scope=self/operator PermissionSpec: its
// authorization is entirely structural, via authContext.service against a
// cap.svc.<actor> ServiceAccess grant (the shape service-location's
// capabilityServiceAccess lens projects in production; these tests seed the
// cap doc directly at the cap.svc.<actor-suffix> key rather than running the
// lens, since service-location's install/seed wiring is a later fire).
package servicedomain_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// seedServiceAccessCapDoc grants consumerKey (a vtx.identity.<id> actor) the
// named operationType against exactly one service (template), at the
// cap.svc.<actor-suffix> key step 3's service path reads
// (serviceKeyFromActor, step3_auth_capability.go).
func seedServiceAccessCapDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, consumerKey, serviceKey, operationType string) {
	t.Helper()
	now := time.Now().UTC()
	doc := &processor.CapabilityDoc{
		Key:                    "cap.svc." + consumerKey[len("vtx."):],
		Actor:                  consumerKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{consumerKey: 1},
		Lanes:                  []string{"default"},
		ServiceAccess: []processor.ServiceAccessEntry{
			{
				Service:           serviceKey,
				AllowedOperations: []processor.AllowedOperation{{OperationType: operationType}},
			},
		},
	}
	testutil.SeedCapDoc(t, ctx, conn, doc)
}

// TestRequestService_Success proves the happy path end-to-end: a consumer
// actor authorized (via the service-path cap.svc grant) for exactly this
// template submits RequestService naming only the service; the platform
// derives the applicant from the verified op actor (never the payload) and
// the instance family from the template's own envelope class.
func TestRequestService_Success(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "reqsvc-ok")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")

	consumerID := "BBconsume1HJKMNPQRST"
	consumerKey := "vtx.identity." + consumerID
	seedVertex(t, ctx, conn, consumerKey, "identity", map[string]any{"state": "claimed"})
	seedServiceAccessCapDoc(t, ctx, conn, consumerKey, tplKey, "RequestService")

	reqID := testutil.GenReqID("reqSvcOk0001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RequestService",
		Actor:         consumerKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"service":"` + tplKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tplKey, consumerKey}},
		AuthContext:   &processor.AuthContext{Service: tplKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	instID := nanoIDFromRequestID(reqID)
	instKey := "vtx.service." + instID

	instDoc := readDoc(t, ctx, conn, instKey)
	if cls, _ := instDoc["class"].(string); cls != "service.backgroundCheck.instance" {
		t.Fatalf("instance class = %q, want service.backgroundCheck.instance (derived from the template's own family)", cls)
	}
	data, _ := instDoc["data"].(map[string]any)
	if len(data) != 0 {
		t.Fatalf("instance root data must be minimal ({}), got %v", data)
	}

	providedToLnk := "lnk.service." + instID + ".providedTo.identity." + consumerID
	ptDoc := readDoc(t, ctx, conn, providedToLnk)
	if got, _ := ptDoc["targetVertex"].(string); got != consumerKey {
		t.Fatalf("providedTo targetVertex = %q, want the verified op actor %q (never a payload field)", got, consumerKey)
	}
}

// TestRequestService_PayloadServiceMismatch_Rejected proves the
// defense-in-depth guard: step 3 authorizes this actor for tplA (a real,
// granted ServiceAccess entry), but the payload names a DIFFERENT template
// tplB. The script must reject rather than silently creating an instance
// against the unauthorized tplB.
func TestRequestService_PayloadServiceMismatch_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "reqsvc-mismatch")

	tplA := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")
	tplB := createTemplate(t, ctx, conn, cp, cons, "payment")

	consumerID := "BBmismatchHJKMNPQRST"
	consumerKey := "vtx.identity." + consumerID
	seedVertex(t, ctx, conn, consumerKey, "identity", map[string]any{"state": "claimed"})
	// Authorized for tplA only.
	seedServiceAccessCapDoc(t, ctx, conn, consumerKey, tplA, "RequestService")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("reqSvcMismat1"),
		Lane:          processor.LaneDefault,
		OperationType: "RequestService",
		Actor:         consumerKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		// Step 3 authorizes against tplA (AuthContext.Service); the payload
		// then names tplB — the script must catch this, not step 3.
		Payload:     json.RawMessage(`{"service":"` + tplB + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{tplB, consumerKey}},
		AuthContext: &processor.AuthContext{Service: tplA},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRequestService_NoServiceAccess_Rejected proves an actor with no
// ServiceAccess grant at all for this template is denied at step 3, before
// the script ever runs (the platform-path/operator grant that authorizes
// every other op in this package does not apply here).
func TestRequestService_NoServiceAccess_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "reqsvc-noaccess")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")

	consumerID := "BBnoaccessHJKMNPQRST"
	consumerKey := "vtx.identity." + consumerID
	seedVertex(t, ctx, conn, consumerKey, "identity", map[string]any{"state": "claimed"})
	// No cap doc seeded at all for this actor — no ServiceAccess grant.

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("reqSvcNoAcc01"),
		Lane:          processor.LaneDefault,
		OperationType: "RequestService",
		Actor:         consumerKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"service":"` + tplKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tplKey, consumerKey}},
		AuthContext:   &processor.AuthContext{Service: tplKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateServiceTemplate_PresentationAspect proves the optional
// "presentation" payload object (edge-manifest Fire 1, §3.3) is written
// verbatim as the template's .presentation aspect when supplied, and that an
// absent presentation writes no aspect at all (byte-identical to every
// template created before this fire).
func TestCreateServiceTemplate_PresentationAspect(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "tpl-presentation")

	reqID := testutil.GenReqID("tplPresent001")
	tplID := nanoIDFromRequestID(reqID)
	tplKey := "vtx.service." + tplID
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceTemplate",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload: json.RawMessage(`{"family":"backgroundCheck","presentation":` +
			`{"name":"Background Check","description":"Standard screening","icon":"shield","category":"screening"}}`),
		ContextHint: &processor.ContextHint{Reads: []string{}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	presDoc := readDoc(t, ctx, conn, tplKey+".presentation")
	pdata, _ := presDoc["data"].(map[string]any)
	if got, _ := pdata["name"].(string); got != "Background Check" {
		t.Fatalf("presentation.name = %q, want %q", got, "Background Check")
	}
	if got, _ := pdata["icon"].(string); got != "shield" {
		t.Fatalf("presentation.icon = %q, want %q", got, "shield")
	}
	if vk, _ := presDoc["vertexKey"].(string); vk != tplKey {
		t.Fatalf("presentation aspect vertexKey = %q, want %q", vk, tplKey)
	}

	// A template created without a presentation object writes no aspect at all.
	tplKeyBare := createTemplate(t, ctx, conn, cp, cons, "payment")
	if keyExists(t, ctx, conn, tplKeyBare+".presentation") {
		t.Fatalf("a template created without a presentation payload must carry no .presentation aspect")
	}
}
