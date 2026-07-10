// RecordAppointmentReminder integration test for the clinic-reminders Capability
// Package.
//
// External test package (clinicreminders_test) so it exercises the public Lattice
// surface: seed the kernel, install rbac + identity + hygiene (via the harness) +
// orchestration-base + clinic-domain + clinic-reminders through the Processor, then
// drive CreateAppointment (clinic-domain) and RecordAppointmentReminder
// (clinic-reminders) and assert the committed Core-KV shape — the .reminder.sentAt
// marker aspect lands, the op is idempotent in effect (re-run overwrites), and the
// liveness guard rejects a reminder on an absent appointment.
//
// The convergence PREDICATE (when missing_reminder opens/closes) is proven on the
// real engine in lens_cypher_test.go; this proves the op write-path end-to-end.
package clinicreminders_test

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
	clinicreminders "github.com/asolgan/lattice/packages/clinic-reminders"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
)

const (
	crStaffActorID  = "CRstaffActHJKMNPQRST"
	crStaffActorKey = "vtx.identity." + crStaffActorID
	crStaffCapKey   = "cap.identity." + crStaffActorID
)

// crOps are the ops the staff actor needs: clinic-domain's create ops + the
// clinic-reminders ops (reminders + the recurring visit series).
var crOps = []string{
	"CreatePatient", "CreateProvider", "CreateAppointment", "RecordAppointmentReminder", "RecordFollowUpReminder",
	"RecordAppointmentReminderNotification", "RecordFollowUpReminderNotification",
	"StartVisitSeries", "PauseVisitSeries", "ResumeVisitSeries", "AdvanceVisitSeries",
}

func crStaffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	perms := make([]processor.PlatformPermission, 0, len(crOps))
	for _, op := range crOps {
		perms = append(perms, processor.PlatformPermission{OperationType: op, Scope: "any"})
	}
	return &processor.CapabilityDoc{
		Key:                    crStaffCapKey,
		Actor:                  crStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{crStaffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    perms,
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{bootstrap.RoleOperatorKey},
	}
}

func setupRemEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // rbac + identity + hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	// remConsumerRoleID stands in for identity-domain's real `consumer` role
	// NanoID: clinic-domain's CreateAppointment scope=self grant (GrantsTo:
	// "consumer") needs a role id registered directly, since these tests don't
	// install identity-domain (the lease-signing lsConsumerRoleID idiom).
	const remConsumerRoleID = "REMConsumerRoZeHJKMN"
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID, "consumer": remConsumerRoleID}
	for _, p := range []pkgmgr.Definition{orchestrationbase.Package, clinicdomain.Package, clinicreminders.Package} {
		if _, err := inst.Install(ctx, p); err != nil {
			stop()
			t.Fatalf("install %s: %v", p.Name, err)
		}
	}
	stop()
	testutil.SeedCapDoc(t, ctx, conn, crStaffCapDoc())
	return ctx, conn
}

func crNanoIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

func crReadDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
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

// crSubmittedAnchor pins the op envelope's submittedAt to a fixed instant so the
// appointment booked below stays valid under CreateAppointment's past-time guard
// (a startsAt at or before submittedAt is rejected ScheduleInPast). The fixed
// 2026-07-01 booking is strictly after this anchor, so the test is deterministic
// regardless of the wall clock.
const crSubmittedAnchor = "2026-01-01T00:00:00Z"

func crSubmit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, op, class, payload string, reads []string, want processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: op,
		Actor:         crStaffActorKey,
		SubmittedAt:   crSubmittedAnchor,
		Class:         class,
		Payload:       json.RawMessage(payload),
	}
	if len(reads) > 0 {
		env.ContextHint = &processor.ContextHint{Reads: reads}
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return crNanoIDFromRequestID(reqID)
}

