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

// clinicOps are the nine ops the staff actor needs.
var clinicOps = []string{
	"CreatePatient", "TombstonePatient",
	"CreateProvider", "TombstoneProvider", "SetProviderHours",
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
		[]string{patientKey, providerKey, providerKey + ".bookings"}, processor.OutcomeAccepted)
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

// TestClinic_DoubleBookRejected proves op-time double-book rejection: CreateAppointment
// reads the provider's .bookings index, kv.Reads each candidate's live schedule + status,
// and rejects an overlap with a still scheduled/confirmed appointment (SlotConflict) while
// allowing disjoint / back-to-back slots, a different provider, and a freed (cancelled /
// tombstoned) slot. Also proves the endsAt>startsAt guard.
func TestClinic_DoubleBookRejected(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "double-book")

	patientKey := createPatient(t, ctx, conn, cp, cons, "dbpat0001", "Dana Booker")
	providerKey := createProvider(t, ctx, conn, cp, cons, "dbprv0001", "Dr. Vale", "Cardiology")
	providerKey2 := createProvider(t, ctx, conn, cp, cons, "dbprv0002", "Dr. West", "Pediatrics")

	bk := providerKey + ".bookings"
	bk2 := providerKey2 + ".bookings"
	mkAppt := func(label, prov, bkkey, start, end string, want processor.MessageOutcome) string {
		return clSubmit(t, ctx, conn, cp, cons, label, "CreateAppointment", "appointment",
			`{"patient":"`+patientKey+`","provider":"`+prov+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			[]string{patientKey, prov, bkkey}, want)
	}

	// 1. First booking 10:00–10:30 → accepted.
	a1 := mkAppt("dbappt0001", providerKey, bk, "2026-08-01T10:00:00Z", "2026-08-01T10:30:00Z", processor.OutcomeAccepted)
	// 2. Overlapping 10:15–10:45 with the SAME provider → SlotConflict (rejected).
	mkAppt("dbappt0002", providerKey, bk, "2026-08-01T10:15:00Z", "2026-08-01T10:45:00Z", processor.OutcomeRejected)
	// 3. Fully containing 09:45–10:50 → also overlaps → rejected.
	mkAppt("dbappt0003", providerKey, bk, "2026-08-01T09:45:00Z", "2026-08-01T10:50:00Z", processor.OutcomeRejected)
	// 4. Disjoint 11:00–11:30 with the same provider → accepted.
	a4 := mkAppt("dbappt0004", providerKey, bk, "2026-08-01T11:00:00Z", "2026-08-01T11:30:00Z", processor.OutcomeAccepted)
	// 5. Back-to-back 10:30–11:00 (touches a1's end and a4's start, half-open → no overlap) → accepted.
	mkAppt("dbappt0005", providerKey, bk, "2026-08-01T10:30:00Z", "2026-08-01T11:00:00Z", processor.OutcomeAccepted)
	// 6. Same 10:00–10:30 slot but a DIFFERENT provider → conflict is per-provider → accepted.
	mkAppt("dbappt0006", providerKey2, bk2, "2026-08-01T10:00:00Z", "2026-08-01T10:30:00Z", processor.OutcomeAccepted)

	// 7. endsAt <= startsAt → InvalidArgument (rejected), no overlap math needed.
	mkAppt("dbappt0007", providerKey, bk, "2026-08-01T13:00:00Z", "2026-08-01T13:00:00Z", processor.OutcomeRejected)

	// 8. Cancel a1, then re-book its 10:00–10:30 slot → the cancelled appointment no
	//    longer blocks (pruned from the index by liveness) → accepted.
	a1Key := "vtx.appointment." + a1
	clSubmit(t, ctx, conn, cp, cons, "dbcancel001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+a1Key+`","status":"cancelled"}`, []string{a1Key}, processor.OutcomeAccepted)
	mkAppt("dbappt0008", providerKey, bk, "2026-08-01T10:00:00Z", "2026-08-01T10:30:00Z", processor.OutcomeAccepted)

	// 9. Tombstone a4 (11:00–11:30), then re-book that slot → a tombstoned vertex is
	//    ignored (gated on the vertex isDeleted, not the lingering aspects) → accepted.
	a4Key := "vtx.appointment." + a4
	clSubmit(t, ctx, conn, cp, cons, "dbtomb0001", "TombstoneAppointment", "appointment",
		`{"appointmentKey":"`+a4Key+`"}`, []string{a4Key}, processor.OutcomeAccepted)
	mkAppt("dbappt0009", providerKey, bk, "2026-08-01T11:00:00Z", "2026-08-01T11:30:00Z", processor.OutcomeAccepted)

	// The index pruned the cancelled a1 and tombstoned a4 on the subsequent creates.
	bookings := clReadDoc(t, ctx, conn, bk)
	bd, _ := bookings["data"].(map[string]any)
	appts, _ := bd["appts"].([]any)
	for _, x := range appts {
		if x == a1Key {
			t.Fatalf("cancelled appointment %s should have been pruned from the bookings index, got %v", a1Key, appts)
		}
		if x == a4Key {
			t.Fatalf("tombstoned appointment %s should have been pruned from the bookings index, got %v", a4Key, appts)
		}
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
		[]string{patientKey, providerKey, providerKey + ".bookings"}, processor.OutcomeAccepted)
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

// TestClinic_StatusCheckedInAndNote proves the day-of-visit extensions: the new
// checkedIn status is accepted (an active, non-terminal state), and an optional
// audit note is recorded on the .status aspect for a no-show / cancel. A later
// noteless transition clears the note (the note belongs to the transition it was
// recorded with).
func TestClinic_StatusCheckedInAndNote(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status-note")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0009", "Pat Note")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0009", "Dr. Note", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0009", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-09T09:00:00Z","endsAt":"2026-07-09T09:20:00Z"}`,
		[]string{patientKey, providerKey, providerKey + ".bookings"}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// checkedIn is a valid status (the new active state).
	clSubmit(t, ctx, conn, cp, cons, "setchkin001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"checkedIn"}`, []string{apptKey}, processor.OutcomeAccepted)
	status := clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ := status["data"].(map[string]any)
	if st["value"] != "checkedIn" {
		t.Fatalf("status = %v, want checkedIn", st["value"])
	}
	if _, hasNote := st["note"]; hasNote {
		t.Fatalf("a noteless transition must carry no note; got %v", st["note"])
	}

	// A no-show with an audit note records the note on .status.
	clSubmit(t, ctx, conn, cp, cons, "setnoshow01", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"noShow","note":"patient never arrived"}`, []string{apptKey}, processor.OutcomeAccepted)
	status = clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ = status["data"].(map[string]any)
	if st["value"] != "noShow" || st["note"] != "patient never arrived" {
		t.Fatalf("status = %v (want noShow + note), got note=%v", st["value"], st["note"])
	}

	// A later noteless transition clears the note (unconditioned upsert).
	clSubmit(t, ctx, conn, cp, cons, "setsched001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"scheduled"}`, []string{apptKey}, processor.OutcomeAccepted)
	status = clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ = status["data"].(map[string]any)
	if _, hasNote := st["note"]; hasNote {
		t.Fatalf("a noteless transition must clear the prior note; got %v", st["note"])
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
		[]string{patientKey, providerKey, providerKey + ".bookings"}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// Reschedule to a new day, re-supplying the reason (the FE round-trips it). The
	// startsAt is given with a +02:00 offset to prove canonical-UTC normalization.
	// The provider + its .bookings index are supplied for the conflict check.
	bk := providerKey + ".bookings"
	clSubmit(t, ctx, conn, cp, cons, "resched0001", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","startsAt":"2026-07-12T18:00:00+02:00","endsAt":"2026-07-12T18:30:00+02:00","reason":"Annual checkup"}`,
		[]string{apptKey, bk}, processor.OutcomeAccepted)

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
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","startsAt":"2026-07-13T09:00:00Z","endsAt":"2026-07-13T09:20:00Z"}`,
		[]string{apptKey, bk}, processor.OutcomeAccepted)
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
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","startsAt":"2026-07-14T09:00:00Z","endsAt":"2026-07-14T09:20:00Z"}`,
		[]string{apptKey, bk}, processor.OutcomeRejected)
}

