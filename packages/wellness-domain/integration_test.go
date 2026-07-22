// wellness-domain integration tests through the real install + Processor
// pipeline. External test package (wellnessdomain_test) so they exercise the
// public Lattice surface: seed the kernel, install rbac + identity + hygiene
// + orchestration-base + service-domain + lease-signing + wellness-domain
// through the Processor, then submit the ops and assert the committed
// Core-KV shape + the emitted events.
package wellnessdomain_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	servicedomain "github.com/operatinggraph/lattice/packages/service-domain"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

const (
	domainActorID  = "BBWELLNESSDMNACTHJKM"
	domainActorKey = "vtx.identity." + domainActorID
	domainCapKey   = "cap.identity." + domainActorID

	domainConsumerRoleID = "BBConsumerRoZeWnessD"

	// domainConsumerID stands in for identity-domain's real `consumer` role
	// grant flow (mirrors clinic-domain's clConsumerID) — the self-service
	// caller's own identity, distinct from the operator actor above.
	domainConsumerID  = "BBWELLNESSCQNSHJKMNP"
	domainConsumerKey = "vtx.identity." + domainConsumerID
	domainConsumerCap = "cap.identity." + domainConsumerID
)

// domainConsumerCapDoc grants the consumer role's scope=self CreateBooking /
// CancelBooking permissions — the real-actor-write-auth-e2e self-service
// caller, mirrors clinic-domain's clConsumerCapDoc.
func domainConsumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    domainConsumerCap,
		Actor:                  domainConsumerKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{domainConsumerKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateBooking", Scope: "self"},
			{OperationType: "CancelBooking", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

func domainCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    domainCapKey,
		Actor:                  domainActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{domainActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateLeaseApplication", Scope: "any"},
			{OperationType: "CreateStudio", Scope: "any"},
			{OperationType: "TombstoneStudio", Scope: "any"},
			{OperationType: "CreateSession", Scope: "any"},
			{OperationType: "TombstoneSession", Scope: "any"},
			{OperationType: "CreateBooking", Scope: "any"},
			{OperationType: "CancelBooking", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

func setupDomainEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // rbac + identity + hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID, "consumer": domainConsumerRoleID, "frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"), "backOfHouse": pkgmgr.RoleID("identity-domain", "backOfHouse")}
	if _, err := inst.Install(ctx, orchestrationbase.Package); err != nil {
		t.Fatalf("install orchestration-base: %v", err)
	}
	if _, err := inst.Install(ctx, servicedomain.Package); err != nil {
		t.Fatalf("install service-domain: %v", err)
	}
	if _, err := inst.Install(ctx, leasesigning.Package); err != nil {
		t.Fatalf("install lease-signing: %v", err)
	}
	if _, err := inst.Install(ctx, wellnessdomain.Package); err != nil {
		t.Fatalf("install wellness-domain: %v", err)
	}
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	// The operator grant is only half the claim — the workplace-confinement
	// guard reads the holdsRole LINK to decide whether its caller is root.
	testutil.SeedHoldsRole(t, ctx, conn, domainActorKey, bootstrap.RoleOperatorKey)
	return ctx, conn
}

func newDomainPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "wd-" + durable,
	})
}

func nanoIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

func seedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string, data map[string]any) {
	t.Helper()
	if data == nil {
		data = map[string]any{}
	}
	doc := map[string]any{"class": class, "isDeleted": false, "data": data}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed vertex %s: %v", key, err)
	}
}

func seedLink(t *testing.T, ctx context.Context, conn *substrate.Conn, key, source, target, class, localName string) {
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

func readDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
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

func keyExists(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return false
	}
	if del, _ := doc["isDeleted"].(bool); del {
		return false
	}
	return true
}

func seedIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.identity." + id
	seedVertex(t, ctx, conn, key, "identity", map[string]any{})
	return key
}

