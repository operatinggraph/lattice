// Patient / provider / appointment integration tests for the clinic-domain
// Capability Package.
//
// External test package (clinicdomain_test) so the tests exercise the public
// Lattice surface a real package sees: seed the kernel, install rbac-domain +
// identity-domain + identity-hygiene + clinic-domain through the Processor, then
// submit the clinic ops and assert the committed Core-KV shape — the patient /
// provider / appointment vertices + their aspects + the forPatient / withProvider
// links land, and the endpoint-class / status-enum / authorization guards reject
// bad input.
//
// Coverage:
//  1. TestClinic_CreateBookable        — patient + provider + appointment, aspects + links + status=scheduled
//  2. TestClinic_SetAppointmentStatus  — upsert .status scheduled→confirmed in place
//  3. TestClinic_RejectsBadStatus      — status not in the enum → Rejected
//  4. TestClinic_RejectsWrongClass     — CreateAppointment endpoint alive but wrong class → Rejected
//  5. TestClinic_RejectsDeadEndpoint   — CreateAppointment patient absent → Rejected
//  6. TestClinic_TombstonePatient      — patient soft-deleted
//  7. TestClinic_UnauthorizedDenied    — consumer cap (no clinic ops) → Rejected
package clinicdomain_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	clinicdomain "github.com/asolgan/lattice/packages/clinic-domain"
)

const (
	clStaffActorID   = "CLstaffActHJKMNPQRST"
	clStaffActorKey  = "vtx.identity." + clStaffActorID
	clStaffCapKey    = "cap.identity." + clStaffActorID
	clConsumerID     = "CLconsumerHJKMNPQRST"
	clConsumerKey    = "vtx.identity." + clConsumerID
	clConsumerCapKey = "cap.identity." + clConsumerID
)

// clinicOps are the eight ops the staff actor needs.
var clinicOps = []string{
	"CreatePatient", "TombstonePatient",
	"CreateProvider", "TombstoneProvider",
	"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "TombstoneAppointment",
}

func clStaffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	perms := make([]processor.PlatformPermission, 0, len(clinicOps))
	for _, op := range clinicOps {
		perms = append(perms, processor.PlatformPermission{OperationType: op, Scope: "any"})
	}
	return &processor.CapabilityDoc{
		Key:                    clStaffCapKey,
		Actor:                  clStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{clStaffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    perms,
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{bootstrap.RoleOperatorKey},
	}
}

func clConsumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    clConsumerCapKey,
		Actor:                  clConsumerKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{clConsumerKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{"vtx.role.consumer"},
	}
}

func setupClinicEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // installs rbac+identity+hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	if _, err := inst.Install(ctx, clinicdomain.Package); err != nil {
		stop()
		t.Fatalf("install clinic-domain: %v", err)
	}
	stop()
	testutil.SeedCapDoc(t, ctx, conn, clStaffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, clConsumerCapDoc())
	return ctx, conn
}

func newClinicPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "cl-" + durable,
	})
}

func clNanoIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

func clSeedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string, isDeleted bool) {
	t.Helper()
	doc := map[string]any{"class": class, "isDeleted": isDeleted, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed vertex %s: %v", key, err)
	}
}

func clReadDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return doc
}

func clMissing(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	_, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	return err != nil
}

// submit publishes an op and drives it to the expected outcome, returning the
// minted primary key (the requestID-derived NanoID, for the create ops).
func clSubmit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, op, class, payload string, reads []string, want processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: op,
		Actor:         clStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         class,
		Payload:       json.RawMessage(payload),
	}
	if len(reads) > 0 {
		env.ContextHint = &processor.ContextHint{Reads: reads}
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return clNanoIDFromRequestID(reqID)
}

// createPatient / createProvider mint a vertex and return its full key.
func createPatient(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, fullName string) string {
	id := clSubmit(t, ctx, conn, cp, cons, label, "CreatePatient", "patient",
		`{"fullName":"`+fullName+`"}`, nil, processor.OutcomeAccepted)
	return "vtx.patient." + id
}