// TestClinic_RescheduleIntoConflictRejected proves Increment 2's core property: a
// reschedule that moves an appointment INTO a slot already booked for the provider
// is rejected (SlotConflict), closing the double-book bypass Increment 1 left open
// (CreateAppointment was checked, RescheduleAppointment was not). It also pins
// self-exclusion (moving onto your own current slot is fine), wrong-provider
// rejection, the endsAt<=startsAt guard, that a cancelled appointment frees its
// slot for a reschedule, and that the moved interval frees its old slot.
func TestClinic_RescheduleIntoConflictRejected(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "reschedule-conflict")

	patientKey := createPatient(t, ctx, conn, cp, cons, "rcpat0001", "Frank Mover")
	providerKey := createProvider(t, ctx, conn, cp, cons, "rcprv0001", "Dr. Solis", "Cardiology")
	providerKey2 := createProvider(t, ctx, conn, cp, cons, "rcprv0002", "Dr. Tan", "Pediatrics")
	bk := providerKey + ".bookings"

	mkAppt := func(label, start, end string) string {
		return clSubmit(t, ctx, conn, cp, cons, label, "CreateAppointment", "appointment",
			`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			[]string{patientKey, providerKey, bk}, processor.OutcomeAccepted)
	}
	resched := func(label, apptKey, start, end string, want processor.MessageOutcome) {
		clSubmit(t, ctx, conn, cp, cons, label, "RescheduleAppointment", "appointment",
			`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			[]string{apptKey, bk}, want)
	}

	a1 := "vtx.appointment." + mkAppt("rcappt0001", "2026-09-01T10:00:00Z", "2026-09-01T10:30:00Z")
	a2 := "vtx.appointment." + mkAppt("rcappt0002", "2026-09-01T11:00:00Z", "2026-09-01T11:30:00Z")

	// 1. Move a2 onto a1's 10:00–10:30 slot → SlotConflict (the bypass is closed).
	resched("rcres0001", a2, "2026-09-01T10:15:00Z", "2026-09-01T10:45:00Z", processor.OutcomeRejected)
	// 2. Move a2 to a free 12:00 slot → accepted.
	resched("rcres0002", a2, "2026-09-01T12:00:00Z", "2026-09-01T12:30:00Z", processor.OutcomeAccepted)
	// 3. Re-time a1 within its own slot (self-exclusion: you never conflict with
	//    your own current booking) → accepted.
	resched("rcres0003", a1, "2026-09-01T10:00:00Z", "2026-09-01T10:45:00Z", processor.OutcomeAccepted)
	// 4. Now a1 occupies 10:00–10:45 and a2 occupies 12:00. Moving a2 back to 10:30
	//    overlaps the extended a1 → rejected.
	resched("rcres0004", a2, "2026-09-01T10:30:00Z", "2026-09-01T11:00:00Z", processor.OutcomeRejected)
	// 5. Back-to-back at a1's new end (10:45) is half-open → no overlap → accepted.
	resched("rcres0005", a2, "2026-09-01T10:45:00Z", "2026-09-01T11:15:00Z", processor.OutcomeAccepted)
	// 6. endsAt <= startsAt is rejected (the guard reschedule previously lacked).
	resched("rcres0006", a2, "2026-09-01T14:00:00Z", "2026-09-01T14:00:00Z", processor.OutcomeRejected)

	// 7. Wrong provider (a2 is Dr. Solis's; pass Dr. Tan) → WrongProvider rejected,
	//    even though Dr. Tan's slot is free — a wrong provider must not bypass the
	//    check by pointing at an empty book.
	clSubmit(t, ctx, conn, cp, cons, "rcres0007", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+a2+`","provider":"`+providerKey2+`","startsAt":"2026-09-01T15:00:00Z","endsAt":"2026-09-01T15:30:00Z"}`,
		[]string{a2, providerKey2 + ".bookings"}, processor.OutcomeRejected)

	// 8. Cancel a1, then move a2 onto a1's (now freed) 10:00 slot → accepted (a
	//    cancelled appointment no longer blocks; pruned by liveness).
	clSubmit(t, ctx, conn, cp, cons, "rccancel01", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+a1+`","status":"cancelled"}`, []string{a1}, processor.OutcomeAccepted)
	resched("rcres0008", a2, "2026-09-01T10:00:00Z", "2026-09-01T10:30:00Z", processor.OutcomeAccepted)
}