// seedLease seeds a leaseapp vertex and, when applicant is non-empty, its
// applicationFor link — the residency check CreateBooking reads. withTenancy
// additionally stamps the .tenancy aspect DecideLeaseApplication's FIRST
// approve writes — CreateBooking requires its presence (not just a live
// applicationFor link) before granting the resident rate, so a pending or
// declined application (link alive, no .tenancy) must fall back to standard.
func seedLease(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseID, applicantID string, withTenancy bool) string {
	t.Helper()
	key := "vtx.leaseapp." + leaseID
	seedVertex(t, ctx, conn, key, "leaseapp", map[string]any{})
	if applicantID != "" {
		lnk := "lnk.leaseapp." + leaseID + ".applicationFor.identity." + applicantID
		seedLink(t, ctx, conn, lnk, key, "vtx.identity."+applicantID, "applicationFor", "applicationFor")
	}
	if withTenancy {
		seedVertex(t, ctx, conn, key+".tenancy", "tenancy", map[string]any{"leaseStart": "2026-08-01T00:00:00Z", "leaseEnd": "2027-07-31T00:00:00Z"})
	}
	return key
}

func createStudio(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, name string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateStudio",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "studio",
		Payload:       json.RawMessage(`{"name":"` + name + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.studio." + nanoIDFromRequestID(reqID)
}

// atStudioLnkKey / forSessionLnkKey are the deterministic validation-link keys
// require_matching_studio / require_matching_session read (ddls.go) — (a)
// declared reads at the TombstoneSession / CancelBooking dispatch.
func atStudioLnkKey(t *testing.T, sessionKey, studioKey string) string {
	t.Helper()
	_, sessID, _ := substrate.ParseVertexKey(sessionKey)
	_, studioID, _ := substrate.ParseVertexKey(studioKey)
	return "lnk.session." + sessID + ".atStudio.studio." + studioID
}

func forSessionLnkKey(t *testing.T, bookingKey, sessionKey string) string {
	t.Helper()
	_, bookID, _ := substrate.ParseVertexKey(bookingKey)
	_, sessID, _ := substrate.ParseVertexKey(sessionKey)
	return "lnk.booking." + bookID + ".forSession.session." + sessID
}

// wdSlotCellCode mirrors the package's slot_cellcode Starlark helper (strip
// '-'/':' and lowercase) so a test dispatcher can declare a covered cell's
// slot-claim key, script-read-posture-design.md §13.
func wdSlotCellCode(cellStart string) string {
	s := strings.ReplaceAll(cellStart, "-", "")
	s = strings.ReplaceAll(s, ":", "")
	return strings.ToLower(s)
}

// wdSlotClaimKeys enumerates the 15-minute cells [startsAt, endsAt) covers
// (mirroring slot_cells/enforce_grid, ddls.go) into their hub slot-claim keys.
func wdSlotClaimKeys(t *testing.T, hub, startsAt, endsAt string) []string {
	t.Helper()
	start, err := time.Parse(time.RFC3339, startsAt)
	if err != nil {
		t.Fatalf("parse startsAt %q: %v", startsAt, err)
	}
	end, err := time.Parse(time.RFC3339, endsAt)
	if err != nil {
		t.Fatalf("parse endsAt %q: %v", endsAt, err)
	}
	var keys []string
	for cur := start; cur.Before(end); cur = cur.Add(15 * time.Minute) {
		keys = append(keys, hub+".slot"+wdSlotCellCode(cur.UTC().Format(time.RFC3339)))
	}
	return keys
}

// wdSeatKeys enumerates a session's seat-claim aspect keys up to capacity,
// mirroring claim_first_free_seat's bounded loop (ddls.go).
func wdSeatKeys(sessionKey string, capacity int) []string {
	keys := make([]string, 0, capacity)
	for n := 1; n <= capacity; n++ {
		keys = append(keys, sessionKey+".seat"+strconv.Itoa(n))
	}
	return keys
}

func createSession(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, studioKey, name, startsAt, endsAt string, capacity int) (string, processor.MessageOutcome) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload, _ := json.Marshal(map[string]any{
		"studio": studioKey, "name": name, "startsAt": startsAt, "endsAt": endsAt, "capacity": capacity,
	})
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateSession",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "session",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         []string{studioKey},
			OptionalReads: wdSlotClaimKeys(t, studioKey, startsAt, endsAt),
		},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	return "vtx.session." + nanoIDFromRequestID(reqID), outcome
}