func createProvider(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, fullName, specialty string) string {
	id := clSubmit(t, ctx, conn, cp, cons, label, "CreateProvider", "provider",
		`{"fullName":"`+fullName+`","specialty":"`+specialty+`"}`, nil, processor.OutcomeAccepted)
	return "vtx.provider." + id
}

// TestClinic_CreateBookable mints a patient + provider + appointment and asserts
// the full bookable shape: vertices class-correct + root {}, the aspects (incl.
// status=scheduled), and the forPatient / withProvider links.
func TestClinic_CreateBookable(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "create")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0001", "Alice Rivera")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0001", "Dr. Sam Okafor", "Cardiology")

	// Patient vertex + demographics aspect.
	if pdoc := clReadDoc(t, ctx, conn, patientKey); pdoc["class"] != "patient" {
		t.Fatalf("patient class = %v, want patient", pdoc["class"])
	}
	demo := clReadDoc(t, ctx, conn, patientKey+".demographics")
	if demo["class"] != "patientDemographics" {
		t.Fatalf("demographics class = %v, want patientDemographics", demo["class"])
	}
	if dd, _ := demo["data"].(map[string]any); dd["fullName"] != "Alice Rivera" {
		t.Fatalf("demographics fullName = %v, want Alice Rivera", dd["fullName"])
	}
	// Provider profile aspect.
	prof := clReadDoc(t, ctx, conn, providerKey+".profile")
	if pd, _ := prof["data"].(map[string]any); pd["specialty"] != "Cardiology" {
		t.Fatalf("profile specialty = %v, want Cardiology", pd["specialty"])
	}

	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z","reason":"Annual checkup"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	if adoc := clReadDoc(t, ctx, conn, apptKey); adoc["class"] != "appointment" {
		t.Fatalf("appointment class = %v, want appointment", adoc["class"])
	}
	sched := clReadDoc(t, ctx, conn, apptKey+".schedule")
	if sd, _ := sched["data"].(map[string]any); sd["startsAt"] != "2026-07-01T15:00:00Z" || sd["endsAt"] != "2026-07-01T15:30:00Z" || sd["reason"] != "Annual checkup" {
		t.Fatalf("schedule data = %v", sched["data"])
	}
	// remindAt = startsAt − 24h, derived by CreateAppointment (canonical UTC) — the
	// deadline the clinic-reminders convergence lens projects as freshUntil.
	if sd, _ := sched["data"].(map[string]any); sd["remindAt"] != "2026-06-30T15:00:00Z" {
		t.Fatalf("schedule remindAt = %v, want 2026-06-30T15:00:00Z (startsAt − 24h)", sched["data"])
	}
	status := clReadDoc(t, ctx, conn, apptKey+".status")
	if st, _ := status["data"].(map[string]any); st["value"] != "scheduled" {
		t.Fatalf("initial status = %v, want scheduled", st["value"])
	}
	// Links: forPatient + withProvider (appointment is the source).
	forPatient := "lnk.appointment." + apptID + ".forPatient.patient." + patientKey[len("vtx.patient."):]
	withProvider := "lnk.appointment." + apptID + ".withProvider.provider." + providerKey[len("vtx.provider."):]
	if ld := clReadDoc(t, ctx, conn, forPatient); ld["sourceVertex"] != apptKey || ld["targetVertex"] != patientKey {
		t.Fatalf("forPatient link endpoints = src %v tgt %v", ld["sourceVertex"], ld["targetVertex"])
	}
	if ld := clReadDoc(t, ctx, conn, withProvider); ld["sourceVertex"] != apptKey || ld["targetVertex"] != providerKey {
		t.Fatalf("withProvider link endpoints = src %v tgt %v", ld["sourceVertex"], ld["targetVertex"])
	}
}