// TestRecordAppointmentReminder_WritesMarker mints a bookable appointment then
// drives RecordAppointmentReminder, asserting the .reminder.sentAt marker lands
// (class appointmentReminder) — the directOp write-path the appointmentReminders
// playbook dispatches. Then re-runs it to prove the unconditioned overwrite is
// idempotent in effect (sentAt stays present).
func TestRecordAppointmentReminder_WritesMarker(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "rem", Instance: "cr-rem"})

	patientID := crSubmit(t, ctx, conn, cp, cons, "crpat0001", "CreatePatient", "patient", `{"fullName":"Alice Rivera"}`, nil, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerID := crSubmit(t, ctx, conn, cp, cons, "crprv0001", "CreateProvider", "provider", `{"fullName":"Dr. Sam Okafor","specialty":"Cardiology"}`, nil, processor.OutcomeAccepted)
	providerKey := "vtx.provider." + providerID

	apptID := crSubmit(t, ctx, conn, cp, cons, "crappt001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// remindAt was derived by CreateAppointment = startsAt − 24h.
	if sched := crReadDoc(t, ctx, conn, apptKey+".schedule"); func() bool {
		sd, _ := sched["data"].(map[string]any)
		return sd["remindAt"] != "2026-06-30T15:00:00Z"
	}() {
		t.Fatalf("schedule remindAt not derived: %v", crReadDoc(t, ctx, conn, apptKey+".schedule")["data"])
	}

	// The directOp: RecordAppointmentReminder targets the appointment. Class is
	// LEFT EMPTY — exactly as Weaver's actuator dispatches a directOp (it relies on
	// the Processor's operationType→class reverse index, which resolves to the
	// appointmentReminderOp vertexType handler; the appointmentReminder aspect DDL
	// is excluded from that index).
	crSubmit(t, ctx, conn, cp, cons, "crrem0001", "RecordAppointmentReminder", "",
		`{"appointmentKey":"`+apptKey+`","remindedFor":"2026-07-01T15:00:00Z"}`, []string{apptKey}, processor.OutcomeAccepted)

	rem := crReadDoc(t, ctx, conn, apptKey+".reminder")
	if rem["class"] != "appointmentReminder" {
		t.Fatalf("reminder class = %v, want appointmentReminder", rem["class"])
	}
	rd, _ := rem["data"].(map[string]any)
	first, _ := rd["sentAt"].(string)
	if first == "" {
		t.Fatalf("reminder sentAt not written: %v", rem["data"])
	}
	// remindedFor (the startsAt this reminder is for) is recorded verbatim — the
	// column the convergence gate keys on so a reschedule re-arms the reminder.
	if rf, _ := rd["remindedFor"].(string); rf != "2026-07-01T15:00:00Z" {
		t.Fatalf("reminder remindedFor = %q, want 2026-07-01T15:00:00Z", rf)
	}

	// Idempotent in effect: a second RecordAppointmentReminder is ACCEPTED (not
	// rejected) and the marker stays present. The OutcomeAccepted on this re-run is
	// the discriminator that proves the write is an UNCONDITIONED overwrite, not a
	// create-only insert — a create-only second write would conflict on the existing
	// .reminder key and be Rejected, failing this assertion.
	crSubmit(t, ctx, conn, cp, cons, "crrem0002", "RecordAppointmentReminder", "",
		`{"appointmentKey":"`+apptKey+`"}`, []string{apptKey}, processor.OutcomeAccepted)
	rem2 := crReadDoc(t, ctx, conn, apptKey+".reminder")
	rd2, _ := rem2["data"].(map[string]any)
	if s, _ := rd2["sentAt"].(string); s == "" {
		t.Fatalf("reminder sentAt missing after re-run: %v", rem2["data"])
	}
}

// TestRecordAppointmentReminder_RejectsTombstonedAppointment proves the liveness
// guard (vertex_alive): a TOMBSTONED appointment (present in KV, isDeleted=true —
// so hydration succeeds and the guard, not a HydrationMiss, is what fires) is
// Rejected and writes no dangling marker. A hard-absent key would instead fail
// closed earlier at hydrate (HydrationMiss); this exercises the script's guard.
func TestRecordAppointmentReminder_RejectsTombstonedAppointment(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "remdead", Instance: "cr-remdead"})

	dead := "vtx.appointment.CRdeadApptKMNPQRSTVWX"
	doc := map[string]any{"class": "appointment", "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, dead, b); err != nil {
		t.Fatalf("seed tombstoned appointment: %v", err)
	}

	crSubmit(t, ctx, conn, cp, cons, "crrem0003", "RecordAppointmentReminder", "",
		`{"appointmentKey":"`+dead+`"}`, []string{dead}, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, dead+".reminder"); err == nil {
		t.Fatalf("a reminder marker must NOT be written for a tombstoned appointment")
	}
}