func createBooking(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, sessionKey, bookerKey, leaseAppKey string) (string, processor.MessageOutcome) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payloadMap := map[string]any{"session": sessionKey, "booker": bookerKey}
	reads := []string{sessionKey, sessionKey + ".schedule", bookerKey}
	// Resident-rate lookup (leaseapp + .tenancy + applicationFor link) is
	// (d)-declared optionalReads — absent falls through to the standard rate
	// (ddls.go, script-read-posture-design.md §13). Seat claims are the same
	// class over the session's capacity dimension (20 covers every capacity
	// this suite's fixtures use; claim_first_free_seat bounds it at 200).
	optionalReads := wdSeatKeys(sessionKey, 20)
	if leaseAppKey != "" {
		payloadMap["leaseAppKey"] = leaseAppKey
		_, leaseID, _ := substrate.ParseVertexKey(leaseAppKey)
		_, bookerID, _ := substrate.ParseVertexKey(bookerKey)
		optionalReads = append(optionalReads, leaseAppKey, leaseAppKey+".tenancy",
			"lnk.leaseapp."+leaseID+".applicationFor.identity."+bookerID)
	}
	payload, _ := json.Marshal(payloadMap)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "booking",
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Reads: reads, OptionalReads: optionalReads},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	return "vtx.booking." + nanoIDFromRequestID(reqID), outcome
}

func TestCreateStudio_MintsStudioWithProfile(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "createstudio")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000001", "Sunrise Yoga Room")

	studioDoc := readDoc(t, ctx, conn, studioKey)
	if d, _ := studioDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("studio root data must stay minimal ({}), got %v", d)
	}
	studioID := studioKey[len("vtx.studio."):]
	profileDoc := readDoc(t, ctx, conn, "vtx.studio."+studioID+".profile")
	profileData, _ := profileDoc["data"].(map[string]any)
	if got, _ := profileData["name"].(string); got != "Sunrise Yoga Room" {
		t.Fatalf("profile.name = %q, want Sunrise Yoga Room", got)
	}
}

func TestCreateSession_ClaimsStudioSlotCells(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "createsession")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000002", "Flow Room")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000001", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession outcome = %v, want Accepted", outcome)
	}

	schedDoc := readDoc(t, ctx, conn, sessionKey+".schedule")
	schedData, _ := schedDoc["data"].(map[string]any)
	if got, _ := schedData["capacity"].(float64); got != 20 {
		t.Fatalf("schedule.capacity = %v, want 20", got)
	}

	// [09:00, 09:30) covers exactly 2 cells: 09:00 and 09:15.
	for _, cc := range []string{"20260708t090000z", "20260708t091500z"} {
		if !keyExists(t, ctx, conn, studioKey+".slot"+cc) {
			t.Fatalf("expected studioSlotClaim at %s", cc)
		}
	}
}

func TestCreateSession_RejectsStudioDoubleBook(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "sessiondoublebook")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000003", "Flow Room")
	_, first := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000002", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if first != processor.OutcomeAccepted {
		t.Fatalf("first CreateSession outcome = %v, want Accepted", first)
	}
	_, second := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000003", studioKey, "Power Sculpt", "2026-07-08T09:15:00Z", "2026-07-08T09:45:00Z", 20)
	if second != processor.OutcomeRejected {
		t.Fatalf("overlapping CreateSession outcome = %v, want Rejected (StudioConflict)", second)
	}
}