// TestClinic_SetAppointmentStatus proves the unconditioned-upsert idiom: a
// SetAppointmentStatus overwrites the .status aspect in place (scheduled→confirmed).
func TestClinic_SetAppointmentStatus(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0002", "Bob Tenant")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0002", "Dr. Lee", "Dermatology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0002", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-02T09:00:00Z","endsAt":"2026-07-02T09:20:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	clSubmit(t, ctx, conn, cp, cons, "setstat0001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"confirmed"}`, []string{apptKey}, processor.OutcomeAccepted)

	status := clReadDoc(t, ctx, conn, apptKey+".status")
	if st, _ := status["data"].(map[string]any); st["value"] != "confirmed" {
		t.Fatalf("after SetAppointmentStatus, status = %v, want confirmed", st["value"])
	}
	if del, _ := status["isDeleted"].(bool); del {
		t.Fatalf("status aspect should be alive after upsert; got isDeleted=%v", del)
	}
}

// TestClinic_RescheduleAppointment proves the move-an-appointment path: a
// RescheduleAppointment rewrites the .schedule aspect with new startsAt/endsAt,
// re-deriving remindAt = startsAt − 24h (so the clinic-reminders @at re-arms),
// while leaving the .status aspect and the forPatient/withProvider links untouched.
// A re-supplied reason is preserved; an omitted reason clears it; a non-Z offset is
// normalized to canonical UTC; a tombstoned target is rejected.
func TestClinic_RescheduleAppointment(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "reschedule")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0007", "Erin Mover")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0007", "Dr. Reyes", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0007", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-10T15:00:00Z","endsAt":"2026-07-10T15:30:00Z","reason":"Annual checkup"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// Reschedule to a new day, re-supplying the reason (the FE round-trips it). The
	// startsAt is given with a +02:00 offset to prove canonical-UTC normalization.
	clSubmit(t, ctx, conn, cp, cons, "resched0001", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","startsAt":"2026-07-12T18:00:00+02:00","endsAt":"2026-07-12T18:30:00+02:00","reason":"Annual checkup"}`,
		[]string{apptKey}, processor.OutcomeAccepted)

	sched := clReadDoc(t, ctx, conn, apptKey+".schedule")
	sd, _ := sched["data"].(map[string]any)
	if sd["startsAt"] != "2026-07-12T16:00:00Z" || sd["endsAt"] != "2026-07-12T16:30:00Z" {
		t.Fatalf("after reschedule, schedule times = %v, want startsAt 2026-07-12T16:00:00Z / endsAt 2026-07-12T16:30:00Z (UTC-normalized)", sched["data"])
	}
	// remindAt re-derived = new startsAt − 24h (so the @at reminder re-arms).
	if sd["remindAt"] != "2026-07-11T16:00:00Z" {
		t.Fatalf("after reschedule, remindAt = %v, want 2026-07-11T16:00:00Z (new startsAt − 24h)", sd["remindAt"])
	}
	if sd["reason"] != "Annual checkup" {
		t.Fatalf("after reschedule, reason = %v, want preserved Annual checkup", sd["reason"])
	}
	if del, _ := sched["isDeleted"].(bool); del {
		t.Fatalf("schedule aspect should be alive after reschedule; got isDeleted=%v", del)
	}
	// Status untouched (still scheduled) and the links still present.
	if st, _ := clReadDoc(t, ctx, conn, apptKey+".status")["data"].(map[string]any); st["value"] != "scheduled" {
		t.Fatalf("status = %v after reschedule, want scheduled (untouched)", st["value"])
	}
	forPatient := "lnk.appointment." + apptID + ".forPatient.patient." + patientKey[len("vtx.patient."):]
	if clMissing(t, ctx, conn, forPatient) {
		t.Fatalf("forPatient link should survive a reschedule")
	}

	// Reschedule again with NO reason → the reason is cleared.
	clSubmit(t, ctx, conn, cp, cons, "resched0002", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","startsAt":"2026-07-13T09:00:00Z","endsAt":"2026-07-13T09:20:00Z"}`,
		[]string{apptKey}, processor.OutcomeAccepted)
	sd2, _ := clReadDoc(t, ctx, conn, apptKey+".schedule")["data"].(map[string]any)
	if _, present := sd2["reason"]; present {
		t.Fatalf("an omitted reason should clear it; got reason=%v", sd2["reason"])
	}
	if sd2["startsAt"] != "2026-07-13T09:00:00Z" || sd2["remindAt"] != "2026-07-12T09:00:00Z" {
		t.Fatalf("second reschedule schedule = %v, want startsAt 2026-07-13T09:00:00Z / remindAt 2026-07-12T09:00:00Z", sd2)
	}

	// A tombstoned appointment cannot be rescheduled.
	clSubmit(t, ctx, conn, cp, cons, "tombappt0001", "TombstoneAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`"}`, []string{apptKey}, processor.OutcomeAccepted)
	clSubmit(t, ctx, conn, cp, cons, "resched0003", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","startsAt":"2026-07-14T09:00:00Z","endsAt":"2026-07-14T09:20:00Z"}`,
		[]string{apptKey}, processor.OutcomeRejected)
}

