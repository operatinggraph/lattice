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
// Coverage spans the full lifecycle (create / reschedule / status transitions /
// tombstone) plus the write-path 15-minute-grid slot-claim double-book mechanism
// (SlotConflict / PatientDoubleBook / SlotGridViolation / AppointmentTooLong), the
// provider hours/time-off guards, the past-time guard, and the authorization /
// wrong-class / dead-endpoint rejection paths — see the per-test doc comments below.
package clinicdomain_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	"github.com/operatinggraph/lattice/internal/vault"
	clinicdomain "github.com/operatinggraph/lattice/packages/clinic-domain"
	locationdomain "github.com/operatinggraph/lattice/packages/location-domain"
)

const (
	clStaffActorID   = "CLstaffActHJKMNPQRST"
	clStaffActorKey  = "vtx.identity." + clStaffActorID
	clStaffCapKey    = "cap.identity." + clStaffActorID
	clConsumerID     = "CLconsumerHJKMNPQRST"
	clConsumerKey    = "vtx.identity." + clConsumerID
	clConsumerCapKey = "cap.identity." + clConsumerID

	// clConsumerRoleID stands in for identity-domain's real `consumer` role
	// NanoID: this package's tests don't install identity-domain (only rbac +
	// hygiene via SetupPackageTestEnv), so clinic-domain's own CreateAppointment
	// scope=self grant (GrantsTo: "consumer") needs a role id registered directly
	// (the lease-signing lsConsumerRoleID idiom).
	clConsumerRoleID = "CLConsumerRoZeHJKMNP"
)

// clinicOps are the ops the staff actor needs.
var clinicOps = []string{
	"CreatePatient", "TombstonePatient", "BackfillPatientRegistration",
	"CreateProvider", "TombstoneProvider", "SetProviderProfile", "SetProviderHours", "SetProviderTimeOff",
	"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "CorrectAppointmentStatus", "MarkPastDueNoShow", "BackfillAppointmentSite", "SetAppointmentSite", "RecordEncounter", "TombstoneAppointment",
	"SetSiteProfile", "AssignProviderSite", "RemoveProviderSite",
	// location-domain ops the multi-site tests need to mint a building directly
	// (mirrors loftspace-domain's setupLoftspaceEnv installing location-domain
	// and granting CreateLocation to the staff actor).
	"CreateLocation",
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
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateAppointment", Scope: "self"},
			{OperationType: "RescheduleAppointment", Scope: "self"},
			{OperationType: "SetAppointmentStatus", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

func setupClinicEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // installs rbac+identity+hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID, "consumer": clConsumerRoleID, "frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"), "backOfHouse": pkgmgr.RoleID("identity-domain", "backOfHouse"), "provider": pkgmgr.RoleID("identity-domain", "provider")}
	if _, err := inst.Install(ctx, locationdomain.Package); err != nil {
		stop()
		t.Fatalf("install location-domain: %v", err)
	}
	if _, err := inst.Install(ctx, clinicdomain.Package); err != nil {
		stop()
		t.Fatalf("install clinic-domain: %v", err)
	}
	stop()
	testutil.SeedCapDoc(t, ctx, conn, clStaffCapDoc())
	// The operator grant is only half the claim — the workplace-confinement
	// guard reads the holdsRole LINK to decide whether its caller is root.
	testutil.SeedHoldsRole(t, ctx, conn, clStaffActorKey, bootstrap.RoleOperatorKey)
	testutil.SeedCapDoc(t, ctx, conn, clConsumerCapDoc())
	return ctx, conn
}

// clCreateBuilding submits CreateLocation(building) and returns the minted
// building key — the multi-site tests' location-domain endpoint.
func clCreateBuilding(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label string) string {
	t.Helper()
	id := clSubmit(t, ctx, conn, cp, cons, label, "CreateLocation", "building",
		`{"locationType":"building"}`, nil, processor.OutcomeAccepted)
	return "vtx.building." + id
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

func clSeedLink(t *testing.T, ctx context.Context, conn *substrate.Conn, key, source, target, class, localName string) {
	t.Helper()
	doc := map[string]any{
		"class": class, "isDeleted": false,
		"sourceVertex": source, "targetVertex": target,
		"localName": localName, "data": map[string]any{},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed link %s: %v", key, err)
	}
}

// clSeedLease seeds a leaseapp vertex and, when applicantID is non-empty, its
// applicationFor link — the residency check CreateAppointment reads
// (residentVisit, mirroring wellness-domain's residentRate). withTenancy
// additionally stamps the .tenancy aspect DecideLeaseApplication's FIRST
// approve writes — CreateAppointment requires its presence (not just a live
// applicationFor link) before writing a residentVisit link, so a pending or
// declined application (link alive, no .tenancy) must fall back to no link.
func clSeedLease(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseID, applicantID string, withTenancy bool) string {
	t.Helper()
	key := "vtx.leaseapp." + leaseID
	clSeedVertex(t, ctx, conn, key, "leaseapp", false)
	if applicantID != "" {
		lnk := "lnk.leaseapp." + leaseID + ".applicationFor.identity." + applicantID
		clSeedLink(t, ctx, conn, lnk, key, "vtx.identity."+applicantID, "applicationFor", "applicationFor")
	}
	if withTenancy {
		clSeedVertex(t, ctx, conn, key+".tenancy", "tenancy", false)
	}
	return key
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

// clDecryptEncounter reads an appointment's SENSITIVE .encounter aspect and
// decrypts it via the shared TestVault against the package's own clinicalRecord
// retention-class holder — the DEK RecordEncounter's write custodies on (custody
// kind retentionClass, never the patient's identity). Mirrors identity-domain's
// readDecryptedAspectData, generalized to a non-identity holder key.
func clDecryptEncounter(t *testing.T, ctx context.Context, conn *substrate.Conn, apptKey string) map[string]any {
	t.Helper()
	return clDecryptAspect(t, ctx, conn, pkgmgr.RetentionClassKey("clinic-domain", "clinicalRecord"), apptKey+".encounter")
}

// clDecryptAspect opens any sensitive aspect under the key holder that custodies
// it — the retention-class holder for the clinical record, the identity itself
// for an identity-custodied aspect like a person's .name.
func clDecryptAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, holderKey, aspectKey string) map[string]any {
	t.Helper()
	envEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, holderKey+".piiKey")
	if err != nil {
		t.Fatalf("KVGet %s: %v", holderKey+".piiKey", err)
	}
	var envDoc struct {
		Data vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(envEntry.Value, &envDoc); err != nil {
		t.Fatalf("unmarshal %s: %v", holderKey+".piiKey", err)
	}

	ctEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, aspectKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", aspectKey, err)
	}
	var ctDoc struct {
		Data vault.Ciphertext `json:"data"`
	}
	if err := json.Unmarshal(ctEntry.Value, &ctDoc); err != nil {
		t.Fatalf("unmarshal %s: %v", aspectKey, err)
	}

	v := testutil.TestVault(t)
	plaintext, err := v.Decrypt(ctx, holderKey, envDoc.Data, ctDoc.Data)
	if err != nil {
		t.Fatalf("decrypt %s: %v", aspectKey, err)
	}
	var value map[string]any
	if err := json.Unmarshal(plaintext, &value); err != nil {
		t.Fatalf("unmarshal decrypted %s: %v", aspectKey, err)
	}
	return value
}

func clMissing(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	_, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	return err != nil
}

// clSlotCellCode derives the deterministic slot-claim localName suffix for a
// canonical whole-second UTC cell-start instant, mirroring the package's
// slot_cellcode Starlark helper (strip '-'/':' and lowercase).
func clSlotCellCode(cellStart string) string {
	s := strings.ReplaceAll(cellStart, "-", "")
	s = strings.ReplaceAll(s, ":", "")
	return strings.ToLower(s)
}

// clSlotClaimKey builds a hub's slot-claim aspect key for the 15-minute cell
// starting at cellStart (a canonical whole-second UTC instant, e.g. "09:00" or
// "09:15" — every appointment boundary in this suite sits on a cell edge).
func clSlotClaimKey(hubKey, cellStart string) string {
	return hubKey + ".slot" + clSlotCellCode(cellStart)
}

// clAssertSlotClaimLive asserts the hub holds a LIVE slot-claim aspect for the
// given 15-minute cell — the write-path double-book lock (§2.1 of
// clinic-booking-write-path-slot-claims-design.md) is engaged for that cell.
func clAssertSlotClaimLive(t *testing.T, ctx context.Context, conn *substrate.Conn, hubKey, cellStart string) {
	t.Helper()
	k := clSlotClaimKey(hubKey, cellStart)
	doc := clReadDoc(t, ctx, conn, k)
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("slot claim %s should be LIVE, got isDeleted=true", k)
	}
}

// clAssertSlotClaimReleased asserts the hub's slot-claim aspect for the given
// 15-minute cell is tombstoned (or was never written) — the cell is free for a
// competing booking to claim.
func clAssertSlotClaimReleased(t *testing.T, ctx context.Context, conn *substrate.Conn, hubKey, cellStart string) {
	t.Helper()
	k := clSlotClaimKey(hubKey, cellStart)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, k)
	if err != nil {
		return // never claimed == released
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", k, err)
	}
	if del, _ := doc["isDeleted"].(bool); !del {
		t.Fatalf("slot claim %s should be RELEASED (tombstoned), got isDeleted=%v", k, del)
	}
}

// clSubmittedAnchor pins the op envelope's submittedAt to a fixed instant so the
// suite is deterministic under the appointment ops' past-time guard (a startsAt at
// or before submittedAt is rejected ScheduleInPast). Every appointment in these
// tests uses a fixed 2026-07/08/09 startsAt — all strictly after this anchor — so
// they stay valid forever regardless of the wall clock; the ScheduleInPast path is
// exercised explicitly with pre-anchor dates in TestClinic_PastTimeRejected.
const clSubmittedAnchor = "2026-01-01T00:00:00Z"

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
		SubmittedAt:   clSubmittedAnchor,
		Class:         class,
		Payload:       json.RawMessage(payload),
	}
	enums := testutil.DeclaredEnumerations(op, clStaffActorKey, clinicdomain.OpMetas())
	if len(reads) > 0 || len(enums) > 0 {
		env.ContextHint = &processor.ContextHint{Reads: reads, Enumerations: enums}
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return clNanoIDFromRequestID(reqID)
}

// clSubmitOpt is clSubmit plus an explicit optionalReads — for op dispatchers
// (SetAppointmentStatus) whose declared read posture mixes required Reads with
// a legitimate-absence OptionalReads.
func clSubmitOpt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, op, class, payload string, reads, optionalReads []string, want processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: op,
		Actor:         clStaffActorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         class,
		Payload:       json.RawMessage(payload),
	}
	enums := testutil.DeclaredEnumerations(op, clStaffActorKey, clinicdomain.OpMetas())
	if len(reads) > 0 || len(optionalReads) > 0 || len(enums) > 0 {
		env.ContextHint = &processor.ContextHint{Reads: reads, OptionalReads: optionalReads, Enumerations: enums}
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return clNanoIDFromRequestID(reqID)
}

// clRescheduleReads returns RescheduleAppointment's required Reads — the
// appointment + its .schedule + the withProvider/forPatient endpoint-
// validation links (app.js submitReschedule, script-read-posture-design.md
// §13).
func clRescheduleReads(apptKey, providerKey, patientKey string) []string {
	apptID := apptKey[len("vtx.appointment."):]
	providerID := providerKey[len("vtx.provider."):]
	patientID := patientKey[len("vtx.patient."):]
	return []string{
		apptKey, apptKey + ".schedule",
		"lnk.appointment." + apptID + ".withProvider.provider." + providerID,
		"lnk.appointment." + apptID + ".forPatient.patient." + patientID,
	}
}

// clStatusReads returns the Reads/OptionalReads pair SetAppointmentStatus's
// dispatcher declares (app.js setStatus): .status is always an optionalRead
// (absence is the legitimate first-set case); the appointment's .schedule +
// the withProvider/forPatient links are additionally REQUIRED only on a
// terminal-status transition (the branch that releases the held slot-claim
// cells) — the same condition the FE gates provider/patient submission on.
func clStatusReads(apptKey string, terminal bool, providerKey, patientKey string) (reads, optionalReads []string) {
	reads = []string{apptKey}
	optionalReads = []string{apptKey + ".status"}
	if terminal {
		apptID := apptKey[len("vtx.appointment."):]
		providerID := providerKey[len("vtx.provider."):]
		patientID := patientKey[len("vtx.patient."):]
		reads = append(reads, apptKey+".schedule",
			"lnk.appointment."+apptID+".withProvider.provider."+providerID,
			"lnk.appointment."+apptID+".forPatient.patient."+patientID)
	}
	return reads, optionalReads
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
	t.Parallel()
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
	// The 30-minute booking covers two 15-minute cells (15:00, 15:15); both hubs
	// hold a LIVE slot-claim aspect for each — the write-path double-book lock.
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-01T15:00:00Z")
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-01T15:15:00Z")
	clAssertSlotClaimLive(t, ctx, conn, patientKey, "2026-07-01T15:00:00Z")
	clAssertSlotClaimLive(t, ctx, conn, patientKey, "2026-07-01T15:15:00Z")
}