// TestClinic_ProviderHoursEnforced proves Increment 2b: a provider's opt-in
// availability windows (the .hours aspect, set by SetProviderHours) gate
// CreateAppointment + RescheduleAppointment. A booking outside the windows is
// rejected (OutsideHours); inside is accepted; a provider with no .hours is
// unconstrained; windows=[] clears the constraint; and SetProviderHours validates
// its windows. Weekdays (UTC): 2026-06-28=Sun, 06-29=Mon, 07-01=Wed, 07-04=Sat.
func TestClinic_ProviderHoursEnforced(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "provider-hours")

	patientKey := createPatient(t, ctx, conn, cp, cons, "phpat0001", "Holly Hours")
	providerKey := createProvider(t, ctx, conn, cp, cons, "phprv0001", "Dr. Mon", "Cardiology")
	bk := providerKey + ".bookings"

	// Set Mon(1) + Wed(3) 09:00–17:00 UTC (32400–61200 seconds-of-day).
	clSubmit(t, ctx, conn, cp, cons, "phhours001", "SetProviderHours", "provider",
		`{"providerKey":"`+providerKey+`","windows":[{"day":1,"openSec":32400,"closeSec":61200},{"day":3,"openSec":32400,"closeSec":61200}]}`,
		[]string{providerKey}, processor.OutcomeAccepted)

	// The .hours aspect landed with both windows.
	hours := clReadDoc(t, ctx, conn, providerKey+".hours")
	if hours["class"] != "providerHours" {
		t.Fatalf("hours class = %v, want providerHours", hours["class"])
	}
	if wd, _ := hours["data"].(map[string]any); wd["windows"] == nil {
		t.Fatalf("hours windows missing: %v", hours["data"])
	} else if w, _ := wd["windows"].([]any); len(w) != 2 {
		t.Fatalf("hours windows = %v, want 2", wd["windows"])
	}

	mkAppt := func(label, start, end string, want processor.MessageOutcome) string {
		return clSubmit(t, ctx, conn, cp, cons, label, "CreateAppointment", "appointment",
			`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			[]string{patientKey, providerKey, bk}, want)
	}

	// Monday 10:00–10:30 — inside the window → accepted.
	a1 := mkAppt("phappt0001", "2026-06-29T10:00:00Z", "2026-06-29T10:30:00Z", processor.OutcomeAccepted)
	// Monday 08:00–08:30 — before open (32400) → OutsideHours rejected.
	mkAppt("phappt0002", "2026-06-29T08:00:00Z", "2026-06-29T08:30:00Z", processor.OutcomeRejected)
	// Monday 16:30–17:30 — ends after close (61200) → rejected.
	mkAppt("phappt0003", "2026-06-29T16:30:00Z", "2026-06-29T17:30:00Z", processor.OutcomeRejected)
	// Monday 16:30–17:00 — ends EXACTLY at close (boundary inclusive) → accepted.
	mkAppt("phappt0004", "2026-06-29T16:30:00Z", "2026-06-29T17:00:00Z", processor.OutcomeAccepted)
	// Wednesday 09:00–09:30 — starts EXACTLY at open (boundary inclusive) → accepted.
	mkAppt("phappt0005", "2026-07-01T09:00:00Z", "2026-07-01T09:30:00Z", processor.OutcomeAccepted)
	// Sunday 10:00–10:30 — no window that day → OutsideHours rejected.
	mkAppt("phappt0006", "2026-06-28T10:00:00Z", "2026-06-28T10:30:00Z", processor.OutcomeRejected)

	// A reschedule must also honour the windows: move a1 (Mon, in-hours) to Sunday
	// 10:00 → rejected; to Wednesday 10:00 (in-hours) → accepted.
	a1Key := "vtx.appointment." + a1
	resched := func(label, start, end string, want processor.MessageOutcome) {
		clSubmit(t, ctx, conn, cp, cons, label, "RescheduleAppointment", "appointment",
			`{"appointmentKey":"`+a1Key+`","provider":"`+providerKey+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			[]string{a1Key, bk}, want)
	}
	resched("phres0001", "2026-06-28T10:00:00Z", "2026-06-28T10:30:00Z", processor.OutcomeRejected)
	resched("phres0002", "2026-07-01T10:00:00Z", "2026-07-01T10:30:00Z", processor.OutcomeAccepted)

	// A DIFFERENT provider with NO .hours is unconstrained — a Sunday booking is fine.
	freeProvider := createProvider(t, ctx, conn, cp, cons, "phprv0002", "Dr. Always", "GeneralPractice")
	clSubmit(t, ctx, conn, cp, cons, "phappt0007", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+freeProvider+`","startsAt":"2026-06-28T03:00:00Z","endsAt":"2026-06-28T03:30:00Z"}`,
		[]string{patientKey, freeProvider, freeProvider + ".bookings"}, processor.OutcomeAccepted)

	// Clearing the constraint (windows=[]) makes the original provider unconstrained:
	// a Sunday booking is now accepted.
	clSubmit(t, ctx, conn, cp, cons, "phhours002", "SetProviderHours", "provider",
		`{"providerKey":"`+providerKey+`","windows":[]}`,
		[]string{providerKey}, processor.OutcomeAccepted)
	mkAppt("phappt0008", "2026-06-28T20:00:00Z", "2026-06-28T20:30:00Z", processor.OutcomeAccepted)

	// SetProviderHours validation: day out of range (7) and openSec>=closeSec → rejected.
	clSubmit(t, ctx, conn, cp, cons, "phbad0001", "SetProviderHours", "provider",
		`{"providerKey":"`+providerKey+`","windows":[{"day":7,"openSec":32400,"closeSec":61200}]}`,
		[]string{providerKey}, processor.OutcomeRejected)
	clSubmit(t, ctx, conn, cp, cons, "phbad0002", "SetProviderHours", "provider",
		`{"providerKey":"`+providerKey+`","windows":[{"day":1,"openSec":61200,"closeSec":32400}]}`,
		[]string{providerKey}, processor.OutcomeRejected)
}

// TestClinic_RejectsBadStatus proves the status enum guard.
func TestClinic_RejectsBadStatus(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "bad-status")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0003", "Carol")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0003", "Dr. Kim", "Pediatrics")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0003", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-03T09:00:00Z","endsAt":"2026-07-03T09:20:00Z"}`,
		[]string{patientKey, providerKey, providerKey + ".bookings"}, processor.OutcomeAccepted)
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