func TestCreateBooking_StandardRate(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "bookingstandard")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000004", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000004", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	bookerKey := seedIdentity(t, ctx, conn, "BBWELLBKERSTDHJKMNPQ")

	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000001", sessionKey, bookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", outcome)
	}

	statusDoc := readDoc(t, ctx, conn, bookingKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["rate"].(string); got != "standard" {
		t.Fatalf("status.rate = %q, want standard", got)
	}
	if got, _ := statusData["seat"].(float64); got != 1 {
		t.Fatalf("status.seat = %v, want 1 (first claimed seat)", got)
	}
	if !keyExists(t, ctx, conn, sessionKey+".seat1") {
		t.Fatalf("expected sessionSeatClaim at seat1")
	}
}

func TestCreateBooking_ResidentRateWhenLeaseMatchesBooker(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "bookingresident")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000005", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000005", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	bookerID := "BBWELLBKERRESHJKMNPQ"
	bookerKey := seedIdentity(t, ctx, conn, bookerID)
	leaseKey := seedLease(t, ctx, conn, "BBWELLLEASEHLDRHJKMN", bookerID, true)

	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000002", sessionKey, bookerKey, leaseKey)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", outcome)
	}

	statusDoc := readDoc(t, ctx, conn, bookingKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["rate"].(string); got != "resident" {
		t.Fatalf("status.rate = %q, want resident", got)
	}

	bookingID := bookingKey[len("vtx.booking."):]
	leaseID := "BBWELLLEASEHLDRHJKMN"
	residentLnk := "lnk.booking." + bookingID + ".residentRate.leaseapp." + leaseID
	if !keyExists(t, ctx, conn, residentLnk) {
		t.Fatalf("expected residentRate link: %s", residentLnk)
	}
}

func TestCreateBooking_MismatchedLeaseFallsBackToStandardRate(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "bookingmismatch")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000006", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000006", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	bookerKey := seedIdentity(t, ctx, conn, "BBWELLBKERMSMHJKMNPQ")
	// Lease applicationFor a DIFFERENT identity — the booker never over-grants
	// the resident rate by merely naming someone else's lease.
	otherApplicantID := "BBWELLALTAPPLCJKMNPQ"
	seedIdentity(t, ctx, conn, otherApplicantID)
	leaseKey := seedLease(t, ctx, conn, "BBWELLMSMATCHLEASHJK", otherApplicantID, true)

	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000003", sessionKey, bookerKey, leaseKey)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted (rate falls back, never rejected)", outcome)
	}

	statusDoc := readDoc(t, ctx, conn, bookingKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["rate"].(string); got != "standard" {
		t.Fatalf("status.rate = %q, want standard (mismatched lease falls back)", got)
	}
}

// TestCreateBooking_PendingLeaseFallsBackToStandardRate proves a lease whose
// applicationFor link matches the booker but that was never approved (no
// .tenancy aspect — pending, or declined) does NOT qualify for the resident
// rate: an over-grant a live-but-undecided applicationFor link alone would
// allow (the booker is named on the lease, but never became a tenant).
func TestCreateBooking_PendingLeaseFallsBackToStandardRate(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "bookingpending")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000010", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000011", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	bookerID := "BBWELLBKERPNDHJKMNPQ"
	bookerKey := seedIdentity(t, ctx, conn, bookerID)
	leaseKey := seedLease(t, ctx, conn, "BBWELLPNDNGLEASEHJKM", bookerID, false)

	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000008", sessionKey, bookerKey, leaseKey)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted (rate falls back, never rejected)", outcome)
	}

	statusDoc := readDoc(t, ctx, conn, bookingKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["rate"].(string); got != "standard" {
		t.Fatalf("status.rate = %q, want standard (pending/undecided lease — no .tenancy — falls back)", got)
	}
	bookingID := bookingKey[len("vtx.booking."):]
	residentLnk := "lnk.booking." + bookingID + ".residentRate.leaseapp.BBWELLPNDNGLEASEHJKM"
	if keyExists(t, ctx, conn, residentLnk) {
		t.Fatalf("residentRate link must NOT be written for a pending/undecided lease: %s", residentLnk)
	}
}