// TestClinic_SetProviderProfile proves SetProviderProfile edits an existing
// provider's .profile through the real Processor: it REPLACES the aspect with the
// supplied fields (an omitted credentials/bio is DROPPED, not merged — the same
// full-replace semantics SetProviderHours/SetProviderTimeOff use), keeps fullName +
// specialty required (the roster lens keys on fullName), and rejects an unknown
// provider.
func TestClinic_SetProviderProfile(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "set-profile")

	providerKey := createProvider(t, ctx, conn, cp, cons, "spprv0001", "Dr. Sam Okafor", "Cardiology")

	// Edit: new name + specialty + credentials + bio → accepted, all four land.
	clSubmit(t, ctx, conn, cp, cons, "spedit001", "SetProviderProfile", "provider",
		`{"providerKey":"`+providerKey+`","fullName":"Dr. Samira Okafor","specialty":"Interventional Cardiology","credentials":"MD, FACC","bio":"Structural heart"}`,
		[]string{providerKey}, processor.OutcomeAccepted)
	prof := clReadDoc(t, ctx, conn, providerKey+".profile")
	if prof["class"] != "providerProfile" {
		t.Fatalf("profile class = %v, want providerProfile", prof["class"])
	}
	pd, _ := prof["data"].(map[string]any)
	if pd["fullName"] != "Dr. Samira Okafor" || pd["specialty"] != "Interventional Cardiology" ||
		pd["credentials"] != "MD, FACC" || pd["bio"] != "Structural heart" {
		t.Fatalf("edited profile = %v", prof["data"])
	}

	// Full-replace semantics: omit credentials + bio → they are DROPPED.
	clSubmit(t, ctx, conn, cp, cons, "spedit002", "SetProviderProfile", "provider",
		`{"providerKey":"`+providerKey+`","fullName":"Dr. Samira Okafor","specialty":"Cardiology"}`,
		[]string{providerKey}, processor.OutcomeAccepted)
	pd2, _ := clReadDoc(t, ctx, conn, providerKey+".profile")["data"].(map[string]any)
	if pd2["specialty"] != "Cardiology" {
		t.Fatalf("replaced specialty = %v, want Cardiology", pd2["specialty"])
	}
	if _, ok := pd2["credentials"]; ok {
		t.Fatalf("credentials must be dropped on a full-replace edit, got %v", pd2["credentials"])
	}
	if _, ok := pd2["bio"]; ok {
		t.Fatalf("bio must be dropped on a full-replace edit, got %v", pd2["bio"])
	}

	// Missing fullName → rejected (required).
	clSubmit(t, ctx, conn, cp, cons, "spbad001", "SetProviderProfile", "provider",
		`{"providerKey":"`+providerKey+`","specialty":"Cardiology"}`,
		[]string{providerKey}, processor.OutcomeRejected)

	// Unknown provider → rejected.
	clSubmit(t, ctx, conn, cp, cons, "spbad002", "SetProviderProfile", "provider",
		`{"providerKey":"vtx.provider.spnope0000000000000","fullName":"Ghost","specialty":"None"}`,
		[]string{"vtx.provider.spnope0000000000000"}, processor.OutcomeRejected)
}

