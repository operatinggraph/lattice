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
// clinic-reminders op.
var crOps = []string{"CreatePatient", "CreateProvider", "CreateAppointment", "RecordAppointmentReminder"}

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
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
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

func crSubmit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, op, class, payload string, reads []string, want processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: op,
		Actor:         crStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
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