func TestCreateBooking_RejectsWhenSessionFull(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "bookingfull")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000007", "Small Studio")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000007", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)

	bookerOneKey := seedIdentity(t, ctx, conn, "BBWELLBKERFUL1HJKMNP")
	_, firstOutcome := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000004", sessionKey, bookerOneKey, "")
	if firstOutcome != processor.OutcomeAccepted {
		t.Fatalf("first CreateBooking outcome = %v, want Accepted", firstOutcome)
	}

	bookerTwoKey := seedIdentity(t, ctx, conn, "BBWELLBKERFUL2HJKMNP")
	_, secondOutcome := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000005", sessionKey, bookerTwoKey, "")
	if secondOutcome != processor.OutcomeRejected {
		t.Fatalf("second CreateBooking outcome = %v, want Rejected (SessionFull)", secondOutcome)
	}
}

func TestCancelBooking_ReleasesSeatForNextClaimant(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "bookingcancel")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000008", "Small Studio")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000008", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)

	bookerOneKey := seedIdentity(t, ctx, conn, "BBWELLBKERCXL1HJKMNP")
	bookingKey, _ := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000006", sessionKey, bookerOneKey, "")

	cancelReqID := testutil.GenReqID("wdcancelbookin000001")
	cancelEnv := &processor.OperationEnvelope{
		RequestID:     cancelReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			bookingKey, bookingKey + ".status",
			forSessionLnkKey(t, bookingKey, sessionKey),
		}},
	}
	testutil.PublishOp(t, conn, cancelEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, sessionKey+".seat1") {
		t.Fatalf("seat1 must be released after CancelBooking")
	}

	bookerTwoKey := seedIdentity(t, ctx, conn, "BBWELLBKERCXL2HJKMNP")
	_, outcome := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000007", sessionKey, bookerTwoKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking after cancel outcome = %v, want Accepted (seat reclaimed)", outcome)
	}
}

func TestTombstoneSession_ReleasesStudioSlotCells(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "sessiontombstone")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000009", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000009", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)

	tombstoneReqID := testutil.GenReqID("wdtombstonsess000001")
	tombstoneEnv := &processor.OperationEnvelope{
		RequestID:     tombstoneReqID,
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneSession",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "session",
		Payload:       json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + studioKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			sessionKey, sessionKey + ".schedule",
			atStudioLnkKey(t, sessionKey, studioKey),
		}},
	}
	testutil.PublishOp(t, conn, tombstoneEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, sessionKey) {
		t.Fatalf("session must be tombstoned")
	}
	for _, cc := range []string{"20260708t090000z", "20260708t091500z"} {
		if keyExists(t, ctx, conn, studioKey+".slot"+cc) {
			t.Fatalf("studioSlotClaim at %s must be released after TombstoneSession", cc)
		}
	}

	// The freed cells are re-bookable.
	_, outcome := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000010", studioKey, "Power Sculpt", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession on freed cells outcome = %v, want Accepted", outcome)
	}
}

// TestCreateBooking_ConsumerSelfScope_Allowed proves a real resident, holding
// only the consumer scope=self grant, can book a class for THEMSELVES:
// payload.booker names their own identity and authContext.target matches it.
func TestCreateBooking_ConsumerSelfScope_Allowed(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "bookingselfok")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdselfstudio000001", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdselfsession000001", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	seedIdentity(t, ctx, conn, domainConsumerID)

	reqID := testutil.GenReqID("wdselfbooking000001")
	payload, _ := json.Marshal(map[string]any{"session": sessionKey, "booker": domainConsumerKey})
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateBooking",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "booking",
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Reads: []string{sessionKey, sessionKey + ".schedule", domainConsumerKey}, OptionalReads: wdSeatKeys(sessionKey, 20)},
		AuthContext:   &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self-service CreateBooking outcome = %v, want Accepted", outcome)
	}
}