// TestClinic_RejectsBadStatus proves the status enum guard.
func TestClinic_RejectsBadStatus(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "bad-status")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0003", "Carol")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0003", "Dr. Kim", "Pediatrics")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0003", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-03T09:00:00Z","endsAt":"2026-07-03T09:20:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	clSubmit(t, ctx, conn, cp, cons, "badstat0001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"bogus"}`, []string{apptKey}, processor.OutcomeRejected)

	// The status stays scheduled (the bad transition committed nothing).
	status := clReadDoc(t, ctx, conn, apptKey+".status")
	if st, _ := status["data"].(map[string]any); st["value"] != "scheduled" {
		t.Fatalf("status = %v after a rejected bad transition, want scheduled (unchanged)", st["value"])
	}
}

// TestClinic_RejectsWrongClass proves CreateAppointment's endpoint-class guard: a
// patient slot pointed at a vtx.patient-shaped key that is alive but the WRONG
// class is rejected (no appointment is committed).
func TestClinic_RejectsWrongClass(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "wrong-class")

	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0004", "Dr. Wu", "Oncology")
	// A patient-shaped key that is alive but class=identity (not patient).
	fakePatient := "vtx.patient.CLfakepatHJKMNPQRST"
	clSeedVertex(t, ctx, conn, fakePatient, "identity", false)

	apptID := clSubmit(t, ctx, conn, cp, cons, "wrongcls0001", "CreateAppointment", "appointment",
		`{"patient":"`+fakePatient+`","provider":"`+providerKey+`","startsAt":"2026-07-04T09:00:00Z","endsAt":"2026-07-04T09:20:00Z"}`,
		[]string{fakePatient, providerKey}, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, "vtx.appointment."+apptID) {
		t.Fatalf("an appointment was committed against a wrong-class patient")
	}
}

// TestClinic_RejectsDeadEndpoint proves the no-orphan guard: CreateAppointment
// against an absent patient is rejected.
func TestClinic_RejectsDeadEndpoint(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "dead-endpoint")

	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0005", "Dr. Ng", "Neurology")
	absentPatient := "vtx.patient.CLabsentptHJKMNPQRS"

	apptID := clSubmit(t, ctx, conn, cp, cons, "dead0001", "CreateAppointment", "appointment",
		`{"patient":"`+absentPatient+`","provider":"`+providerKey+`","startsAt":"2026-07-05T09:00:00Z","endsAt":"2026-07-05T09:20:00Z"}`,
		[]string{absentPatient, providerKey}, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, "vtx.appointment."+apptID) {
		t.Fatalf("an appointment was committed against an absent patient")
	}
}

// TestClinic_TombstonePatient proves a patient soft-deletes.
func TestClinic_TombstonePatient(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "tombstone")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0006", "Dave")
	clSubmit(t, ctx, conn, cp, cons, "tomb0001", "TombstonePatient", "patient",
		`{"patientKey":"`+patientKey+`"}`, []string{patientKey}, processor.OutcomeAccepted)

	if pdoc := clReadDoc(t, ctx, conn, patientKey); pdoc["isDeleted"] != true {
		t.Fatalf("patient isDeleted = %v after tombstone, want true", pdoc["isDeleted"])
	}
}

// TestClinic_UnauthorizedDenied proves a consumer with no clinic ops is rejected.
func TestClinic_UnauthorizedDenied(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "unauth")

	reqID := testutil.GenReqID("unauth0001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreatePatient",
		Actor:         clConsumerKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "patient",
		Payload:       json.RawMessage(`{"fullName":"Should Not Commit"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, "vtx.patient."+clNanoIDFromRequestID(reqID)) {
		t.Fatalf("a patient was committed by an unauthorized consumer")
	}
}