// TestClinic_DoubleBookRejected proves write-path double-book rejection: CreateAppointment
// claims a deterministic providerSlotClaim aspect per covered 15-minute grid cell, and a
// live claim on any covered cell rejects the whole booking (SlotConflict), while disjoint /
// back-to-back cells, a different provider, and a freed (cancelled / tombstoned) slot are
// all accepted. Also proves the endsAt>startsAt guard. The write-path CreateOnly /
// expectedRevision conditioning that makes two truly concurrent claims for the same cell
// collide is the Processor's own atomic-batch guarantee (Contract #2 §2.5), exercised at
// the substrate/processor level, not re-proven here — this suite proves the FUNCTIONAL
// claim/release behavior via sequential submissions, exactly as the retired mechanism's
// suite did.
func TestClinic_DoubleBookRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "double-book")

	patientKey := createPatient(t, ctx, conn, cp, cons, "dbpat0001", "Dana Booker")
	patientKey2 := createPatient(t, ctx, conn, cp, cons, "dbpat0002", "Other Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "dbprv0001", "Dr. Vale", "Cardiology")
	providerKey2 := createProvider(t, ctx, conn, cp, cons, "dbprv0002", "Dr. West", "Pediatrics")

	mkAppt := func(label, patient, prov, start, end string, want processor.MessageOutcome) string {
		return clSubmit(t, ctx, conn, cp, cons, label, "CreateAppointment", "appointment",
			`{"patient":"`+patient+`","provider":"`+prov+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			[]string{patient, prov}, want)
	}

	// 1. First booking 10:00–10:30 (cells 10:00, 10:15) → accepted.
	a1 := mkAppt("dbappt0001", patientKey, providerKey, "2026-08-01T10:00:00Z", "2026-08-01T10:30:00Z", processor.OutcomeAccepted)
	// 2. Overlapping 10:15–10:45 (cells 10:15, 10:30) shares cell 10:15 with a1, same
	//    provider → SlotConflict (rejected).
	mkAppt("dbappt0002", patientKey, providerKey, "2026-08-01T10:15:00Z", "2026-08-01T10:45:00Z", processor.OutcomeRejected)
	// 3. Fully containing 09:45–10:45 (cells 09:45, 10:00, 10:15, 10:30) also shares
	//    cells with a1 → rejected.
	mkAppt("dbappt0003", patientKey, providerKey, "2026-08-01T09:45:00Z", "2026-08-01T10:45:00Z", processor.OutcomeRejected)
	// 4. Disjoint 11:00–11:30 (cells 11:00, 11:15) with the same provider → accepted.
	a4 := mkAppt("dbappt0004", patientKey, providerKey, "2026-08-01T11:00:00Z", "2026-08-01T11:30:00Z", processor.OutcomeAccepted)
	// 5. Back-to-back 10:30–11:00 (cells 10:30, 10:45) — disjoint from a1's {10:00,
	//    10:15} and a4's {11:00, 11:15}, no shared cell → accepted.
	mkAppt("dbappt0005", patientKey, providerKey, "2026-08-01T10:30:00Z", "2026-08-01T11:00:00Z", processor.OutcomeAccepted)
	// 6. Same 10:00–10:30 cells but a DIFFERENT provider AND a DIFFERENT patient → the
	//    claim is per-hub-per-cell, so neither hub's claim collides → accepted. (The
	//    original patient at this exact slot would now be a PatientDoubleBook — see
	//    TestClinic_PatientDoubleBook.)
	clSubmit(t, ctx, conn, cp, cons, "dbappt0006", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey2+`","provider":"`+providerKey2+`","startsAt":"2026-08-01T10:00:00Z","endsAt":"2026-08-01T10:30:00Z"}`,
		[]string{patientKey2, providerKey2}, processor.OutcomeAccepted)

	// 7. endsAt <= startsAt → InvalidArgument (rejected), no cell math needed.
	mkAppt("dbappt0007", patientKey, providerKey, "2026-08-01T13:00:00Z", "2026-08-01T13:00:00Z", processor.OutcomeRejected)

	// 8. Cancel a1, then re-book its 10:00–10:30 cells → the cancelled appointment's
	//    claims were released on the terminal transition, so the cells are free → accepted.
	a1Key := "vtx.appointment." + a1
	{
		reads, optionalReads := clStatusReads(a1Key, true, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "dbcancel001", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+a1Key+`","status":"cancelled","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			reads, optionalReads, processor.OutcomeAccepted)
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-08-01T10:00:00Z")
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-08-01T10:15:00Z")
	mkAppt("dbappt0008", patientKey, providerKey, "2026-08-01T10:00:00Z", "2026-08-01T10:30:00Z", processor.OutcomeAccepted)
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-08-01T10:00:00Z")

	// 9. Tombstone a4 (11:00–11:30), then re-book that slot → the hard tombstone
	//    released a4's held cells too → accepted.
	a4Key := "vtx.appointment." + a4
	// read-posture (a): the appointment's .schedule (required for cell release) +
	// the withProvider/forPatient endpoint-validation links (script-read-posture-
	// design.md §13) — this test is TombstoneAppointment's only dispatcher.
	clSubmit(t, ctx, conn, cp, cons, "dbtomb0001", "TombstoneAppointment", "appointment",
		`{"appointmentKey":"`+a4Key+`","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
		[]string{
			a4Key, a4Key + ".schedule",
			"lnk.appointment." + a4 + ".withProvider.provider." + providerKey[len("vtx.provider."):],
			"lnk.appointment." + a4 + ".forPatient.patient." + patientKey[len("vtx.patient."):],
		}, processor.OutcomeAccepted)
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-08-01T11:00:00Z")
	mkAppt("dbappt0009", patientKey, providerKey, "2026-08-01T11:00:00Z", "2026-08-01T11:30:00Z", processor.OutcomeAccepted)
}

// TestClinic_TombstoneAppointmentValidatesEndpoints proves TombstoneAppointment's
// provider/patient are validated against the appointment's actual withProvider /
// forPatient links (WrongProvider / WrongPatient) before any cell is released — a
// wrong / fabricated endpoint must not release someone else's slot claims.
func TestClinic_TombstoneAppointmentValidatesEndpoints(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "tomb-validate")

	patientKey := createPatient(t, ctx, conn, cp, cons, "tvpat0001", "Val Idate")
	providerKey := createProvider(t, ctx, conn, cp, cons, "tvprv0001", "Dr. Real", "Cardiology")
	otherProvider := createProvider(t, ctx, conn, cp, cons, "tvprv0002", "Dr. Other", "Pediatrics")

	apptID := clSubmit(t, ctx, conn, cp, cons, "tvappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-08-05T10:00:00Z","endsAt":"2026-08-05T10:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// A provider that is NOT this appointment's actual provider → WrongProvider,
	// rejected before any mutation. require_matching_provider fails before the
	// script ever reads forPatient/.schedule, so only the (wrong) withProvider
	// link is a real read here (read-posture (a), script-read-posture-design.md §13).
	clSubmit(t, ctx, conn, cp, cons, "tvtomb0001", "TombstoneAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+otherProvider+`","patient":"`+patientKey+`"}`,
		[]string{
			apptKey,
			"lnk.appointment." + apptID + ".withProvider.provider." + otherProvider[len("vtx.provider."):],
		}, processor.OutcomeRejected)
	if clMissing(t, ctx, conn, apptKey) {
		t.Fatalf("appointment must survive a TombstoneAppointment with a wrong provider")
	}
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-08-05T10:00:00Z")

	// The real endpoints → accepted, cells released. read-posture (a): .schedule +
	// the withProvider/forPatient links (script-read-posture-design.md §13).
	clSubmit(t, ctx, conn, cp, cons, "tvtomb0002", "TombstoneAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
		[]string{
			apptKey, apptKey + ".schedule",
			"lnk.appointment." + apptID + ".withProvider.provider." + providerKey[len("vtx.provider."):],
			"lnk.appointment." + apptID + ".forPatient.patient." + patientKey[len("vtx.patient."):],
		}, processor.OutcomeAccepted)
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-08-05T10:00:00Z")
}

// TestClinic_PatientDoubleBook proves the patient-side double-book guard — the
// symmetric analog of the per-provider SlotConflict. A per-provider claim set alone
// cannot see a patient booked with TWO DIFFERENT providers at the same instant; the
// patient's own patientSlotClaim aspects close that. CreateAppointment AND
// RescheduleAppointment claim one per covered cell on the patient hub, and reject
// an overlap with the patient's other live claim (PatientDoubleBook), while allowing
// a different patient, a back-to-back slot, and a freed (cancelled) slot. This is
// the exact PO live-repro: one patient cannot be in two rooms at once.
func TestClinic_PatientDoubleBook(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "patient-double-book")

	patientKey := createPatient(t, ctx, conn, cp, cons, "pdpat0001", "Pat Double")
	patientKey2 := createPatient(t, ctx, conn, cp, cons, "pdpat0002", "Other Pat")
	provA := createProvider(t, ctx, conn, cp, cons, "pdprvA001", "Dr. A", "Cardiology")
	provB := createProvider(t, ctx, conn, cp, cons, "pdprvB001", "Dr. B", "Pediatrics")
	provC := createProvider(t, ctx, conn, cp, cons, "pdprvC001", "Dr. C", "GeneralPractice")

	book := func(label, patient, prov, start, end string, want processor.MessageOutcome) string {
		return clSubmit(t, ctx, conn, cp, cons, label, "CreateAppointment", "appointment",
			`{"patient":"`+patient+`","provider":"`+prov+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			[]string{patient, prov}, want)
	}

	// 1. Patient booked with provider A at 14:00–14:30 (cells 14:00, 14:15) → accepted.
	aA := book("pdappt0001", patientKey, provA, "2026-08-10T14:00:00Z", "2026-08-10T14:30:00Z", processor.OutcomeAccepted)
	// 2. SAME patient with a DIFFERENT provider B at the SAME slot → PatientDoubleBook
	//    (the exact PO repro — provider B's own book is empty, yet the patient is busy).
	book("pdappt0002", patientKey, provB, "2026-08-10T14:00:00Z", "2026-08-10T14:30:00Z", processor.OutcomeRejected)
	// 3. Same patient, provider B, partially overlapping 14:15–14:45 (shares cell
	//    14:15 with booking 1) → rejected.
	book("pdappt0003", patientKey, provB, "2026-08-10T14:15:00Z", "2026-08-10T14:45:00Z", processor.OutcomeRejected)
	// 4. Back-to-back 14:30–15:00 (cells 14:30, 14:45) with provider B — disjoint from
	//    booking 1's {14:00, 14:15} → accepted.
	book("pdappt0004", patientKey, provB, "2026-08-10T14:30:00Z", "2026-08-10T15:00:00Z", processor.OutcomeAccepted)
	// 5. Disjoint 16:00–16:30 with provider B → accepted.
	book("pdappt0005", patientKey, provB, "2026-08-10T16:00:00Z", "2026-08-10T16:30:00Z", processor.OutcomeAccepted)
	// 6. A DIFFERENT patient at the same 14:00 slot (provider C, free) → accepted: the
	//    claim is per-patient, and provider C's book is free (no SlotConflict).
	book("pdappt0006", patientKey2, provC, "2026-08-10T14:00:00Z", "2026-08-10T14:30:00Z", processor.OutcomeAccepted)

	// 7. Reschedule a provider-B appointment back onto 14:00 (where the patient is still
	//    booked with provider A) → PatientDoubleBook, even though provider B's own book
	//    is free at 14:00 (the per-provider check passes; the patient check fails).
	a7 := "vtx.appointment." + book("pdappt0007", patientKey, provB, "2026-08-10T18:00:00Z", "2026-08-10T18:30:00Z", processor.OutcomeAccepted)
	clSubmit(t, ctx, conn, cp, cons, "pdres0001", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+a7+`","provider":"`+provB+`","patient":"`+patientKey+`","startsAt":"2026-08-10T14:00:00Z","endsAt":"2026-08-10T14:30:00Z"}`,
		clRescheduleReads(a7, provB, patientKey), processor.OutcomeRejected)

	// 8. Cancel the provider-A 14:00 appointment, then the reschedule onto 14:00
	//    succeeds (the cancelled appointment's patient slot claims were released on
	//    the terminal transition).
	aAKey := "vtx.appointment." + aA
	{
		reads, optionalReads := clStatusReads(aAKey, true, provA, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "pdcancel01", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+aAKey+`","status":"cancelled","provider":"`+provA+`","patient":"`+patientKey+`"}`,
			reads, optionalReads, processor.OutcomeAccepted)
	}
	clAssertSlotClaimReleased(t, ctx, conn, patientKey, "2026-08-10T14:00:00Z")
	clSubmit(t, ctx, conn, cp, cons, "pdres0002", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+a7+`","provider":"`+provB+`","patient":"`+patientKey+`","startsAt":"2026-08-10T14:00:00Z","endsAt":"2026-08-10T14:30:00Z"}`,
		clRescheduleReads(a7, provB, patientKey), processor.OutcomeAccepted)
	clAssertSlotClaimLive(t, ctx, conn, patientKey, "2026-08-10T14:00:00Z")
}

// TestClinic_SetAppointmentStatus proves the unconditioned-upsert idiom: a
// SetAppointmentStatus overwrites the .status aspect in place (scheduled→confirmed).
func TestClinic_SetAppointmentStatus(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0002", "Bob Tenant")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0002", "Dr. Lee", "Dermatology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0002", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-02T09:00:00Z","endsAt":"2026-07-02T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	clSubmitOpt(t, ctx, conn, cp, cons, "setstat0001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"confirmed"}`, []string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeAccepted)

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
// audit note is recorded on the .status aspect. A later noteless transition clears
// the note (the note belongs to the transition it was recorded with). The flow uses
// only NON-terminal statuses so the note record/clear mechanism is exercised
// independently of the terminal-status guard (see TestClinic_TerminalStatusGuard).
func TestClinic_StatusCheckedInAndNote(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status-note")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0009", "Pat Note")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0009", "Dr. Note", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0009", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-09T09:00:00Z","endsAt":"2026-07-09T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// checkedIn is a valid status (the new active state).
	clSubmitOpt(t, ctx, conn, cp, cons, "setchkin001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"checkedIn"}`, []string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeAccepted)
	status := clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ := status["data"].(map[string]any)
	if st["value"] != "checkedIn" {
		t.Fatalf("status = %v, want checkedIn", st["value"])
	}
	if _, hasNote := st["note"]; hasNote {
		t.Fatalf("a noteless transition must carry no note; got %v", st["note"])
	}

	// A transition with an audit note records the note on .status (a non-terminal
	// state — the note mechanism is independent of the status value).
	clSubmitOpt(t, ctx, conn, cp, cons, "setconf0001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"confirmed","note":"rescheduled by phone"}`, []string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeAccepted)
	status = clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ = status["data"].(map[string]any)
	if st["value"] != "confirmed" || st["note"] != "rescheduled by phone" {
		t.Fatalf("status = %v (want confirmed + note), got note=%v", st["value"], st["note"])
	}

	// A later noteless transition clears the note (unconditioned upsert).
	clSubmitOpt(t, ctx, conn, cp, cons, "setsched001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"scheduled"}`, []string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeAccepted)
	status = clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ = status["data"].(map[string]any)
	if _, hasNote := st["note"]; hasNote {
		t.Fatalf("a noteless transition must clear the prior note; got %v", st["note"])
	}
}

// TestClinic_TerminalStatusGuard proves the lifecycle guard: once an appointment
// reaches a terminal status (cancelled / completed / noShow) it cannot transition
// to a DIFFERENT status — a finished / cancelled visit must not silently revert.
// Re-setting the SAME terminal value stays accepted (idempotent under at-least-once,
// and clears a stale note). Non-terminal statuses are unaffected (covered above).
func TestClinic_TerminalStatusGuard(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status-terminal")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0010", "Term Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0010", "Dr. Term", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0010", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-10T09:00:00Z","endsAt":"2026-07-10T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// Move to a terminal status (the visit happened) — the FIRST terminal transition
	// requires provider + patient (to release the held slot-claim cells).
	{
		reads, optionalReads := clStatusReads(apptKey, true, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "setcompl001", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+apptKey+`","status":"completed","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			reads, optionalReads, processor.OutcomeAccepted)
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-10T09:00:00Z")

	// completed→scheduled is REJECTED — a completed visit must not silently revert.
	// scheduled is non-terminal, so the dispatcher never reaches the release
	// branch: only .status (optionalReads) is declared.
	clSubmitOpt(t, ctx, conn, cp, cons, "settrevrt01", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"scheduled"}`, []string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeRejected)

	// completed→cancelled (terminal→terminal, different value) is also REJECTED —
	// by the TerminalStatus guard, which fires off the already-declared .status
	// BEFORE the script ever reaches the first-terminal-transition release branch,
	// so schedule/links are never read here either.
	clSubmitOpt(t, ctx, conn, cp, cons, "settcancl01", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"cancelled"}`, []string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeRejected)

	// The status is unchanged by the rejected attempts — still completed.
	status := clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ := status["data"].(map[string]any)
	if st["value"] != "completed" {
		t.Fatalf("after rejected transitions, status = %v, want completed (unchanged)", st["value"])
	}

	// completed→completed (same terminal value) stays accepted — idempotent re-set
	// under at-least-once, and a noteless re-set clears any stale note.
	clSubmitOpt(t, ctx, conn, cp, cons, "settidemp01", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"completed"}`, []string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeAccepted)
	status = clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ = status["data"].(map[string]any)
	if st["value"] != "completed" {
		t.Fatalf("idempotent re-set: status = %v, want completed", st["value"])
	}
}

// TestClinic_NoShowFee proves the billing consequence a plain noShow status
// flip otherwise lacked: transitioning to noShow stores a noShowFeeCents
// amount on .status — a caller-supplied positive figure, or a 2500 default
// when omitted — that clinic-ledger's clinicNoShowSettlement lens reads to
// post a DebitAccount charge. A non-positive supplied fee is rejected; a
// non-noShow transition (e.g. completed) never sets the field at all.
func TestClinic_NoShowFee(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status-noshowfee")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0011", "Noshow Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0011", "Dr. Fee", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0011", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-11T09:00:00Z","endsAt":"2026-07-11T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// Omitted noShowFeeCents defaults to 2500.
	{
		reads, optionalReads := clStatusReads(apptKey, true, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "setnoshow001", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+apptKey+`","status":"noShow","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			reads, optionalReads, processor.OutcomeAccepted)
	}
	status := clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ := status["data"].(map[string]any)
	if st["value"] != "noShow" {
		t.Fatalf("status = %v, want noShow", st["value"])
	}
	if got, _ := st["noShowFeeCents"].(float64); got != 2500 {
		t.Fatalf("noShowFeeCents = %v, want default 2500", st["noShowFeeCents"])
	}

	// A caller-supplied positive fee overrides the default on the idempotent
	// same-value re-set.
	clSubmitOpt(t, ctx, conn, cp, cons, "setnoshow002", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"noShow","noShowFeeCents":5000}`,
		[]string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeAccepted)
	status = clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ = status["data"].(map[string]any)
	if got, _ := st["noShowFeeCents"].(float64); got != 5000 {
		t.Fatalf("noShowFeeCents = %v, want caller-supplied 5000", got)
	}

	// A non-positive supplied fee is rejected.
	clSubmitOpt(t, ctx, conn, cp, cons, "setnoshow003", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"noShow","noShowFeeCents":0}`,
		[]string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeRejected)

	// A different appointment moved to a non-noShow terminal status never
	// gets a noShowFeeCents field at all.
	apptID2 := clSubmit(t, ctx, conn, cp, cons, "mkappt0012", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-12T09:00:00Z","endsAt":"2026-07-12T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey2 := "vtx.appointment." + apptID2
	{
		reads, optionalReads := clStatusReads(apptKey2, true, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "setcompl002", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+apptKey2+`","status":"completed","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			reads, optionalReads, processor.OutcomeAccepted)
	}
	status2 := clReadDoc(t, ctx, conn, apptKey2+".status")
	st2, _ := status2["data"].(map[string]any)
	if _, present := st2["noShowFeeCents"]; present {
		t.Fatalf("a completed transition must never set noShowFeeCents, got %v", st2["noShowFeeCents"])
	}
}

// TestClinic_MarkPastDueNoShow proves the Weaver-only auto no-show op: given
// only appointmentKey (no caller-supplied provider/patient — clinic-reminders'
// pastDueAppointments playbook has none to supply), it resolves both LIVE off
// the appointment's own links, upserts .status{noShow, the auto note}, and
// releases the held slot-claim cells. Unlike a staff SetAppointmentStatus
// (noShow), it never sets noShowFeeCents — an appointment nobody at the desk
// closed is a documentation lapse, not a missed visit (PO ruling,
// verticals.md), so the automated sweep marks without a fee.
func TestClinic_MarkPastDueNoShow(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status-pastdue")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0020", "PastDue Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0020", "Dr. PastDue", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0020", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-20T09:00:00Z","endsAt":"2026-07-20T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-20T09:00:00Z")
	clAssertSlotClaimLive(t, ctx, conn, patientKey, "2026-07-20T09:00:00Z")

	clSubmit(t, ctx, conn, cp, cons, "pastdue001", "MarkPastDueNoShow", "appointment",
		`{"appointmentKey":"`+apptKey+`"}`,
		[]string{apptKey, apptKey + ".schedule", apptKey + ".status"}, processor.OutcomeAccepted)

	status := clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ := status["data"].(map[string]any)
	if st["value"] != "noShow" {
		t.Fatalf("status = %v, want noShow", st["value"])
	}
	if _, present := st["noShowFeeCents"]; present {
		t.Fatalf("MarkPastDueNoShow must never set noShowFeeCents (sweep marks without a fee), got %v", st["noShowFeeCents"])
	}
	if st["note"] != "Auto no-show: appointment ended without a status update" {
		t.Fatalf("note = %v, want the auto no-show note", st["note"])
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-20T09:00:00Z")
	clAssertSlotClaimReleased(t, ctx, conn, patientKey, "2026-07-20T09:00:00Z")
}

// TestClinic_MarkPastDueNoShowSkipsAlreadyTerminal proves the idempotent race
// guard: a dispatch that lands after the appointment ALREADY reached a terminal
// status (e.g. staff completed it between the lens's last projection and this
// dispatch, or a prior at-least-once retry already converged it) is a silent
// no-op — it must never clobber a legitimate outcome with noShow.
func TestClinic_MarkPastDueNoShowSkipsAlreadyTerminal(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status-pastdue-terminal")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0021", "Already Done Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0021", "Dr. Done", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0021", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-21T09:00:00Z","endsAt":"2026-07-21T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	{
		reads, optionalReads := clStatusReads(apptKey, true, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "setcompl021", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+apptKey+`","status":"completed","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			reads, optionalReads, processor.OutcomeAccepted)
	}

	clSubmit(t, ctx, conn, cp, cons, "pastdue021", "MarkPastDueNoShow", "appointment",
		`{"appointmentKey":"`+apptKey+`"}`,
		[]string{apptKey, apptKey + ".schedule", apptKey + ".status"}, processor.OutcomeAccepted)

	status := clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ := status["data"].(map[string]any)
	if st["value"] != "completed" {
		t.Fatalf("status = %v, want completed (MarkPastDueNoShow must not clobber a terminal status)", st["value"])
	}
	if _, present := st["noShowFeeCents"]; present {
		t.Fatalf("a skipped no-op must never set noShowFeeCents, got %v", st["noShowFeeCents"])
	}
}

// TestClinic_RecordEncounter proves the post-visit clinical-record path: a
// RecordEncounter upserts two sibling aspects along the sensitivity boundary —
// .encounter, SENSITIVE, carrying the RAW clinical content (summary / assessment
// / plan), its DEK custodied on the clinicalRecord retention class (never the
// patient's identity), and .documentation, non-sensitive, carrying the
// operational signals (documentedAt = canonical-UTC op.submittedAt,
// followUpRequested, followUpDate). A correction (re-run with
// followUpRequested=false) overwrites both aspects and drops followUpDate
// (unconditioned upsert). A non-appointment target is rejected (WrongClass).
func TestClinic_RecordEncounter(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "encounter")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0010", "Pat Visit")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0010", "Dr. Visit", "Family")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0010", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-10T09:00:00Z","endsAt":"2026-07-10T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// Document the visit: raw clinical content + operational follow-up.
	clSubmit(t, ctx, conn, cp, cons, "enc0001", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+apptKey+`","summary":"Annual checkup, vitals normal.","assessment":"Essential hypertension, well-controlled.","plan":"Continue medication; recheck in 6 months.","followUpRequested":true,"followUpDate":"2027-01-15T15:00:00Z"}`,
		[]string{apptKey}, processor.OutcomeAccepted)

	// The .encounter aspect's class is readable without decrypting its data (only
	// the data map is ciphertext).
	if cls, _ := clReadDoc(t, ctx, conn, apptKey+".encounter")["class"].(string); cls != "appointmentEncounter" {
		t.Fatalf("encounter class = %q, want appointmentEncounter", cls)
	}
	// Raw clinical content is SENSITIVE — captured only under decryption.
	data := clDecryptEncounter(t, ctx, conn, apptKey)
	if data["summary"] != "Annual checkup, vitals normal." {
		t.Fatalf("encounter summary = %v", data["summary"])
	}
	if data["assessment"] != "Essential hypertension, well-controlled." {
		t.Fatalf("encounter assessment = %v", data["assessment"])
	}
	if data["plan"] != "Continue medication; recheck in 6 months." {
		t.Fatalf("encounter plan = %v", data["plan"])
	}

	// Operational signals live on the non-sensitive .documentation aspect.
	docAsp := clReadDoc(t, ctx, conn, apptKey+".documentation")
	if cls, _ := docAsp["class"].(string); cls != "appointmentDocumentation" {
		t.Fatalf("documentation class = %q, want appointmentDocumentation", cls)
	}
	docData, _ := docAsp["data"].(map[string]any)
	// documentedAt is the canonical-UTC op.submittedAt anchor.
	if docData["documentedAt"] != clSubmittedAnchor {
		t.Fatalf("documentation documentedAt = %v, want %s (= op.submittedAt)", docData["documentedAt"], clSubmittedAnchor)
	}
	if docData["followUpRequested"] != true {
		t.Fatalf("documentation followUpRequested = %v, want true", docData["followUpRequested"])
	}
	if docData["followUpDate"] != "2027-01-15T15:00:00Z" {
		t.Fatalf("documentation followUpDate = %v", docData["followUpDate"])
	}

	// A correction (unconditioned upsert): no follow-up this time → followUpDate
	// dropped, followUpRequested false. Both aspects are replaced whole.
	clSubmit(t, ctx, conn, cp, cons, "enc0002", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+apptKey+`","summary":"Corrected note.","followUpRequested":false,"followUpDate":"2027-01-15T15:00:00Z"}`,
		[]string{apptKey}, processor.OutcomeAccepted)
	data = clDecryptEncounter(t, ctx, conn, apptKey)
	if data["summary"] != "Corrected note." {
		t.Fatalf("after correction summary = %v", data["summary"])
	}
	// The correction omitted assessment, so it is written as the empty string —
	// the plaintext shape is fixed so clinicEncountersRead's per-field secure
	// columns never see a missing field. The stale value being gone (rather than
	// the key being gone) is what proves the whole aspect was replaced.
	if data["assessment"] != "" {
		t.Fatalf("correction must replace the whole aspect; stale assessment present: %v", data["assessment"])
	}
	if data["plan"] != "" {
		t.Fatalf("correction must replace the whole aspect; stale plan present: %v", data["plan"])
	}
	docData, _ = clReadDoc(t, ctx, conn, apptKey+".documentation")["data"].(map[string]any)
	if docData["followUpRequested"] != false {
		t.Fatalf("after correction followUpRequested = %v, want false", docData["followUpRequested"])
	}
	if _, hasDate := docData["followUpDate"]; hasDate {
		t.Fatalf("followUpDate must be dropped when followUpRequested is false; got %v", docData["followUpDate"])
	}

	// A date-only followUpDate (the FE's <input type=date> value) is normalized to a
	// full canonical-UTC RFC3339 instant anchored to 09:00:00Z, so the clinic-reminders
	// follow-up reminder can arm an @at timer at it (Weaver's temporal lane needs a
	// parseable RFC3339 freshUntil). The stored value stays date-prefixed, so the FE's
	// .slice(0,10) renders the same day.
	clSubmit(t, ctx, conn, cp, cons, "enc0004", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+apptKey+`","summary":"Follow-up by date.","followUpRequested":true,"followUpDate":"2027-03-20"}`,
		[]string{apptKey}, processor.OutcomeAccepted)
	docData, _ = clReadDoc(t, ctx, conn, apptKey+".documentation")["data"].(map[string]any)
	if docData["followUpDate"] != "2027-03-20T09:00:00Z" {
		t.Fatalf("date-only followUpDate must normalize to 2027-03-20T09:00:00Z; got %v", docData["followUpDate"])
	}

	// A non-appointment target key is rejected (the vtx.appointment.<id> key-shape
	// guard fails closed before any write).
	clSubmit(t, ctx, conn, cp, cons, "enc0003", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+patientKey+`","summary":"x"}`, []string{patientKey}, processor.OutcomeRejected)
}

// TestClinic_RescheduleAppointment proves the move-an-appointment path: a
// RescheduleAppointment rewrites the .schedule aspect with new startsAt/endsAt,
// re-deriving remindAt = startsAt − 24h (so the clinic-reminders @at re-arms),
// while leaving the .status aspect and the forPatient/withProvider links untouched.
// A re-supplied reason is preserved; an omitted reason clears it; a non-Z offset is
// normalized to canonical UTC; a tombstoned target is rejected.
func TestClinic_RescheduleAppointment(t *testing.T) {
	t.Parallel()
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
	// provider + patient are supplied and validated (WrongProvider/WrongPatient) so
	// the op can release the old cells and claim the new ones (SlotConflict /
	// PatientDoubleBook conflict-check).
	clSubmit(t, ctx, conn, cp, cons, "resched0001", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"2026-07-12T18:00:00+02:00","endsAt":"2026-07-12T18:30:00+02:00","reason":"Annual checkup"}`,
		clRescheduleReads(apptKey, providerKey, patientKey), processor.OutcomeAccepted)
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-10T15:00:00Z")
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-12T16:00:00Z")

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
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"2026-07-13T09:00:00Z","endsAt":"2026-07-13T09:30:00Z"}`,
		clRescheduleReads(apptKey, providerKey, patientKey), processor.OutcomeAccepted)
	sd2, _ := clReadDoc(t, ctx, conn, apptKey+".schedule")["data"].(map[string]any)
	if _, present := sd2["reason"]; present {
		t.Fatalf("an omitted reason should clear it; got reason=%v", sd2["reason"])
	}
	if sd2["startsAt"] != "2026-07-13T09:00:00Z" || sd2["remindAt"] != "2026-07-12T09:00:00Z" {
		t.Fatalf("second reschedule schedule = %v, want startsAt 2026-07-13T09:00:00Z / remindAt 2026-07-12T09:00:00Z", sd2)
	}

	// A tombstoned appointment cannot be rescheduled.
	clSubmit(t, ctx, conn, cp, cons, "tombappt0001", "TombstoneAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
		[]string{apptKey}, processor.OutcomeAccepted)
	clSubmit(t, ctx, conn, cp, cons, "resched0003", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"2026-07-14T09:00:00Z","endsAt":"2026-07-14T09:30:00Z"}`,
		clRescheduleReads(apptKey, providerKey, patientKey), processor.OutcomeRejected)
}

// TestClinic_RescheduleIntoConflictRejected proves Increment 2's core property: a
// reschedule that moves an appointment INTO a slot already booked for the provider
// is rejected (SlotConflict), closing the double-book bypass Increment 1 left open
// (CreateAppointment was checked, RescheduleAppointment was not). It also pins
// self-exclusion (moving onto your own current slot is fine), wrong-provider
// rejection, the endsAt<=startsAt guard, that a cancelled appointment frees its
// slot for a reschedule, and that the moved interval frees its old slot.
func TestClinic_RescheduleIntoConflictRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "reschedule-conflict")

	patientKey := createPatient(t, ctx, conn, cp, cons, "rcpat0001", "Frank Mover")
	providerKey := createProvider(t, ctx, conn, cp, cons, "rcprv0001", "Dr. Solis", "Cardiology")
	providerKey2 := createProvider(t, ctx, conn, cp, cons, "rcprv0002", "Dr. Tan", "Pediatrics")

	mkAppt := func(label, start, end string) string {
		return clSubmit(t, ctx, conn, cp, cons, label, "CreateAppointment", "appointment",
			`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	}
	resched := func(label, apptKey, start, end string, want processor.MessageOutcome) {
		clSubmit(t, ctx, conn, cp, cons, label, "RescheduleAppointment", "appointment",
			`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			clRescheduleReads(apptKey, providerKey, patientKey), want)
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
		`{"appointmentKey":"`+a2+`","provider":"`+providerKey2+`","patient":"`+patientKey+`","startsAt":"2026-09-01T15:00:00Z","endsAt":"2026-09-01T15:30:00Z"}`,
		clRescheduleReads(a2, providerKey2, patientKey), processor.OutcomeRejected)

	// 8. Cancel a1, then move a2 onto a1's (now freed) 10:00 slot → accepted (a
	//    cancelled appointment's claims were released on the terminal transition).
	{
		reads, optionalReads := clStatusReads(a1, true, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "rccancel01", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+a1+`","status":"cancelled","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			reads, optionalReads, processor.OutcomeAccepted)
	}
	resched("rcres0008", a2, "2026-09-01T10:00:00Z", "2026-09-01T10:30:00Z", processor.OutcomeAccepted)
}

// TestClinic_ProviderHoursEnforced proves Increment 2b: a provider's opt-in
// availability windows (the .hours aspect, set by SetProviderHours) gate
// CreateAppointment + RescheduleAppointment. A booking outside the windows is
// rejected (OutsideHours); inside is accepted; a provider with no .hours is
// unconstrained; windows=[] clears the constraint; and SetProviderHours validates
// its windows. Weekdays (UTC): 2026-06-28=Sun, 06-29=Mon, 07-01=Wed, 07-04=Sat.
func TestClinic_ProviderHoursEnforced(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "provider-hours")

	patientKey := createPatient(t, ctx, conn, cp, cons, "phpat0001", "Holly Hours")
	providerKey := createProvider(t, ctx, conn, cp, cons, "phprv0001", "Dr. Mon", "Cardiology")
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
			[]string{patientKey, providerKey}, want)
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
			`{"appointmentKey":"`+a1Key+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			clRescheduleReads(a1Key, providerKey, patientKey), want)
	}
	resched("phres0001", "2026-06-28T10:00:00Z", "2026-06-28T10:30:00Z", processor.OutcomeRejected)
	resched("phres0002", "2026-07-01T10:00:00Z", "2026-07-01T10:30:00Z", processor.OutcomeAccepted)

	// A DIFFERENT provider with NO .hours is unconstrained — a Sunday booking is fine.
	freeProvider := createProvider(t, ctx, conn, cp, cons, "phprv0002", "Dr. Always", "GeneralPractice")
	clSubmit(t, ctx, conn, cp, cons, "phappt0007", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+freeProvider+`","startsAt":"2026-06-28T03:00:00Z","endsAt":"2026-06-28T03:30:00Z"}`,
		[]string{patientKey, freeProvider}, processor.OutcomeAccepted)

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