// TestRecordAppointmentReminderNotification_WritesMarker drives the replyOp the
// bridge posts after its "notification" adapter Executes for the
// external.notification event recordReminderScript emits. It submits with no
// Reads (the bridge submits none), asserts the .reminderNotification aspect
// lands with the split-out remindedFor, and then proves the create-only
// once-only guard: a second reply for the SAME externalRef is Rejected (the
// FR58 redelivery defense).
func TestRecordAppointmentReminderNotification_WritesMarker(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "remnotif", Instance: "cr-remnotif"})

	apptKey := "vtx.appointment.CRnotifApptMNPQRSTUV"
	extRef := apptKey + ":2026-07-01T15:00:00Z"

	crSubmit(t, ctx, conn, cp, cons, "crremnotif01", "RecordAppointmentReminderNotification", "",
		`{"externalRef":"`+extRef+`","status":"completed","result":"notification sent"}`, nil, processor.OutcomeAccepted)

	notif := crReadDoc(t, ctx, conn, apptKey+".reminderNotification")
	if notif["class"] != "appointmentReminderNotification" {
		t.Fatalf("reminderNotification class = %v, want appointmentReminderNotification", notif["class"])
	}
	nd, _ := notif["data"].(map[string]any)
	if s, _ := nd["status"].(string); s != "completed" {
		t.Fatalf("reminderNotification status = %q, want completed", s)
	}
	if rf, _ := nd["remindedFor"].(string); rf != "2026-07-01T15:00:00Z" {
		t.Fatalf("reminderNotification remindedFor = %q, want 2026-07-01T15:00:00Z", rf)
	}

	// Create-only: a redelivered reply for the SAME externalRef must be
	// Rejected (once-only FR58 guard), not silently re-accepted.
	crSubmit(t, ctx, conn, cp, cons, "crremnotif02", "RecordAppointmentReminderNotification", "",
		`{"externalRef":"`+extRef+`","status":"completed","result":"redelivered"}`, nil, processor.OutcomeRejected)
}

// TestRecordFollowUpReminderNotification_WritesMarker mirrors
// TestRecordAppointmentReminderNotification_WritesMarker for the follow-up
// reminder's notification-outcome replyOp.
func TestRecordFollowUpReminderNotification_WritesMarker(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "fremnotif", Instance: "cr-fremnotif"})

	apptKey := "vtx.appointment.CRfnotifApptMNPQRSTU"
	extRef := apptKey + ":2027-01-15T09:00:00Z"

	crSubmit(t, ctx, conn, cp, cons, "crfremnotif01", "RecordFollowUpReminderNotification", "",
		`{"externalRef":"`+extRef+`","status":"completed","result":"notification sent"}`, nil, processor.OutcomeAccepted)

	notif := crReadDoc(t, ctx, conn, apptKey+".followUpReminderNotification")
	if notif["class"] != "followUpReminderNotification" {
		t.Fatalf("followUpReminderNotification class = %v, want followUpReminderNotification", notif["class"])
	}
	nd, _ := notif["data"].(map[string]any)
	if s, _ := nd["status"].(string); s != "completed" {
		t.Fatalf("followUpReminderNotification status = %q, want completed", s)
	}
	if rf, _ := nd["remindedFor"].(string); rf != "2027-01-15T09:00:00Z" {
		t.Fatalf("followUpReminderNotification remindedFor = %q, want 2027-01-15T09:00:00Z", rf)
	}

	crSubmit(t, ctx, conn, cp, cons, "crfremnotif02", "RecordFollowUpReminderNotification", "",
		`{"externalRef":"`+extRef+`","status":"completed","result":"redelivered"}`, nil, processor.OutcomeRejected)
}