// TestCreateBooking_ConsumerSelfScope_RejectedForOtherBooker proves the
// Starlark guard closes the gap step 3 leaves open: step 3's scope=self only
// checks authContext.target == actor, never looks at payload.booker. A
// consumer satisfying that check but naming a DIFFERENT identity as the
// booker must be rejected — self-service never lets one resident book on
// behalf of another.
func TestCreateBooking_ConsumerSelfScope_RejectedForOtherBooker(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "bookingselfother")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdselfstudio000002", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdselfsession000002", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	seedIdentity(t, ctx, conn, domainConsumerID)
	otherBookerKey := seedIdentity(t, ctx, conn, "BBWELLQTHERBKRHJKMNP")

	reqID := testutil.GenReqID("wdselfbooking000002")
	payload, _ := json.Marshal(map[string]any{"session": sessionKey, "booker": otherBookerKey})
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateBooking",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "booking",
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Reads: []string{sessionKey, sessionKey + ".schedule", otherBookerKey}, OptionalReads: wdSeatKeys(sessionKey, 20)},
		AuthContext:   &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-service CreateBooking for another booker outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// TestCancelBooking_ConsumerSelfScope_Allowed proves a real resident can
// cancel THEIR OWN booking: the booking's bookedBy link resolves to the
// caller's own authContext.target identity.
func TestCancelBooking_ConsumerSelfScope_Allowed(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "cancelselfok")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdselfstudio000003", "Small Studio")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdselfsession000003", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)
	seedIdentity(t, ctx, conn, domainConsumerID)
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdselfbookmine000001", sessionKey, domainConsumerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("setup CreateBooking outcome = %v, want Accepted", outcome)
	}

	bookingID := bookingKey[len("vtx.booking."):]
	bookedByLnk := "lnk.booking." + bookingID + ".bookedBy.identity." + domainConsumerID

	reqID := testutil.GenReqID("wdselfcancel000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{bookingKey, bookingKey + ".status", forSessionLnkKey(t, bookingKey, sessionKey)},
			OptionalReads: []string{bookedByLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	cancelOutcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if cancelOutcome != processor.OutcomeAccepted {
		t.Fatalf("self-service CancelBooking outcome = %v, want Accepted", cancelOutcome)
	}
}

// TestCancelBooking_ConsumerSelfScope_RejectedForOthersBooking proves a
// consumer satisfying step 3 (authContext.target == actor) but naming a
// booking that is NOT their own (a different bookedBy identity) is rejected
// — self-service never lets one resident cancel another's booking.
func TestCancelBooking_ConsumerSelfScope_RejectedForOthersBooking(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "cancelselfother")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdselfstudio000004", "Small Studio")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdselfsession000004", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)
	seedIdentity(t, ctx, conn, domainConsumerID)
	otherBookerKey := seedIdentity(t, ctx, conn, "BBWELLQTHERBKR2HJKMN")
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdselfbookoth000001", sessionKey, otherBookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("setup CreateBooking outcome = %v, want Accepted", outcome)
	}

	bookingID := bookingKey[len("vtx.booking."):]
	bookedByLnk := "lnk.booking." + bookingID + ".bookedBy.identity." + domainConsumerID

	reqID := testutil.GenReqID("wdselfcancel000002")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{bookingKey, bookingKey + ".status", forSessionLnkKey(t, bookingKey, sessionKey)},
			OptionalReads: []string{bookedByLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	cancelOutcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if cancelOutcome != processor.OutcomeRejected {
		t.Fatalf("self-service CancelBooking of another's booking outcome = %v, want Rejected (AuthDenied)", cancelOutcome)
	}
}