// TestClinic_PastTimeRejected proves the soft past-time guard: CreateAppointment
// and RescheduleAppointment reject a startsAt at or before op.submittedAt
// (ScheduleInPast). The suite pins submittedAt to clSubmittedAnchor, so a startsAt
// before / equal to that anchor is "in the past"; a clearly-future startsAt is
// accepted (the guard never blocks a valid booking).
func TestClinic_PastTimeRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "past-time")

	patientKey := createPatient(t, ctx, conn, cp, cons, "ptpat0001", "Petra Past")
	providerKey := createProvider(t, ctx, conn, cp, cons, "ptprv0001", "Dr. Future", "GeneralPractice")
	mkAppt := func(label, start, end string, want processor.MessageOutcome) string {
		return clSubmit(t, ctx, conn, cp, cons, label, "CreateAppointment", "appointment",
			`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			[]string{patientKey, providerKey}, want)
	}

	// startsAt strictly before submittedAt (the anchor) → ScheduleInPast rejected.
	mkAppt("ptappt0001", "2025-12-31T23:00:00Z", "2025-12-31T23:30:00Z", processor.OutcomeRejected)
	// startsAt EXACTLY at submittedAt → rejected (the guard requires submitted < startsAt).
	mkAppt("ptappt0002", "2026-01-01T00:00:00Z", "2026-01-01T00:30:00Z", processor.OutcomeRejected)
	// A clearly-future startsAt → accepted (the guard does not block valid bookings).
	apptKey := "vtx.appointment." + mkAppt("ptappt0003", "2026-07-01T10:00:00Z", "2026-07-01T10:30:00Z", processor.OutcomeAccepted)

	// RescheduleAppointment into the past is rejected exactly as a create is.
	clSubmit(t, ctx, conn, cp, cons, "ptres0001", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"2025-06-01T10:00:00Z","endsAt":"2025-06-01T10:30:00Z"}`,
		clRescheduleReads(apptKey, providerKey, patientKey), processor.OutcomeRejected)
	// A reschedule to a future time still works (and re-derives remindAt).
	clSubmit(t, ctx, conn, cp, cons, "ptres0002", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"2026-08-01T10:00:00Z","endsAt":"2026-08-01T10:30:00Z"}`,
		clRescheduleReads(apptKey, providerKey, patientKey), processor.OutcomeAccepted)
	sched := clReadDoc(t, ctx, conn, apptKey+".schedule")
	if sd, _ := sched["data"].(map[string]any); sd["startsAt"] != "2026-08-01T10:00:00Z" {
		t.Fatalf("after future reschedule, startsAt = %v, want 2026-08-01T10:00:00Z", sched["data"])
	}
}

// TestClinic_ProviderTimeOffEnforced proves the date-specific time-off exceptions
// layer: a provider's opt-in .timeOff blackout ranges (set by SetProviderTimeOff)
// gate CreateAppointment + RescheduleAppointment on top of the recurring weekly
// .hours. A booking overlapping any range is rejected (ProviderUnavailable);
// back-to-back at a range boundary is allowed (half-open [from,to)); a booking
// outside every range is accepted; a provider with no .timeOff is unrestricted;
// ranges=[] clears all blackouts; and SetProviderTimeOff validates its ranges.
func TestClinic_ProviderTimeOffEnforced(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "provider-timeoff")

	patientKey := createPatient(t, ctx, conn, cp, cons, "topat0001", "Tom Off")
	providerKey := createProvider(t, ctx, conn, cp, cons, "toprv0001", "Dr. Away", "GeneralPractice")
	// Block a vacation week 2026-07-06 00:00 → 2026-07-13 00:00 (with a reason).
	clSubmit(t, ctx, conn, cp, cons, "tooff0001", "SetProviderTimeOff", "provider",
		`{"providerKey":"`+providerKey+`","ranges":[{"from":"2026-07-06T00:00:00Z","to":"2026-07-13T00:00:00Z","reason":"Vacation"}]}`,
		[]string{providerKey}, processor.OutcomeAccepted)

	// The .timeOff aspect landed with one range carrying the reason.
	off := clReadDoc(t, ctx, conn, providerKey+".timeOff")
	if off["class"] != "providerTimeOff" {
		t.Fatalf("timeOff class = %v, want providerTimeOff", off["class"])
	}
	if od, _ := off["data"].(map[string]any); od["ranges"] == nil {
		t.Fatalf("timeOff ranges missing: %v", off["data"])
	} else if r, _ := od["ranges"].([]any); len(r) != 1 {
		t.Fatalf("timeOff ranges = %v, want 1", od["ranges"])
	} else if r0, _ := r[0].(map[string]any); r0["reason"] != "Vacation" {
		t.Fatalf("timeOff range reason = %v, want Vacation", r0["reason"])
	}

	mkAppt := func(label, start, end string, want processor.MessageOutcome) string {
		return clSubmit(t, ctx, conn, cp, cons, label, "CreateAppointment", "appointment",
			`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"`+start+`","endsAt":"`+end+`"}`,
			[]string{patientKey, providerKey}, want)
	}

	// Inside the blocked week → ProviderUnavailable rejected.
	mkAppt("toappt0001", "2026-07-08T10:00:00Z", "2026-07-08T10:30:00Z", processor.OutcomeRejected)
	// Ends EXACTLY at the range start (half-open: from is the first blocked instant) → accepted.
	mkAppt("toappt0002", "2026-07-05T23:30:00Z", "2026-07-06T00:00:00Z", processor.OutcomeAccepted)
	// Starts EXACTLY at the range end (half-open: to is the first free instant) → accepted.
	mkAppt("toappt0003", "2026-07-13T00:00:00Z", "2026-07-13T00:30:00Z", processor.OutcomeAccepted)
	// Clearly outside the range → accepted.
	outsideKey := "vtx.appointment." + mkAppt("toappt0004", "2026-07-20T10:00:00Z", "2026-07-20T10:30:00Z", processor.OutcomeAccepted)

	// A reschedule INTO the blocked week is rejected; a move to another free slot works.
	clSubmit(t, ctx, conn, cp, cons, "tores0001", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+outsideKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"2026-07-09T10:00:00Z","endsAt":"2026-07-09T10:30:00Z"}`,
		clRescheduleReads(outsideKey, providerKey, patientKey), processor.OutcomeRejected)
	clSubmit(t, ctx, conn, cp, cons, "tores0002", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+outsideKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"2026-07-21T10:00:00Z","endsAt":"2026-07-21T10:30:00Z"}`,
		clRescheduleReads(outsideKey, providerKey, patientKey), processor.OutcomeAccepted)

	// A different provider with NO .timeOff is unrestricted — a booking in that week is fine.
	freeProvider := createProvider(t, ctx, conn, cp, cons, "toprv0002", "Dr. Here", "GeneralPractice")
	clSubmit(t, ctx, conn, cp, cons, "toappt0005", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+freeProvider+`","startsAt":"2026-07-08T10:00:00Z","endsAt":"2026-07-08T10:30:00Z"}`,
		[]string{patientKey, freeProvider}, processor.OutcomeAccepted)

	// Clearing the blackouts (ranges=[]) makes the original provider's blocked week bookable.
	clSubmit(t, ctx, conn, cp, cons, "tooff0002", "SetProviderTimeOff", "provider",
		`{"providerKey":"`+providerKey+`","ranges":[]}`,
		[]string{providerKey}, processor.OutcomeAccepted)
	mkAppt("toappt0006", "2026-07-08T14:00:00Z", "2026-07-08T14:30:00Z", processor.OutcomeAccepted)

	// SetProviderTimeOff validation: from >= to, and a missing endpoint → rejected.
	clSubmit(t, ctx, conn, cp, cons, "tobad0001", "SetProviderTimeOff", "provider",
		`{"providerKey":"`+providerKey+`","ranges":[{"from":"2026-08-10T00:00:00Z","to":"2026-08-05T00:00:00Z"}]}`,
		[]string{providerKey}, processor.OutcomeRejected)
	clSubmit(t, ctx, conn, cp, cons, "tobad0002", "SetProviderTimeOff", "provider",
		`{"providerKey":"`+providerKey+`","ranges":[{"to":"2026-08-10T00:00:00Z"}]}`,
		[]string{providerKey}, processor.OutcomeRejected)
}