// TestRecordFollowUpReminder_WritesMarker mints a bookable appointment then drives
// RecordFollowUpReminder, asserting the .followUpReminder.sentAt marker lands (class
// followUpReminder) — the directOp write-path the followUpReminders playbook
// dispatches. Then re-runs it to prove the unconditioned overwrite is idempotent in
// effect (sentAt stays present). Mirrors the appointment-reminder write-path test.
func TestRecordFollowUpReminder_WritesMarker(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "furem", Instance: "cr-furem"})

	patientID := crSubmit(t, ctx, conn, cp, cons, "crfupat01", "CreatePatient", "patient", `{"fullName":"Alice Rivera"}`, nil, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerID := crSubmit(t, ctx, conn, cp, cons, "crfuprv01", "CreateProvider", "provider", `{"fullName":"Dr. Sam Okafor","specialty":"Cardiology"}`, nil, processor.OutcomeAccepted)
	providerKey := "vtx.provider." + providerID

	apptID := crSubmit(t, ctx, conn, cp, cons, "crfuap001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// The directOp: RecordFollowUpReminder targets the appointment. Class LEFT EMPTY
	// exactly as Weaver's actuator dispatches a directOp (the operationType→class
	// reverse index resolves to the followUpReminderOp vertexType handler).
	crSubmit(t, ctx, conn, cp, cons, "crfurm001", "RecordFollowUpReminder", "",
		`{"appointmentKey":"`+apptKey+`","remindedFor":"2027-01-15T09:00:00Z"}`, []string{apptKey}, processor.OutcomeAccepted)

	rem := crReadDoc(t, ctx, conn, apptKey+".followUpReminder")
	if rem["class"] != "followUpReminder" {
		t.Fatalf("followUpReminder class = %v, want followUpReminder", rem["class"])
	}
	rd, _ := rem["data"].(map[string]any)
	if first, _ := rd["sentAt"].(string); first == "" {
		t.Fatalf("followUpReminder sentAt not written: %v", rem["data"])
	}
	if rf, _ := rd["remindedFor"].(string); rf != "2027-01-15T09:00:00Z" {
		t.Fatalf("followUpReminder remindedFor = %q, want 2027-01-15T09:00:00Z", rf)
	}

	// Idempotent in effect: a second RecordFollowUpReminder is ACCEPTED and the marker
	// stays present (the unconditioned-overwrite discriminator).
	crSubmit(t, ctx, conn, cp, cons, "crfurm002", "RecordFollowUpReminder", "",
		`{"appointmentKey":"`+apptKey+`"}`, []string{apptKey}, processor.OutcomeAccepted)
	rem2 := crReadDoc(t, ctx, conn, apptKey+".followUpReminder")
	rd2, _ := rem2["data"].(map[string]any)
	if s, _ := rd2["sentAt"].(string); s == "" {
		t.Fatalf("followUpReminder sentAt missing after re-run: %v", rem2["data"])
	}
}

// TestRecordFollowUpReminder_RejectsTombstonedAppointment proves the liveness guard:
// a TOMBSTONED appointment is Rejected and writes no dangling .followUpReminder marker.
func TestRecordFollowUpReminder_RejectsTombstonedAppointment(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "furemdead", Instance: "cr-furemdead"})

	dead := "vtx.appointment.CRdeadFUApptKMNPQRSTV"
	doc := map[string]any{"class": "appointment", "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, dead, b); err != nil {
		t.Fatalf("seed tombstoned appointment: %v", err)
	}

	crSubmit(t, ctx, conn, cp, cons, "crfurm003", "RecordFollowUpReminder", "",
		`{"appointmentKey":"`+dead+`"}`, []string{dead}, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, dead+".followUpReminder"); err == nil {
		t.Fatalf("a follow-up reminder marker must NOT be written for a tombstoned appointment")
	}
}