// TestClinic_RescheduleToSameInterval proves the degenerate case of the
// release/claim cell diff: a "reschedule" that resupplies the SAME startsAt/endsAt
// the appointment already holds (e.g. a reason-only edit) computes an EMPTY
// to_release and to_claim (every held cell appears in both the old and new set),
// so no cell mutation is emitted — the held cells stay claimed, untouched, and a
// second, truly conflicting booking against those same cells still correctly
// rejects.
func TestClinic_RescheduleToSameInterval(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "reschedule-same")

	patientKey := createPatient(t, ctx, conn, cp, cons, "rspat0001", "Sam Same")
	providerKey := createProvider(t, ctx, conn, cp, cons, "rsprv0001", "Dr. Same", "Cardiology")

	apptID := clSubmit(t, ctx, conn, cp, cons, "rsappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-15T09:00:00Z","endsAt":"2026-07-15T09:30:00Z","reason":"Checkup"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-15T09:00:00Z")
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-15T09:15:00Z")

	// Resupply the SAME startsAt/endsAt (only the reason changes) → accepted, held
	// cells stay live (never released, never re-claimed — an empty diff).
	clSubmit(t, ctx, conn, cp, cons, "rsres0001", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"2026-07-15T09:00:00Z","endsAt":"2026-07-15T09:30:00Z","reason":"Checkup, updated note"}`,
		clRescheduleReads(apptKey, providerKey, patientKey), processor.OutcomeAccepted)
	sd, _ := clReadDoc(t, ctx, conn, apptKey+".schedule")["data"].(map[string]any)
	if sd["reason"] != "Checkup, updated note" {
		t.Fatalf("reason = %v, want updated", sd["reason"])
	}
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-15T09:00:00Z")
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-15T09:15:00Z")

	// The cells are still genuinely held: a different appointment competing for them
	// is still rejected (the no-op diff did not silently drop the claim).
	clSubmit(t, ctx, conn, cp, cons, "rsappt0002", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-15T09:00:00Z","endsAt":"2026-07-15T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeRejected)
}

// TestClinic_SlotGridAndTooLong proves the two new write-path-claim guards: a
// startsAt/endsAt not aligned to the clinic's mandatory 15-minute grid is rejected
// (SlotGridViolation), on both CreateAppointment and RescheduleAppointment; and a
// span exceeding 96 cells (24h) is rejected (AppointmentTooLong) rather than
// silently truncated.
func TestClinic_SlotGridAndTooLong(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "grid-toolong")

	patientKey := createPatient(t, ctx, conn, cp, cons, "sgpat0001", "Grid Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "sgprv0001", "Dr. Grid", "Cardiology")

	// startsAt minute (:05) is off-grid → SlotGridViolation.
	clSubmit(t, ctx, conn, cp, cons, "sgappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-03T09:05:00Z","endsAt":"2026-07-03T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeRejected)
	// endsAt minute (:40) is off-grid → SlotGridViolation.
	clSubmit(t, ctx, conn, cp, cons, "sgappt0002", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-03T09:00:00Z","endsAt":"2026-07-03T09:40:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeRejected)

	// A valid grid-aligned booking, to reschedule against next.
	apptID := clSubmit(t, ctx, conn, cp, cons, "sgappt0003", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-03T09:00:00Z","endsAt":"2026-07-03T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// RescheduleAppointment enforces the same grid guard.
	clSubmit(t, ctx, conn, cp, cons, "sgres0001", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"2026-07-04T09:10:00Z","endsAt":"2026-07-04T09:30:00Z"}`,
		clRescheduleReads(apptKey, providerKey, patientKey), processor.OutcomeRejected)
	// The appointment's original slot claims survive the rejected reschedule.
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-03T09:00:00Z")

	// A span of 97 fifteen-minute cells (24h15m) exceeds MAX_SLOT_CELLS=96 →
	// AppointmentTooLong, not a silently truncated claim set.
	clSubmit(t, ctx, conn, cp, cons, "sgappt0004", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-08-01T00:00:00Z","endsAt":"2026-08-02T00:15:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeRejected)

	// Exactly 96 cells (24h, the boundary) is accepted.
	clSubmit(t, ctx, conn, cp, cons, "sgappt0005", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-08-05T00:00:00Z","endsAt":"2026-08-06T00:00:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
}

// TestClinic_TerminalStatusRequiresEndpoints proves the FIRST terminal
// SetAppointmentStatus transition requires provider + patient (to release the held
// slot-claim cells) and validates them against the appointment's actual withProvider
// / forPatient links (WrongProvider / WrongPatient) — a missing or wrong endpoint
// must not silently leave the cells claimed, nor release someone else's cells.
func TestClinic_TerminalStatusRequiresEndpoints(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "terminal-endpoints")

	patientKey := createPatient(t, ctx, conn, cp, cons, "tepat0001", "Ted Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "teprv0001", "Dr. Ted", "Cardiology")
	otherPatient := createPatient(t, ctx, conn, cp, cons, "tepat0002", "Other Ted")

	apptID := clSubmit(t, ctx, conn, cp, cons, "teappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-06T09:00:00Z","endsAt":"2026-07-06T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// Missing provider/patient on the first terminal transition → InvalidArgument,
	// before the script ever reads a withProvider/forPatient link.
	clSubmitOpt(t, ctx, conn, cp, cons, "tecancl001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"cancelled"}`, []string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeRejected)
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-06T09:00:00Z")

	// A wrong patient (not this appointment's actual patient) → WrongPatient. The
	// script still reads the (correct) withProvider link and the (absent, because
	// wrong) forPatient link keyed off the SUPPLIED patient before failing.
	{
		reads, optionalReads := clStatusReads(apptKey, true, providerKey, otherPatient)
		clSubmitOpt(t, ctx, conn, cp, cons, "tecancl002", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+apptKey+`","status":"cancelled","provider":"`+providerKey+`","patient":"`+otherPatient+`"}`,
			reads, optionalReads, processor.OutcomeRejected)
	}
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-06T09:00:00Z")

	// The real endpoints → accepted, cells released.
	{
		reads, optionalReads := clStatusReads(apptKey, true, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "tecancl003", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+apptKey+`","status":"cancelled","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			reads, optionalReads, processor.OutcomeAccepted)
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-06T09:00:00Z")
}

// TestClinic_RejectsBadStatus proves the status enum guard.
func TestClinic_RejectsBadStatus(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "bad-status")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0003", "Carol")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0003", "Dr. Kim", "Pediatrics")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0003", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-03T09:00:00Z","endsAt":"2026-07-03T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	clSubmitOpt(t, ctx, conn, cp, cons, "badstat0001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"bogus"}`, []string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeRejected)

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
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "wrong-class")

	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0004", "Dr. Wu", "Oncology")
	// A patient-shaped key that is alive but class=identity (not patient).
	fakePatient := "vtx.patient.CLfakepatHJKMNPQRST"
	clSeedVertex(t, ctx, conn, fakePatient, "identity", false)

	apptID := clSubmit(t, ctx, conn, cp, cons, "wrongcls0001", "CreateAppointment", "appointment",
		`{"patient":"`+fakePatient+`","provider":"`+providerKey+`","startsAt":"2026-07-04T09:00:00Z","endsAt":"2026-07-04T09:30:00Z"}`,
		[]string{fakePatient, providerKey}, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, "vtx.appointment."+apptID) {
		t.Fatalf("an appointment was committed against a wrong-class patient")
	}
}

// TestClinic_RejectsDeadEndpoint proves the no-orphan guard: CreateAppointment
// against an absent patient is rejected.
func TestClinic_RejectsDeadEndpoint(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "dead-endpoint")

	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0005", "Dr. Ng", "Neurology")
	absentPatient := "vtx.patient.CLabsentptHJKMNPQRS"

	apptID := clSubmit(t, ctx, conn, cp, cons, "dead0001", "CreateAppointment", "appointment",
		`{"patient":"`+absentPatient+`","provider":"`+providerKey+`","startsAt":"2026-07-05T09:00:00Z","endsAt":"2026-07-05T09:30:00Z"}`,
		[]string{absentPatient, providerKey}, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, "vtx.appointment."+apptID) {
		t.Fatalf("an appointment was committed against an absent patient")
	}
}

// TestClinic_TombstonePatient proves a patient soft-deletes.
func TestClinic_TombstonePatient(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "tombstone")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0006", "Dave")
	clSubmit(t, ctx, conn, cp, cons, "tomb0001", "TombstonePatient", "patient",
		`{"patientKey":"`+patientKey+`"}`, []string{patientKey}, processor.OutcomeAccepted)

	if pdoc := clReadDoc(t, ctx, conn, patientKey); pdoc["isDeleted"] != true {
		t.Fatalf("patient isDeleted = %v after tombstone, want true", pdoc["isDeleted"])
	}
}

// TestClinic_BackfillPatientRegistration proves the one-time repair for a
// patient minted before registeredAt became an always-present .demographics
// field (2026-08-08, 7eb4c72f): it upserts registeredAt while preserving
// fullName, and no-ops cleanly once registeredAt is already set — mirroring
// the live defect (verticals.md "worksAt front-desk staffer's patient-context
// switcher is empty") where such a patient is silently excluded from every
// clinicPatientsRead roster row, including the WildcardAnchor holder's.
func TestClinic_BackfillPatientRegistration(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "backfill-registration")

	patientKey := "vtx.patient.CLpreaugustHJKMNPQRS"
	clSeedVertex(t, ctx, conn, patientKey, "patient", false)
	demoKey := patientKey + ".demographics"
	doc := map[string]any{"class": "patientDemographics", "isDeleted": false,
		"data": map[string]any{"fullName": "Priya Chandra"}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, demoKey, b); err != nil {
		t.Fatalf("seed pre-2026-08-08 demographics: %v", err)
	}

	clSubmit(t, ctx, conn, cp, cons, "backfillreg0001", "BackfillPatientRegistration", "patient",
		`{"patient":"`+patientKey+`"}`, []string{patientKey, demoKey}, processor.OutcomeAccepted)

	demo := clReadDoc(t, ctx, conn, demoKey)
	dd, _ := demo["data"].(map[string]any)
	if dd["fullName"] != "Priya Chandra" {
		t.Fatalf("backfill must preserve fullName, got %v", dd["fullName"])
	}
	registeredAt, _ := dd["registeredAt"].(string)
	if registeredAt == "" {
		t.Fatalf("backfill must set registeredAt, got %v", dd["registeredAt"])
	}

	// Re-submitting once registeredAt is already set must no-op cleanly
	// (never reject, never overwrite the now-canonical timestamp) — mirrors
	// BackfillAppointmentSite's own already-present no-op.
	clSubmit(t, ctx, conn, cp, cons, "backfillreg0002", "BackfillPatientRegistration", "patient",
		`{"patient":"`+patientKey+`"}`, []string{patientKey, demoKey}, processor.OutcomeAccepted)
	demo2 := clReadDoc(t, ctx, conn, demoKey)
	dd2, _ := demo2["data"].(map[string]any)
	if dd2["registeredAt"] != registeredAt {
		t.Fatalf("a second backfill must no-op, registeredAt changed from %v to %v", registeredAt, dd2["registeredAt"])
	}
}

// TestClinic_CreatePatientWithIdentity proves CreatePatient's optional
// identityKey wires a live identifiedBy link to a pre-minted identity, and
// that .demographics carries only fullName (no contact PII).
func TestClinic_CreatePatientWithIdentity(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "create-with-identity")

	identityKey := "vtx.identity.CLidentwithHJKMNPQRS"
	clSeedVertex(t, ctx, conn, identityKey, "identity", false)

	id := clSubmitOpt(t, ctx, conn, cp, cons, "mkpatid0001", "CreatePatient", "patient",
		`{"fullName":"Bea Nakamura","identityKey":"`+identityKey+`"}`,
		[]string{identityKey}, []string{identityKey + ".patientClaim"}, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + id

	// An IDENTIFIED patient's name lives on the identity's sensitive .name
	// aspect, not here: a plaintext copy on .demographics would outlive the
	// ShredIdentityKey that destroys the same person's contact, leaving their
	// retained clinical record identified (F3(b)). .demographics keeps only the
	// non-identifying registeredAt.
	demo := clReadDoc(t, ctx, conn, patientKey+".demographics")
	dd, _ := demo["data"].(map[string]any)
	if _, hasName := dd["fullName"]; hasName {
		t.Fatalf("an identified patient's demographics must not carry fullName; got %v", dd["fullName"])
	}
	if dd["registeredAt"] == nil || dd["registeredAt"] == "" {
		t.Fatalf("demographics registeredAt = %v, want the op's submittedAt (the lenses' ghost filter reads it)", dd["registeredAt"])
	}
	if _, hasEmail := dd["email"]; hasEmail {
		t.Fatalf("demographics carries an email field %v, want none (contact PII lives on the identity)", dd["email"])
	}

	// The name landed on the identity, ENCRYPTED under that identity's own DEK —
	// so ShredIdentityKey reaches it.
	nameKey := identityKey + ".name"
	nameDoc := clReadDoc(t, ctx, conn, nameKey)
	if cls, _ := nameDoc["class"].(string); cls != "name" {
		t.Fatalf("%s class = %q, want name", nameKey, cls)
	}
	nameData, _ := nameDoc["data"].(map[string]any)
	if _, plaintext := nameData["value"].(string); plaintext {
		t.Fatalf("%s stored the name in plaintext; a sensitive aspect must commit as a ciphertext envelope", nameKey)
	}
	if decrypted := clDecryptAspect(t, ctx, conn, identityKey, nameKey); decrypted["value"] != "Bea Nakamura" {
		t.Fatalf("decrypted %s = %v, want Bea Nakamura", nameKey, decrypted["value"])
	}

	linkKey := "lnk.patient." + id + ".identifiedBy.identity.CLidentwithHJKMNPQRS"
	ldoc := clReadDoc(t, ctx, conn, linkKey)
	if ldoc["class"] != "identifiedBy" {
		t.Fatalf("identifiedBy link class = %v, want identifiedBy", ldoc["class"])
	}
	if ldoc["sourceVertex"] != patientKey || ldoc["targetVertex"] != identityKey {
		t.Fatalf("identifiedBy link source/target = %v/%v, want %v/%v",
			ldoc["sourceVertex"], ldoc["targetVertex"], patientKey, identityKey)
	}
}

// TestClinic_CreatePatientRejectsDuplicateIdentityClaim proves the global
// identityPatientClaim guard: two DIFFERENT patients can never both wire the same
// identityKey. Without the guard, two roster rows would decrypt and display the
// same person's email/phone as if each patient individually owned that contact.
func TestClinic_CreatePatientRejectsDuplicateIdentityClaim(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "dup-identity-claim")

	identityKey := "vtx.identity.CLdupePatientHJKMNPQ"
	clSeedVertex(t, ctx, conn, identityKey, "identity", false)

	clSubmitOpt(t, ctx, conn, cp, cons, "mkpatdup001", "CreatePatient", "patient",
		`{"fullName":"First Claimant","identityKey":"`+identityKey+`"}`,
		[]string{identityKey}, []string{identityKey + ".patientClaim"}, processor.OutcomeAccepted)

	secondID := clSubmitOpt(t, ctx, conn, cp, cons, "mkpatdup002", "CreatePatient", "patient",
		`{"fullName":"Second Claimant","identityKey":"`+identityKey+`"}`,
		[]string{identityKey}, []string{identityKey + ".patientClaim"}, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, "vtx.patient."+secondID) {
		t.Fatalf("a second patient was committed against an already-claimed identity")
	}
	linkKey := "lnk.patient." + secondID + ".identifiedBy.identity.CLdupePatientHJKMNPQ"
	if !clMissing(t, ctx, conn, linkKey) {
		t.Fatalf("a second identifiedBy link was committed against an already-claimed identity")
	}

	claimDoc := clReadDoc(t, ctx, conn, identityKey+".patientClaim")
	if claimDoc["class"] != "identityPatientClaim" {
		t.Fatalf("patientClaim aspect class = %v, want identityPatientClaim", claimDoc["class"])
	}
}

// TestClinic_CreatePatientRejectsDeadIdentity proves an absent identityKey is
// never wired — no patient is committed against a dead/absent identity.
func TestClinic_CreatePatientRejectsDeadIdentity(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "create-dead-identity")

	absentIdentity := "vtx.identity.CLabsentidentHJKMNPQ"

	id := clSubmit(t, ctx, conn, cp, cons, "mkpatid0002", "CreatePatient", "patient",
		`{"fullName":"Should Not Commit","identityKey":"`+absentIdentity+`"}`,
		[]string{absentIdentity}, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, "vtx.patient."+id) {
		t.Fatalf("a patient was committed against an absent identity")
	}
}

// TestClinic_CreatePatientRejectsWrongClassIdentity proves a live but
// wrong-class identityKey (e.g. another patient's key) is never wired.
func TestClinic_CreatePatientRejectsWrongClassIdentity(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "create-wrongclass-identity")

	fakeIdentity := "vtx.identity.CLfakeidentHJKMNPQRS"
	clSeedVertex(t, ctx, conn, fakeIdentity, "patient", false)

	id := clSubmit(t, ctx, conn, cp, cons, "mkpatid0003", "CreatePatient", "patient",
		`{"fullName":"Should Not Commit","identityKey":"`+fakeIdentity+`"}`,
		[]string{fakeIdentity}, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, "vtx.patient."+id) {
		t.Fatalf("a patient was committed against a wrong-class identityKey")
	}
}