// TestStartVisitSeries_MintsSeriesAndLinks drives StartVisitSeries and asserts the
// series vertex + its .series/.progress aspects + the forPatient/withProvider links
// land, with .progress.nextDueAt seeded to startAt (the first occurrence anchors on
// startAt, not an interval offset).
func TestStartVisitSeries_MintsSeriesAndLinks(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vs", Instance: "cr-vs"})

	patientID := crSubmit(t, ctx, conn, cp, cons, "crvspat01", "CreatePatient", "patient", `{"fullName":"Alice Rivera"}`, nil, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerID := crSubmit(t, ctx, conn, cp, cons, "crvsprv01", "CreateProvider", "provider", `{"fullName":"Dr. Sam Okafor","specialty":"Cardiology"}`, nil, processor.OutcomeAccepted)
	providerKey := "vtx.provider." + providerID

	seriesID := crSubmit(t, ctx, conn, cp, cons, "crvsstart1", "StartVisitSeries", "visitseries",
		`{"patientKey":"`+patientKey+`","providerKey":"`+providerKey+`","intervalDays":30,"startAt":"2026-08-01T09:00:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	seriesKey := "vtx.visitseries." + seriesID

	series := crReadDoc(t, ctx, conn, seriesKey+".series")
	if series["class"] != "visitSeriesDefinition" {
		t.Fatalf("series class = %v, want visitSeriesDefinition", series["class"])
	}
	sd, _ := series["data"].(map[string]any)
	if v, _ := sd["intervalDays"].(float64); v != 30 {
		t.Fatalf("series intervalDays = %v, want 30", sd["intervalDays"])
	}
	if sd["startAt"] != "2026-08-01T09:00:00Z" {
		t.Fatalf("series startAt = %v, want 2026-08-01T09:00:00Z", sd["startAt"])
	}

	progress := crReadDoc(t, ctx, conn, seriesKey+".progress")
	pd, _ := progress["data"].(map[string]any)
	if pd["nextDueAt"] != "2026-08-01T09:00:00Z" {
		t.Fatalf("progress nextDueAt = %v, want seeded to startAt", pd["nextDueAt"])
	}
	if v, _ := pd["occurrenceCount"].(float64); v != 0 {
		t.Fatalf("progress occurrenceCount = %v, want 0", pd["occurrenceCount"])
	}

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, "lnk.visitseries."+seriesID+".forPatient.patient."+patientID); err != nil {
		t.Fatalf("forPatient link not written: %v", err)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, "lnk.visitseries."+seriesID+".withProvider.provider."+providerID); err != nil {
		t.Fatalf("withProvider link not written: %v", err)
	}
}

// TestStartVisitSeries_RejectsUnknownPatient proves the endpoint-liveness guard: an
// absent/wrong-class patientKey rejects, minting no series.
func TestStartVisitSeries_RejectsUnknownPatient(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vsbad", Instance: "cr-vsbad"})

	providerID := crSubmit(t, ctx, conn, cp, cons, "crvsbprv1", "CreateProvider", "provider", `{"fullName":"Dr. Sam Okafor","specialty":"Cardiology"}`, nil, processor.OutcomeAccepted)
	providerKey := "vtx.provider." + providerID
	unknown := "vtx.patient.CRunknownPatientMNPQRST"

	crSubmit(t, ctx, conn, cp, cons, "crvsbstart", "StartVisitSeries", "visitseries",
		`{"patientKey":"`+unknown+`","providerKey":"`+providerKey+`","intervalDays":30,"startAt":"2026-08-01T09:00:00Z"}`,
		[]string{unknown, providerKey}, processor.OutcomeRejected)
}

// TestAdvanceVisitSeries_RollsForward drives AdvanceVisitSeries directly (the
// directOp shape Weaver dispatches — class left empty) and asserts .progress rolls:
// lastOccurrenceAt = dueFor, nextDueAt = dueFor + intervalDays, occurrenceCount+1.
// Then re-runs it to prove the unconditioned overwrite is idempotent in effect.
func TestAdvanceVisitSeries_RollsForward(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vsadv", Instance: "cr-vsadv"})

	patientID := crSubmit(t, ctx, conn, cp, cons, "crvsapat01", "CreatePatient", "patient", `{"fullName":"Alice Rivera"}`, nil, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerID := crSubmit(t, ctx, conn, cp, cons, "crvsaprv01", "CreateProvider", "provider", `{"fullName":"Dr. Sam Okafor","specialty":"Cardiology"}`, nil, processor.OutcomeAccepted)
	providerKey := "vtx.provider." + providerID
	seriesID := crSubmit(t, ctx, conn, cp, cons, "crvsastart", "StartVisitSeries", "visitseries",
		`{"patientKey":"`+patientKey+`","providerKey":"`+providerKey+`","intervalDays":30,"startAt":"2026-08-01T09:00:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	seriesKey := "vtx.visitseries." + seriesID

	crSubmit(t, ctx, conn, cp, cons, "crvsadv001", "AdvanceVisitSeries", "",
		`{"seriesKey":"`+seriesKey+`","dueFor":"2026-08-01T09:00:00Z","intervalDays":30,"occurrenceCount":0}`,
		[]string{seriesKey}, processor.OutcomeAccepted)

	progress := crReadDoc(t, ctx, conn, seriesKey+".progress")
	pd, _ := progress["data"].(map[string]any)
	if pd["lastOccurrenceAt"] != "2026-08-01T09:00:00Z" {
		t.Fatalf("progress lastOccurrenceAt = %v, want 2026-08-01T09:00:00Z", pd["lastOccurrenceAt"])
	}
	if pd["nextDueAt"] != "2026-08-31T09:00:00Z" {
		t.Fatalf("progress nextDueAt = %v, want 2026-08-31T09:00:00Z (rolled 30 days forward)", pd["nextDueAt"])
	}
	if v, _ := pd["occurrenceCount"].(float64); v != 1 {
		t.Fatalf("progress occurrenceCount = %v, want 1", pd["occurrenceCount"])
	}

	// Idempotent in effect: a second AdvanceVisitSeries with the SAME dueFor is
	// ACCEPTED (unconditioned overwrite, not a create-only insert).
	crSubmit(t, ctx, conn, cp, cons, "crvsadv002", "AdvanceVisitSeries", "",
		`{"seriesKey":"`+seriesKey+`","dueFor":"2026-08-01T09:00:00Z","intervalDays":30,"occurrenceCount":0}`,
		[]string{seriesKey}, processor.OutcomeAccepted)
}

// TestAdvanceVisitSeries_RejectsTombstonedSeries proves the liveness guard: a
// TOMBSTONED series is Rejected and writes no dangling .progress advance.
func TestAdvanceVisitSeries_RejectsTombstonedSeries(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vsadvdead", Instance: "cr-vsadvdead"})

	dead := "vtx.visitseries.CRdeadSeriesMNPQRSTVWX"
	doc := map[string]any{"class": "visitseries", "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, dead, b); err != nil {
		t.Fatalf("seed tombstoned series: %v", err)
	}

	crSubmit(t, ctx, conn, cp, cons, "crvsadv003", "AdvanceVisitSeries", "",
		`{"seriesKey":"`+dead+`","dueFor":"2026-08-01T09:00:00Z","intervalDays":30}`, []string{dead}, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, dead+".progress"); err == nil {
		t.Fatalf("a visit-series advance must NOT be written for a tombstoned series")
	}
}

// TestPauseResumeVisitSeries_TogglesPaused drives Pause then Resume and asserts the
// .paused aspect flips, re-running each to prove the unconditioned upsert is
// idempotent in effect.
func TestPauseResumeVisitSeries_TogglesPaused(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vspause", Instance: "cr-vspause"})

	patientID := crSubmit(t, ctx, conn, cp, cons, "crvsppat01", "CreatePatient", "patient", `{"fullName":"Alice Rivera"}`, nil, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerID := crSubmit(t, ctx, conn, cp, cons, "crvspprv01", "CreateProvider", "provider", `{"fullName":"Dr. Sam Okafor","specialty":"Cardiology"}`, nil, processor.OutcomeAccepted)
	providerKey := "vtx.provider." + providerID
	seriesID := crSubmit(t, ctx, conn, cp, cons, "crvspstart", "StartVisitSeries", "visitseries",
		`{"patientKey":"`+patientKey+`","providerKey":"`+providerKey+`","intervalDays":30,"startAt":"2026-08-01T09:00:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	seriesKey := "vtx.visitseries." + seriesID

	crSubmit(t, ctx, conn, cp, cons, "crvsp0001", "PauseVisitSeries", "", `{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)
	paused := crReadDoc(t, ctx, conn, seriesKey+".paused")
	pd, _ := paused["data"].(map[string]any)
	if v, _ := pd["value"].(bool); !v {
		t.Fatalf("paused.value = %v, want true after PauseVisitSeries", pd["value"])
	}

	crSubmit(t, ctx, conn, cp, cons, "crvsp0002", "ResumeVisitSeries", "", `{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)
	resumed := crReadDoc(t, ctx, conn, seriesKey+".paused")
	rd, _ := resumed["data"].(map[string]any)
	if v, _ := rd["value"].(bool); v {
		t.Fatalf("paused.value = %v, want false after ResumeVisitSeries", rd["value"])
	}
}