// TestClinic_UnauthorizedDenied proves a consumer with no clinic ops is rejected.
func TestClinic_UnauthorizedDenied(t *testing.T) {
	t.Parallel()
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

// TestClinic_CreateAppointmentConsumerSelfScope_Allowed exercises the real
// consumer scope=self permission (permissions.go) end to end: a patient linked
// (identifiedBy) to the caller's own identity books their OWN appointment —
// step 3 authorizes scope=self via authContext.target == actor (Contract #6),
// and the script's own identifiedBy-link check (ddls.go) confirms the target
// identity actually owns the named patient.
func TestClinic_CreateAppointmentConsumerSelfScope_Allowed(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "appt-consumer-self")

	clSeedVertex(t, ctx, conn, clConsumerKey, "identity", false)

	patientID := clSubmitOpt(t, ctx, conn, cp, cons, "selfpat00001", "CreatePatient", "patient",
		`{"fullName":"Self Booker","identityKey":"`+clConsumerKey+`"}`,
		[]string{clConsumerKey}, []string{clConsumerKey + ".patientClaim"}, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerKey := createProvider(t, ctx, conn, cp, cons, "selfprv0001", "Dr. Nora Vance", "Dermatology")

	// The self-service caller computes the identifiedBy link key client-side
	// (it already knows payload.patient and its own authContext.target) and
	// declares it in OptionalReads — the correct read-posture class (d): a
	// declared, OCC-snapshotted, absence-tolerant read, not a lazy kv.Read.
	identifiedByLnk := "lnk.patient." + patientID + ".identifiedBy.identity." + clConsumerID

	reqID := testutil.GenReqID("selfappt0001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAppointment",
		Actor:         clConsumerKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"patient":"` + patientKey + `","provider":"` + providerKey + `","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{patientKey, providerKey}, OptionalReads: []string{identifiedByLnk}},
		AuthContext:   &processor.AuthContext{Target: clConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	apptKey := "vtx.appointment." + clNanoIDFromRequestID(reqID)
	if adoc := clReadDoc(t, ctx, conn, apptKey); adoc["class"] != "appointment" {
		t.Fatalf("appointment class = %v, want appointment", adoc["class"])
	}
}

// TestClinic_CreateAppointmentConsumerSelfScope_AllowedWithoutDeclaredRead proves
// the identifiedBy-link guard is correct even when the caller does NOT declare
// the link key in ContextHint.OptionalReads: kv.Read (ddls.go) falls through to
// a live on-demand read for any undeclared key (§2.5), so a caller that skips
// the declaration still gets the same correct outcome — only OCC-snapshot
// consistency, not correctness, depends on declaring it.
func TestClinic_CreateAppointmentConsumerSelfScope_AllowedWithoutDeclaredRead(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "appt-consumer-self-lazy")

	clSeedVertex(t, ctx, conn, clConsumerKey, "identity", false)

	patientID := clSubmitOpt(t, ctx, conn, cp, cons, "lazypat00001", "CreatePatient", "patient",
		`{"fullName":"Lazy Read Booker","identityKey":"`+clConsumerKey+`"}`,
		[]string{clConsumerKey}, []string{clConsumerKey + ".patientClaim"}, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerKey := createProvider(t, ctx, conn, cp, cons, "lazyprv0001", "Dr. Priya Anand", "Neurology")

	reqID := testutil.GenReqID("lazyappt0001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAppointment",
		Actor:         clConsumerKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"patient":"` + patientKey + `","provider":"` + providerKey + `","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{patientKey, providerKey}},
		AuthContext:   &processor.AuthContext{Target: clConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	apptKey := "vtx.appointment." + clNanoIDFromRequestID(reqID)
	if adoc := clReadDoc(t, ctx, conn, apptKey); adoc["class"] != "appointment" {
		t.Fatalf("appointment class = %v, want appointment", adoc["class"])
	}
}

// TestClinic_CreateAppointmentConsumerNamesUnlinkedPatient_Rejected proves the
// Starlark guard closes the gap step 3 leaves open: step 3's scope=self only
// checks authContext.target == actor, never looks at payload.patient. A
// consumer satisfying that check (target == actor) but naming a patient NOT
// linked to their own identity — no identifiedBy link, or linked to someone
// else — must still be rejected, by the script's identifiedBy-link check.
func TestClinic_CreateAppointmentConsumerNamesUnlinkedPatient_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "appt-consumer-forge")

	// A patient with no linked identity at all (the common shape: staff creates
	// most patients without ever pre-minting/wiring an identity).
	victimPatientID := clSubmit(t, ctx, conn, cp, cons, "forgepat0001", "CreatePatient", "patient",
		`{"fullName":"Unlinked Patient"}`, nil, processor.OutcomeAccepted)
	victimPatientKey := "vtx.patient." + victimPatientID
	providerKey := createProvider(t, ctx, conn, cp, cons, "forgeprv0001", "Dr. Owen Reyes", "Pediatrics")

	// Declared the same way a well-behaved caller would (OptionalReads,
	// read-posture class (d)) — absent here (no identifiedBy link exists), which
	// is exactly the case OptionalReads tolerates: the op still reaches the
	// script's guard and is rejected there, not hard-failed at hydration.
	identifiedByLnk := "lnk.patient." + victimPatientID + ".identifiedBy.identity." + clConsumerID

	reqID := testutil.GenReqID("forgeappt001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAppointment",
		Actor:         clConsumerKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"patient":"` + victimPatientKey + `","provider":"` + providerKey + `","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{victimPatientKey, providerKey}, OptionalReads: []string{identifiedByLnk}},
		AuthContext:   &processor.AuthContext{Target: clConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, "vtx.appointment."+clNanoIDFromRequestID(reqID)) {
		t.Fatalf("an appointment was committed for a patient not linked to the caller's identity")
	}
}

// TestClinic_RescheduleAppointmentConsumerSelfScope_Allowed exercises the
// consumer scope=self grant for RescheduleAppointment (permissions.go): a
// patient linked (identifiedBy) to the caller's own identity reschedules
// THEIR OWN appointment — step 3 authorizes scope=self via authContext.target
// == actor (Contract #6), and the script's own identifiedBy-link check
// (ddls.go), mirroring CreateAppointment's, confirms the target identity
// actually owns the appointment's patient.
func TestClinic_RescheduleAppointmentConsumerSelfScope_Allowed(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "resched-consumer-self")

	clSeedVertex(t, ctx, conn, clConsumerKey, "identity", false)

	patientID := clSubmitOpt(t, ctx, conn, cp, cons, "reschpat0001", "CreatePatient", "patient",
		`{"fullName":"Self Reschedule Patient","identityKey":"`+clConsumerKey+`"}`,
		[]string{clConsumerKey}, []string{clConsumerKey + ".patientClaim"}, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerKey := createProvider(t, ctx, conn, cp, cons, "reschprv0001", "Dr. Alina Voss", "Family Medicine")

	apptID := clSubmit(t, ctx, conn, cp, cons, "reschappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-10T15:00:00Z","endsAt":"2026-07-10T15:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	identifiedByLnk := "lnk.patient." + patientID + ".identifiedBy.identity." + clConsumerID

	reqID := testutil.GenReqID("selfresched1")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RescheduleAppointment",
		Actor:         clConsumerKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"appointmentKey":"` + apptKey + `","provider":"` + providerKey + `","patient":"` + patientKey + `","startsAt":"2026-07-12T16:00:00Z","endsAt":"2026-07-12T16:30:00Z"}`),
		ContextHint:   &processor.ContextHint{Reads: clRescheduleReads(apptKey, providerKey, patientKey), OptionalReads: []string{identifiedByLnk}},
		AuthContext:   &processor.AuthContext{Target: clConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	sched := clReadDoc(t, ctx, conn, apptKey+".schedule")
	sd, _ := sched["data"].(map[string]any)
	if sd["startsAt"] != "2026-07-12T16:00:00Z" {
		t.Fatalf("after self-reschedule, startsAt = %v, want 2026-07-12T16:00:00Z", sd["startsAt"])
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-10T15:00:00Z")
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-12T16:00:00Z")
}

// TestClinic_RescheduleAppointmentConsumerNamesUnlinkedPatient_Rejected mirrors
// TestClinic_CreateAppointmentConsumerNamesUnlinkedPatient_Rejected for
// RescheduleAppointment: a consumer satisfying step 3 (target == actor) but
// naming an appointment whose patient is NOT linked to their own identity must
// still be rejected by the script's identifiedBy-link check, leaving the
// appointment's schedule untouched.
func TestClinic_RescheduleAppointmentConsumerNamesUnlinkedPatient_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "resched-consumer-forge")

	victimPatientKey := createPatient(t, ctx, conn, cp, cons, "vicrespat001", "Unlinked Reschedule Victim")
	providerKey := createProvider(t, ctx, conn, cp, cons, "vicresprv001", "Dr. Owen Reyes", "Pediatrics")

	apptID := clSubmit(t, ctx, conn, cp, cons, "vicreschap01", "CreateAppointment", "appointment",
		`{"patient":"`+victimPatientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-10T15:00:00Z","endsAt":"2026-07-10T15:30:00Z"}`,
		[]string{victimPatientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	victimPatientID := victimPatientKey[len("vtx.patient."):]
	identifiedByLnk := "lnk.patient." + victimPatientID + ".identifiedBy.identity." + clConsumerID

	reqID := testutil.GenReqID("forgeresched1")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RescheduleAppointment",
		Actor:         clConsumerKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"appointmentKey":"` + apptKey + `","provider":"` + providerKey + `","patient":"` + victimPatientKey + `","startsAt":"2026-07-12T16:00:00Z","endsAt":"2026-07-12T16:30:00Z"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{apptKey}, OptionalReads: []string{identifiedByLnk}},
		AuthContext:   &processor.AuthContext{Target: clConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	sched := clReadDoc(t, ctx, conn, apptKey+".schedule")
	sd, _ := sched["data"].(map[string]any)
	if sd["startsAt"] != "2026-07-10T15:00:00Z" {
		t.Fatalf("appointment should be unchanged after a rejected forged reschedule; startsAt = %v", sd["startsAt"])
	}
}

// clSelfScopeReason submits op on the consumer scope=self path and returns the
// script's own failure text. The rejection MESSAGE is the subject of these
// vectors: the outcome collapses every denial into "rejected", while what a
// prober actually reads is which error came back.
func clSelfScopeReason(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, op, apptKey, payload string, optionalReads []string) string {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: op,
		Actor:         clConsumerKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(payload),
		// The endpoint links a prober names are declared OPTIONAL: a client that
		// expects them to exist declares them in Reads, where a miss faults at
		// hydration before the script runs — but the submitter writes its own
		// contextHint, so declaring them optional makes absence a script-visible
		// branch and the in-script order the only thing guarding the answer.
		ContextHint: &processor.ContextHint{Reads: []string{apptKey}, OptionalReads: optionalReads},
		AuthContext: &processor.AuthContext{Target: clConsumerKey},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("%s: outcome = %v, want Rejected", label, outcome)
	}
	if reply.Error == nil {
		t.Fatalf("%s: rejected reply carries no error", label)
	}
	msg := reply.Error.Message
	i := strings.Index(msg, "fail: ")
	if i < 0 {
		t.Fatalf("%s: rejection carries no script failure: %s", label, msg)
	}
	return msg[i+len("fail: "):]
}

// TestClinic_SelfScopeProbesRevealNothingAboutAnotherPatient: the endpoint
// checks each answer differently for this appointment's real provider / patient
// than for any other, so run either ahead of the identifiedBy binding and a
// self-scoped consumer walks a stranger's appointment — a clinic's provider
// roster is published, so the search space is small and the answer is which
// clinician that stranger sees. Behind the binding, every probe on an
// appointment the caller does not own answers the same way.
func TestClinic_SelfScopeProbesRevealNothingAboutAnotherPatient(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "selfscope-probe")

	clSeedVertex(t, ctx, conn, clConsumerKey, "identity", false)

	// The prober's own patient (identifiedBy their identity) — naming any other
	// patient is what the binding already rejects, so this is their only lever.
	ownID := clSubmitOpt(t, ctx, conn, cp, cons, "probeownpat01", "CreatePatient", "patient",
		`{"fullName":"Probe Own Patient","identityKey":"`+clConsumerKey+`"}`,
		[]string{clConsumerKey}, []string{clConsumerKey + ".patientClaim"}, processor.OutcomeAccepted)
	ownPatientKey := "vtx.patient." + ownID

	victimPatientKey := createPatient(t, ctx, conn, cp, cons, "probevicpat01", "Probe Victim")
	strangerPatientKey := createPatient(t, ctx, conn, cp, cons, "probestrpat01", "Probe Stranger")
	realProviderKey := createProvider(t, ctx, conn, cp, cons, "probeprvA0001", "Dr. Real Provider", "Cardiology")
	decoyProviderKey := createProvider(t, ctx, conn, cp, cons, "probeprvB0001", "Dr. Decoy Provider", "Dermatology")

	apptID := clSubmit(t, ctx, conn, cp, cons, "probeappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+victimPatientKey+`","provider":"`+realProviderKey+`","startsAt":"2026-07-10T15:00:00Z","endsAt":"2026-07-10T15:30:00Z"}`,
		[]string{victimPatientKey, realProviderKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	ownIdentifiedBy := []string{"lnk.patient." + ownID + ".identifiedBy.identity." + clConsumerID}

	// RescheduleAppointment: guessing the appointment's real provider must be
	// indistinguishable from guessing wrong.
	reschedule := func(label, providerKey string) string {
		return clSelfScopeReason(t, ctx, conn, cp, cons, label, "RescheduleAppointment", apptKey,
			`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+ownPatientKey+
				`","startsAt":"2026-07-12T16:00:00Z","endsAt":"2026-07-12T16:30:00Z"}`,
			append([]string{
				"lnk.appointment." + apptID + ".withProvider.provider." + providerKey[len("vtx.provider."):],
				"lnk.appointment." + apptID + ".forPatient.patient." + ownID,
			}, ownIdentifiedBy...))
	}
	real, decoy := reschedule("probersreal1", realProviderKey), reschedule("probersdcoy1", decoyProviderKey)
	if real != decoy {
		t.Errorf("the provider probe distinguishes this appointment's clinician from any other:\n  real provider  → %s\n  decoy provider → %s\n"+
			"walking a published roster then names who a stranger is seeing", real, decoy)
	}
	// Pin what they agree ON: equality alone would also be satisfied by both
	// probes dying at some earlier common error, which proves nothing.
	if !strings.Contains(real, "WrongPatient") {
		t.Errorf("probes should be answered by the appointment's own patient check, got %q", real)
	}

	// SetAppointmentStatus: naming the appointment's actual patient must be
	// indistinguishable from naming an unrelated one.
	cancel := func(label, patientKey string) string {
		patientID := patientKey[len("vtx.patient."):]
		return clSelfScopeReason(t, ctx, conn, cp, cons, label, "SetAppointmentStatus", apptKey,
			`{"appointmentKey":"`+apptKey+`","status":"cancelled","patient":"`+patientKey+`","provider":"`+realProviderKey+`"}`,
			[]string{
				"lnk.appointment." + apptID + ".forPatient.patient." + patientID,
				"lnk.patient." + patientID + ".identifiedBy.identity." + clConsumerID,
			})
	}
	victim, stranger := cancel("probescvic01", victimPatientKey), cancel("probescstr01", strangerPatientKey)
	if victim != stranger {
		t.Errorf("the patient probe confirms who an appointment belongs to:\n  its actual patient → %s\n  an unrelated one   → %s", victim, stranger)
	}
	if !strings.Contains(victim, "AuthDenied") {
		t.Errorf("probes should be answered by the ownership binding, got %q", victim)
	}

	sched := clReadDoc(t, ctx, conn, apptKey+".schedule")
	sd, _ := sched["data"].(map[string]any)
	if sd["startsAt"] != "2026-07-10T15:00:00Z" {
		t.Fatalf("probes must not move the appointment; startsAt = %v", sd["startsAt"])
	}
}

// TestClinic_SetAppointmentStatusConsumerSelfScope_CancelAllowed exercises the
// consumer scope=self grant for SetAppointmentStatus (permissions.go): a
// patient linked (identifiedBy) to the caller's own identity cancels THEIR OWN
// appointment — the only status value the self grant permits.
func TestClinic_SetAppointmentStatusConsumerSelfScope_CancelAllowed(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "cancel-consumer-self")

	clSeedVertex(t, ctx, conn, clConsumerKey, "identity", false)

	patientID := clSubmitOpt(t, ctx, conn, cp, cons, "cancelpat0001", "CreatePatient", "patient",
		`{"fullName":"Self Cancel Patient","identityKey":"`+clConsumerKey+`"}`,
		[]string{clConsumerKey}, []string{clConsumerKey + ".patientClaim"}, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerKey := createProvider(t, ctx, conn, cp, cons, "cancelprv0001", "Dr. Mara Chen", "Internal Medicine")

	apptID := clSubmit(t, ctx, conn, cp, cons, "cancelappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-10T15:00:00Z","endsAt":"2026-07-10T15:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	identifiedByLnk := "lnk.patient." + patientID + ".identifiedBy.identity." + clConsumerID

	reqID := testutil.GenReqID("selfcancel001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "SetAppointmentStatus",
		Actor:         clConsumerKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"appointmentKey":"` + apptKey + `","status":"cancelled","provider":"` + providerKey + `","patient":"` + patientKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: clRescheduleReads(apptKey, providerKey, patientKey), OptionalReads: []string{identifiedByLnk, apptKey + ".status"}},
		AuthContext:   &processor.AuthContext{Target: clConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	status := clReadDoc(t, ctx, conn, apptKey+".status")
	if st, _ := status["data"].(map[string]any); st["value"] != "cancelled" {
		t.Fatalf("status = %v after self-cancel, want cancelled", st["value"])
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-10T15:00:00Z")
}

// TestClinic_SetAppointmentStatusConsumerSelfScope_NonCancelRejected proves the
// self grant's value restriction: even a legitimate self-service caller (the
// real patient, correctly identity-bound) may never set anything but
// cancelled — confirmed/checkedIn/completed/noShow stay operator-only.
func TestClinic_SetAppointmentStatusConsumerSelfScope_NonCancelRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "noncancel-consumer-self")

	clSeedVertex(t, ctx, conn, clConsumerKey, "identity", false)

	patientID := clSubmitOpt(t, ctx, conn, cp, cons, "nclpat0001", "CreatePatient", "patient",
		`{"fullName":"Self NoShow Patient","identityKey":"`+clConsumerKey+`"}`,
		[]string{clConsumerKey}, []string{clConsumerKey + ".patientClaim"}, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerKey := createProvider(t, ctx, conn, cp, cons, "nclprv0001", "Dr. Ines Rocha", "Family Medicine")

	apptID := clSubmit(t, ctx, conn, cp, cons, "nclappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-10T15:00:00Z","endsAt":"2026-07-10T15:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	identifiedByLnk := "lnk.patient." + patientID + ".identifiedBy.identity." + clConsumerID

	reqID := testutil.GenReqID("selfnoshow01")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "SetAppointmentStatus",
		Actor:         clConsumerKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"appointmentKey":"` + apptKey + `","status":"noShow","provider":"` + providerKey + `","patient":"` + patientKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{apptKey}, OptionalReads: []string{identifiedByLnk}},
		AuthContext:   &processor.AuthContext{Target: clConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	status := clReadDoc(t, ctx, conn, apptKey+".status")
	if st, _ := status["data"].(map[string]any); st["value"] != "scheduled" {
		t.Fatalf("status = %v after a rejected self noShow attempt, want scheduled (unchanged)", st["value"])
	}
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-10T15:00:00Z")
}

// TestClinic_SetAppointmentStatusConsumerSelfScope_UnlinkedPatientRejected
// mirrors the reschedule/CreateAppointment forgery tests: a consumer
// satisfying step 3 (target == actor) but naming an appointment whose patient
// is NOT linked to their own identity must still be rejected, even for the
// otherwise-permitted cancelled value.
func TestClinic_SetAppointmentStatusConsumerSelfScope_UnlinkedPatientRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "cancel-consumer-forge")

	victimPatientKey := createPatient(t, ctx, conn, cp, cons, "viccanpat001", "Unlinked Cancel Victim")
	providerKey := createProvider(t, ctx, conn, cp, cons, "viccanprv001", "Dr. Owen Reyes", "Pediatrics")

	apptID := clSubmit(t, ctx, conn, cp, cons, "viccanappt01", "CreateAppointment", "appointment",
		`{"patient":"`+victimPatientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-10T15:00:00Z","endsAt":"2026-07-10T15:30:00Z"}`,
		[]string{victimPatientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	victimPatientID := victimPatientKey[len("vtx.patient."):]
	identifiedByLnk := "lnk.patient." + victimPatientID + ".identifiedBy.identity." + clConsumerID

	reqID := testutil.GenReqID("forgecancel1")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "SetAppointmentStatus",
		Actor:         clConsumerKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"appointmentKey":"` + apptKey + `","status":"cancelled","provider":"` + providerKey + `","patient":"` + victimPatientKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{apptKey}, OptionalReads: []string{identifiedByLnk}},
		AuthContext:   &processor.AuthContext{Target: clConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	status := clReadDoc(t, ctx, conn, apptKey+".status")
	if st, _ := status["data"].(map[string]any); st["value"] != "scheduled" {
		t.Fatalf("status = %v after a rejected self-cancel forgery attempt, want scheduled (unchanged)", st["value"])
	}
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-10T15:00:00Z")
}

// clCreateAppointmentWithLease submits CreateAppointment with an optional
// leaseAppKey — mirrors wellness-domain integration_test.go's createBooking
// helper, declaring the lease/tenancy reads as (d) optionalReads (absent is
// the common no-lease case), never a hard requirement.
func clCreateAppointmentWithLease(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, patientKey, providerKey, leaseAppKey string, want processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payloadMap := map[string]any{
		"patient": patientKey, "provider": providerKey,
		"startsAt": "2026-07-01T15:00:00Z", "endsAt": "2026-07-01T15:30:00Z",
	}
	optionalReads := []string{}
	if leaseAppKey != "" {
		payloadMap["leaseAppKey"] = leaseAppKey
		optionalReads = append(optionalReads, leaseAppKey, leaseAppKey+".tenancy")
	}
	payload, _ := json.Marshal(payloadMap)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAppointment",
		Actor:         clStaffActorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       payload,
		ContextHint: &processor.ContextHint{Reads: []string{patientKey, providerKey}, OptionalReads: optionalReads,
			Enumerations: testutil.DeclaredEnumerations("CreateAppointment", clStaffActorKey, clinicdomain.OpMetas())},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return clNanoIDFromRequestID(reqID)
}

// TestClinic_CreateAppointment_ResidentVisitWhenLeaseMatchesPatient proves the
// Inc 5 confinement: a leaseAppKey whose applicationFor identity matches the
// booked patient's own identifiedBy identity, AND carries a live .tenancy
// aspect, gets a residentVisit link (appointment→leaseapp) — mirrors
// wellness-domain's TestCreateBooking_ResidentRateWhenLeaseMatchesBooker.
func TestClinic_CreateAppointment_ResidentVisitWhenLeaseMatchesPatient(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "resident-visit-match")

	identityKey := "vtx.identity.CLrvmatchHJKMNPQRSTU"
	clSeedVertex(t, ctx, conn, identityKey, "identity", false)
	patientID := clSubmitOpt(t, ctx, conn, cp, cons, "rvmatchpat01", "CreatePatient", "patient",
		`{"fullName":"Rae Visitor","identityKey":"`+identityKey+`"}`,
		[]string{identityKey}, []string{identityKey + ".patientClaim"}, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerKey := createProvider(t, ctx, conn, cp, cons, "rvmatchprv01", "Dr. Owen Reyes", "Pediatrics")

	leaseKey := clSeedLease(t, ctx, conn, "CLrvmatchLeaseHJKMNP", "CLrvmatchHJKMNPQRSTU", true)

	apptID := clCreateAppointmentWithLease(t, ctx, conn, cp, cons, "rvmatchappt1", patientKey, providerKey, leaseKey, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	residentVisitLnk := "lnk.appointment." + apptID + ".residentVisit.leaseapp.CLrvmatchLeaseHJKMNP"
	ldoc := clReadDoc(t, ctx, conn, residentVisitLnk)
	if ldoc["class"] != "residentVisit" {
		t.Fatalf("residentVisit link class = %v, want residentVisit", ldoc["class"])
	}
	if ldoc["sourceVertex"] != apptKey || ldoc["targetVertex"] != leaseKey {
		t.Fatalf("residentVisit link endpoints = src %v tgt %v, want %v/%v", ldoc["sourceVertex"], ldoc["targetVertex"], apptKey, leaseKey)
	}
}

// TestClinic_CreateAppointment_MismatchedLeaseFallsBack proves a leaseAppKey
// whose applicant is a DIFFERENT identity than the patient's own falls through
// silently — no residentVisit link, and the appointment still commits (a
// confinement hint, never a hard requirement) — mirrors wellness-domain's
// TestCreateBooking_MismatchedLeaseFallsBackToStandardRate.
func TestClinic_CreateAppointment_MismatchedLeaseFallsBack(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "resident-visit-mismatch")

	patientIdentityKey := "vtx.identity.CLrvmisPatHJKMNPQRST"
	clSeedVertex(t, ctx, conn, patientIdentityKey, "identity", false)
	patientID := clSubmitOpt(t, ctx, conn, cp, cons, "rvmispat0001", "CreatePatient", "patient",
		`{"fullName":"Mira Mismatch","identityKey":"`+patientIdentityKey+`"}`,
		[]string{patientIdentityKey}, []string{patientIdentityKey + ".patientClaim"}, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerKey := createProvider(t, ctx, conn, cp, cons, "rvmisprv0001", "Dr. Owen Reyes", "Pediatrics")

	// The lease's applicant is a DIFFERENT identity — never the patient's own.
	otherIdentityID := "CLrvmisDiffHJKMNPQRS"
	clSeedVertex(t, ctx, conn, "vtx.identity."+otherIdentityID, "identity", false)
	leaseKey := clSeedLease(t, ctx, conn, "CLrvmisLeaseHJKMNPQR", otherIdentityID, true)

	apptID := clCreateAppointmentWithLease(t, ctx, conn, cp, cons, "rvmisappt001", patientKey, providerKey, leaseKey, processor.OutcomeAccepted)

	residentVisitLnk := "lnk.appointment." + apptID + ".residentVisit.leaseapp.CLrvmisLeaseHJKMNPQR"
	if !clMissing(t, ctx, conn, residentVisitLnk) {
		t.Fatalf("a residentVisit link was written for a lease applicant that does not match the patient's own identity")
	}
}

// TestClinic_CreateAppointment_PendingLeaseFallsBack proves a leaseAppKey
// whose applicationFor link matches the patient but carries NO .tenancy
// aspect (a pending or declined application) falls through silently — no
// residentVisit link — mirrors wellness-domain's
// TestCreateBooking_PendingLeaseFallsBackToStandardRate.
func TestClinic_CreateAppointment_PendingLeaseFallsBack(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "resident-visit-pending")

	identityKey := "vtx.identity.CLrvpendHJKMNPQRSTUV"
	clSeedVertex(t, ctx, conn, identityKey, "identity", false)
	patientID := clSubmitOpt(t, ctx, conn, cp, cons, "rvpendpat001", "CreatePatient", "patient",
		`{"fullName":"Penny Pending","identityKey":"`+identityKey+`"}`,
		[]string{identityKey}, []string{identityKey + ".patientClaim"}, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + patientID
	providerKey := createProvider(t, ctx, conn, cp, cons, "rvpendprv001", "Dr. Owen Reyes", "Pediatrics")

	// applicationFor link present, but NO .tenancy — a pending/undecided lease.
	leaseKey := clSeedLease(t, ctx, conn, "CLrvpendLeaseHJKMNPQ", "CLrvpendHJKMNPQRSTUV", false)

	apptID := clCreateAppointmentWithLease(t, ctx, conn, cp, cons, "rvpendappt01", patientKey, providerKey, leaseKey, processor.OutcomeAccepted)

	residentVisitLnk := "lnk.appointment." + apptID + ".residentVisit.leaseapp.CLrvpendLeaseHJKMNPQ"
	if !clMissing(t, ctx, conn, residentVisitLnk) {
		t.Fatalf("residentVisit link must NOT be written for a pending/undecided lease: %s", residentVisitLnk)
	}
}
