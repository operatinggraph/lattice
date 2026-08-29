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
			{OperationType: "CreateSessionSeries", Scope: "any"},
			{OperationType: "TombstoneSession", Scope: "any"},
			{OperationType: "ReassignSession", Scope: "any"},
			{OperationType: "CreateBooking", Scope: "any"},
			{OperationType: "JoinWaitlist", Scope: "any"},
			{OperationType: "CancelBooking", Scope: "any"},
			{OperationType: "SetBookingAttendance", Scope: "any"},
			{OperationType: "ReleaseOrphanedBooking", Scope: "any"},
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
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID, "consumer": domainConsumerRoleID, "frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"), "backOfHouse": pkgmgr.RoleID("identity-domain", "backOfHouse"), "provider": pkgmgr.RoleID("identity-domain", "provider")}
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

// wdWaitlistKeys enumerates a session's waitlist-claim aspect keys up to n,
// mirroring claim_first_free_waitlist_slot's bounded loop (ddls.go). Tests
// pass a small n (not the full MAX_WAITLIST_SIZE=200) since the fixtures
// here never approach that bound.
func wdWaitlistKeys(sessionKey string, n int) []string {
	keys := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		keys = append(keys, sessionKey+".wl"+strconv.Itoa(i))
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

// createSessionPriced is createSession plus an optional priceCents — nil
// omits the field entirely (the free-class default), any other value
// (including a negative int, to exercise the rejection path) is sent
// verbatim.
func createSessionPriced(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, studioKey, name, startsAt, endsAt string, capacity int, priceCents any) (string, processor.MessageOutcome) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payloadMap := map[string]any{
		"studio": studioKey, "name": name, "startsAt": startsAt, "endsAt": endsAt, "capacity": capacity,
	}
	if priceCents != nil {
		payloadMap["priceCents"] = priceCents
	}
	payload, _ := json.Marshal(payloadMap)
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
	// The per-(session, booker) double-book guard (ddls.go) — declared so the
	// script can classify absent/tombstoned/alive (a re-book after cancel needs
	// the tombstoned revision to OCC-revive).
	_, bookerID, _ := substrate.ParseVertexKey(bookerKey)
	optionalReads = append(optionalReads, sessionKey+".bkr"+bookerID)
	if leaseAppKey != "" {
		payloadMap["leaseAppKey"] = leaseAppKey
		_, leaseID, _ := substrate.ParseVertexKey(leaseAppKey)
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

// joinWaitlist mirrors createBooking's dispatch shape exactly (same declared
// reads/optionalReads pattern, JoinWaitlist shares prepare_booking_common
// with CreateBooking) but declares wdWaitlistKeys instead of wdSeatKeys and
// dispatches JoinWaitlist.
func joinWaitlist(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, sessionKey, bookerKey, leaseAppKey string) (string, processor.MessageOutcome) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payloadMap := map[string]any{"session": sessionKey, "booker": bookerKey}
	reads := []string{sessionKey, sessionKey + ".schedule", bookerKey}
	optionalReads := wdWaitlistKeys(sessionKey, 20)
	_, bookerID, _ := substrate.ParseVertexKey(bookerKey)
	optionalReads = append(optionalReads, sessionKey+".bkr"+bookerID)
	if leaseAppKey != "" {
		payloadMap["leaseAppKey"] = leaseAppKey
		_, leaseID, _ := substrate.ParseVertexKey(leaseAppKey)
		optionalReads = append(optionalReads, leaseAppKey, leaseAppKey+".tenancy",
			"lnk.leaseapp."+leaseID+".applicationFor.identity."+bookerID)
	}
	payload, _ := json.Marshal(payloadMap)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "JoinWaitlist",
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

// attendanceEnv builds a SetBookingAttendance envelope with the read posture
// its dispatcher declares (ddls.go): the booking + its .status + the session's
// .schedule are required reads, the session-match and the two ownership probes
// are optionalReads. An empty instructor is the operator path, which supplies
// neither the param nor its probes.
func attendanceEnv(t *testing.T, label, bookingKey, sessionKey, status, instructorKey, actorKey, submittedAt string) *processor.OperationEnvelope {
	t.Helper()
	_, bookID, _ := substrate.ParseVertexKey(bookingKey)
	_, sessID, _ := substrate.ParseVertexKey(sessionKey)
	payloadMap := map[string]any{"bookingKey": bookingKey, "session": sessionKey, "status": status}
	optionalReads := []string{"lnk.booking." + bookID + ".forSession.session." + sessID}
	if instructorKey != "" {
		payloadMap["instructor"] = instructorKey
		_, instrID, _ := substrate.ParseVertexKey(instructorKey)
		_, actorID, _ := substrate.ParseVertexKey(actorKey)
		optionalReads = append(optionalReads,
			"lnk.session."+sessID+".ledBy.instructor."+instrID,
			"lnk.instructor."+instrID+".identifiedBy.identity."+actorID)
	}
	payload, _ := json.Marshal(payloadMap)
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetBookingAttendance",
		Actor:         actorKey,
		SubmittedAt:   submittedAt,
		Class:         "booking",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         []string{bookingKey, bookingKey + ".status", sessionKey + ".schedule"},
			OptionalReads: optionalReads,
		},
	}
}

func attendanceStatus(t *testing.T, ctx context.Context, conn *substrate.Conn, bookingKey string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, bookingKey+".status")
	data, _ := doc["data"].(map[string]any)
	return data
}

// TestSetBookingAttendance_CarriesFieldsForwardThenBlocksCancel is the load-
// bearing one. The attendance write replaces .status, carrying rate / seat /
// booker forward unchanged so ReleaseOrphanedBooking's convergence lens can
// still find them — but once attendance is recorded, CancelBooking must
// reject (AttendanceRecorded): a no-show is no longer member-erasable by
// cancelling the booking that recorded it.
func TestSetBookingAttendance_CarriesFieldsForwardThenBlocksCancel(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "attendcarry")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdattendstudio000001", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdattendsessio000001", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	bookerKey := seedIdentity(t, ctx, conn, "BBWELLATTNDCRYHJKMNP")

	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdattendbookin000001", sessionKey, bookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", outcome)
	}
	before := attendanceStatus(t, ctx, conn, bookingKey)

	testutil.PublishOp(t, conn, attendanceEnv(t, "wdattendmark00000001", bookingKey, sessionKey, "attended", "", domainActorKey, "2026-07-08T09:05:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	after := attendanceStatus(t, ctx, conn, bookingKey)
	if got, _ := after["value"].(string); got != "attended" {
		t.Fatalf("status.value = %q, want attended", got)
	}
	for _, field := range []string{"rate", "seat", "booker"} {
		if after[field] != before[field] {
			t.Errorf("status.%s = %v after marking, want %v carried forward unchanged", field, after[field], before[field])
		}
	}

	// A marked booking cannot be cancelled; this pins the guard.
	cancelEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wdattendcancel000001"),
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T09:40:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			bookingKey, bookingKey + ".status", sessionKey + ".schedule",
			forSessionLnkKey(t, bookingKey, sessionKey),
		}},
	}
	outcome2, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, cancelEnv)
	if outcome2 != processor.OutcomeRejected {
		t.Fatalf("CancelBooking on a marked booking outcome = %v, want Rejected", outcome2)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AttendanceRecorded") {
		t.Errorf("CancelBooking rejection should be AttendanceRecorded, got %+v", reply.Error)
	}

	if !keyExists(t, ctx, conn, sessionKey+".seat1") {
		t.Errorf("seat1 must NOT be released by a rejected CancelBooking")
	}
	_, bookerID, _ := substrate.ParseVertexKey(bookerKey)
	if !keyExists(t, ctx, conn, sessionKey+".bkr"+bookerID) {
		t.Errorf("the double-book guard must NOT be released by a rejected CancelBooking")
	}
}

// TestSetBookingAttendance_RejectsBeforeTheClassBegins: attendance is a record
// of what happened, so it cannot be taken on a class that has not started —
// the mirror of CreateBooking's SessionInPast.
func TestSetBookingAttendance_RejectsBeforeTheClassBegins(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "attendearly")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdattendstudio000002", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdattendsessio000002", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	bookerKey := seedIdentity(t, ctx, conn, "BBWELLATTNDERLHJKMNP")
	bookingKey, _ := createBooking(t, ctx, conn, cp, cons, "wdattendbookin000002", sessionKey, bookerKey, "")

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		attendanceEnv(t, "wdattendearly0000001", bookingKey, sessionKey, "attended", "", domainActorKey, "2026-07-08T08:59:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("marking a class that has not begun = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "SessionNotStarted") {
		t.Fatalf("rejection should be SessionNotStarted, got %+v", reply.Error)
	}

	// One minute past the start it is allowed — the boundary is the start
	// instant, not some later cutoff.
	testutil.PublishOp(t, conn, attendanceEnv(t, "wdattendontime000001", bookingKey, sessionKey, "attended", "", domainActorKey, "2026-07-08T09:00:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestSetBookingAttendance_EitherValueCorrectsTheOther: a mis-marked member is
// restated rather than stranded — cafe-domain's missing charge-correction op
// is the gap this avoids.
func TestSetBookingAttendance_EitherValueCorrectsTheOther(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "attendremark")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdattendstudio000003", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdattendsessio000003", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	bookerKey := seedIdentity(t, ctx, conn, "BBWELLATTNDRMKHJKMNP")
	bookingKey, _ := createBooking(t, ctx, conn, cp, cons, "wdattendbookin000003", sessionKey, bookerKey, "")

	for i, want := range []string{"noShow", "attended", "noShow"} {
		testutil.PublishOp(t, conn, attendanceEnv(t, "wdattendremark00000"+strconv.Itoa(i), bookingKey, sessionKey, want, "", domainActorKey, "2026-07-08T09:05:00Z"))
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
		if got, _ := attendanceStatus(t, ctx, conn, bookingKey)["value"].(string); got != want {
			t.Fatalf("after mark %d: status.value = %q, want %q", i, got, want)
		}
	}
}

// TestSetBookingAttendance_RejectsAnUnknownStatus: the aspect's enum is not
// merely documentation — an arbitrary value would land in the read model the
// roster renders.
func TestSetBookingAttendance_RejectsAnUnknownStatus(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "attendbadvalue")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdattendstudio000004", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdattendsessio000004", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	bookerKey := seedIdentity(t, ctx, conn, "BBWELLATTNDBADHJKMNP")
	bookingKey, _ := createBooking(t, ctx, conn, cp, cons, "wdattendbookin000004", sessionKey, bookerKey, "")

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		attendanceEnv(t, "wdattendbadval000001", bookingKey, sessionKey, "maybe", "", domainActorKey, "2026-07-08T09:05:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("an unknown status = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "must be attended or noShow") {
		t.Fatalf("rejection should name the permitted values, got %+v", reply.Error)
	}
	if got, _ := attendanceStatus(t, ctx, conn, bookingKey)["value"].(string); got != "booked" {
		t.Fatalf("status.value = %q after a rejected mark, want booked (untouched)", got)
	}
}

// TestSetBookingAttendance_NoShowFeeCents pins the three-way noShowFeeCents
// contract the automated pastDueBookings sweep depends on: omitted still
// defaults to 2500 (unchanged staff behavior), a caller-supplied positive
// value is stored as-is, a caller-supplied 0 (the sweep's "documentation
// lapse, not a billable no-show" signal) leaves the field off .status
// entirely rather than writing 0, and a negative value is rejected
// InvalidArgument.
func TestSetBookingAttendance_NoShowFeeCents(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "attendfee")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdattendstudio000005", "Flow Room")

	feeAttendanceEnv := func(label, bookingKey, sessionKey, submittedAt string, feeCents *float64) *processor.OperationEnvelope {
		_, bookID, _ := substrate.ParseVertexKey(bookingKey)
		_, sessID, _ := substrate.ParseVertexKey(sessionKey)
		payloadMap := map[string]any{"bookingKey": bookingKey, "session": sessionKey, "status": "noShow"}
		if feeCents != nil {
			payloadMap["noShowFeeCents"] = *feeCents
		}
		payload, _ := json.Marshal(payloadMap)
		return &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "SetBookingAttendance",
			Actor:         domainActorKey,
			SubmittedAt:   submittedAt,
			Class:         "booking",
			Payload:       payload,
			ContextHint: &processor.ContextHint{
				Reads:         []string{bookingKey, bookingKey + ".status", sessionKey + ".schedule"},
				OptionalReads: []string{"lnk.booking." + bookID + ".forSession.session." + sessID},
			},
		}
	}

	// Omitted: still defaults to 2500 — the unchanged staff-observed path.
	// Each sub-case gets its own non-overlapping time slot in the same
	// studio (a shared start/end would StudioConflict — the double-session
	// lock is scoped per covered 15-minute cell, not per booking).
	sessionKey1, _ := createSession(t, ctx, conn, cp, cons, "wdattendsessio000005", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	bookerKey1 := seedIdentity(t, ctx, conn, "BBWELLATTNDFEEDEFMNP")
	bookingKey1, outcome1 := createBooking(t, ctx, conn, cp, cons, "wdattendbookin000005", sessionKey1, bookerKey1, "")
	if outcome1 != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking (default-fee case) outcome = %v, want Accepted", outcome1)
	}
	testutil.PublishOp(t, conn, feeAttendanceEnv("wdattendfeedef00001", bookingKey1, sessionKey1, "2026-07-08T09:05:00Z", nil))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got, _ := attendanceStatus(t, ctx, conn, bookingKey1)["noShowFeeCents"].(float64); got != 2500 {
		t.Fatalf("omitted noShowFeeCents = %v, want default 2500", attendanceStatus(t, ctx, conn, bookingKey1)["noShowFeeCents"])
	}

	// A caller-supplied positive value is stored as-is.
	sessionKey2, _ := createSession(t, ctx, conn, cp, cons, "wdattendsessio000006", studioKey, "Vinyasa Flow", "2026-07-08T10:00:00Z", "2026-07-08T10:30:00Z", 20)
	bookerKey2 := seedIdentity(t, ctx, conn, "BBWELLATTNDFEEPSTMNP")
	bookingKey2, outcome2 := createBooking(t, ctx, conn, cp, cons, "wdattendbookin000006", sessionKey2, bookerKey2, "")
	if outcome2 != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking (positive-fee case) outcome = %v, want Accepted", outcome2)
	}
	positiveFee := 1500.0
	testutil.PublishOp(t, conn, feeAttendanceEnv("wdattendfeepos00001", bookingKey2, sessionKey2, "2026-07-08T10:05:00Z", &positiveFee))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got, _ := attendanceStatus(t, ctx, conn, bookingKey2)["noShowFeeCents"].(float64); got != 1500 {
		t.Fatalf("caller-supplied noShowFeeCents = %v, want 1500", attendanceStatus(t, ctx, conn, bookingKey2)["noShowFeeCents"])
	}

	// An explicit 0 (the automated sweep's signal) leaves the field off
	// .status entirely — never written as 0.
	sessionKey3, _ := createSession(t, ctx, conn, cp, cons, "wdattendsessio000007", studioKey, "Vinyasa Flow", "2026-07-08T11:00:00Z", "2026-07-08T11:30:00Z", 20)
	bookerKey3 := seedIdentity(t, ctx, conn, "BBWELLATTNDFEEZRSMNP")
	bookingKey3, outcome3 := createBooking(t, ctx, conn, cp, cons, "wdattendbookin000007", sessionKey3, bookerKey3, "")
	if outcome3 != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking (zero-fee case) outcome = %v, want Accepted", outcome3)
	}
	zeroFee := 0.0
	testutil.PublishOp(t, conn, feeAttendanceEnv("wdattendfeezer00001", bookingKey3, sessionKey3, "2026-07-08T11:05:00Z", &zeroFee))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if _, present := attendanceStatus(t, ctx, conn, bookingKey3)["noShowFeeCents"]; present {
		t.Fatalf("explicit noShowFeeCents:0 must never be written to .status (no-fee signal), got %v", attendanceStatus(t, ctx, conn, bookingKey3)["noShowFeeCents"])
	}

	// A negative value is rejected InvalidArgument, distinct from the "no
	// fee" meaning of an explicit 0.
	sessionKey4, _ := createSession(t, ctx, conn, cp, cons, "wdattendsessio000008", studioKey, "Vinyasa Flow", "2026-07-08T12:00:00Z", "2026-07-08T12:30:00Z", 20)
	bookerKey4 := seedIdentity(t, ctx, conn, "BBWELLATTNDFEENEGMP1")
	bookingKey4, outcome4 := createBooking(t, ctx, conn, cp, cons, "wdattendbookin000008", sessionKey4, bookerKey4, "")
	if outcome4 != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking (negative-fee case) outcome = %v, want Accepted", outcome4)
	}
	negativeFee := -100.0
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, feeAttendanceEnv("wdattendfeeneg00001", bookingKey4, sessionKey4, "2026-07-08T12:05:00Z", &negativeFee))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("negative noShowFeeCents outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") || !strings.Contains(reply.Error.Message, "noShowFeeCents") {
		t.Fatalf("rejection should be InvalidArgument naming noShowFeeCents, got %+v", reply.Error)
	}
}

// TestSetBookingAttendance_InstructorConfinedToTheirOwnClass: every bound
// instructor in the deployment holds the same standing scope=any grant, so the
// capability plane cannot tell one from another — the script's two-hop binder
// is the entire boundary. It also asserts the ORDER: naming the session's real
// leader must be indistinguishable from naming any other instructor, or one
// instructor reads off who leads a stranger's class from which denial returns.
func TestSetBookingAttendance_InstructorConfinedToTheirOwnClass(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "attendconfine")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	// The instructor's login. It holds exactly the standing grant every bound
	// instructor holds — nothing about the capability doc says WHICH classes.
	const instructorActorID = "BBWELLATTNDNSTRACTRK"
	instructorActorKey := "vtx.identity." + instructorActorID
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + instructorActorID,
		Actor:                  instructorActorKey,
		Version:                "1.0",
		ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{instructorActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "SetBookingAttendance", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "provider")},
	})

	mkInstructor := func(label, name string) string {
		reqID := testutil.GenReqID(label)
		testutil.PublishOp(t, conn, &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "CreateInstructor",
			Actor:         domainActorKey,
			SubmittedAt:   "2026-07-07T12:00:00Z",
			Class:         "instructor",
			Payload:       json.RawMessage(`{"displayName":"` + name + `"}`),
		})
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
		return "vtx.instructor." + nanoIDFromRequestID(reqID)
	}
	mineKey := mkInstructor("wdattendinstr000001", "Mine")
	theirsKey := mkInstructor("wdattendinstr000002", "Theirs")

	// The identifiedBy binding BindInstructorIdentity would mint. Seeded
	// directly so this test proves the attendance guard, not the bind.
	_, mineID, _ := substrate.ParseVertexKey(mineKey)
	seedLink(t, ctx, conn, "lnk.instructor."+mineID+".identifiedBy.identity."+instructorActorID,
		mineKey, instructorActorKey, "identifiedBy", "identifiedBy")

	ledSession := func(label, studioLabel, studioName, instructorKey, startsAt, endsAt string) string {
		studioKey := createStudio(t, ctx, conn, cp, cons, studioLabel, studioName)
		reqID := testutil.GenReqID(label)
		testutil.PublishOp(t, conn, &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "CreateSession",
			Actor:         domainActorKey,
			SubmittedAt:   "2026-07-07T12:00:00Z",
			Class:         "session",
			Payload: json.RawMessage(`{"studio":"` + studioKey + `","name":"Class","instructor":"` + instructorKey +
				`","startsAt":"` + startsAt + `","endsAt":"` + endsAt + `","capacity":4}`),
			ContextHint: &processor.ContextHint{
				Reads:         []string{studioKey, instructorKey},
				OptionalReads: wdSlotClaimKeys(t, studioKey, startsAt, endsAt),
			},
		})
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
		return "vtx.session." + nanoIDFromRequestID(reqID)
	}
	mySession := ledSession("wdattendsessio000005", "wdattendstudio000005", "Mine Room", mineKey, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z")
	theirSession := ledSession("wdattendsessio000006", "wdattendstudio000006", "Their Room", theirsKey, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z")

	// Two DISTINCT bookers — mySession and theirSession run the exact same
	// overlapping span at different studios (the fixture's point is testing
	// instructor confinement, not booker uniqueness), and one booker claimed
	// into both would now legitimately collide on bookerSlotClaim
	// (BookerConflict, ddls.go).
	booker := seedIdentity(t, ctx, conn, "BBWELLATTNDCNFHJKMNP")
	theirBooker := seedIdentity(t, ctx, conn, "BBWELLATTNDCNFHJKMNQ")
	myBooking, _ := createBooking(t, ctx, conn, cp, cons, "wdattendbookin000005", mySession, booker, "")
	theirBooking, _ := createBooking(t, ctx, conn, cp, cons, "wdattendbookin000006", theirSession, theirBooker, "")

	// The instructor marks a booking on the class they lead.
	testutil.PublishOp(t, conn, attendanceEnv(t, "wdattendmine00000001", myBooking, mySession, "attended", mineKey, instructorActorKey, "2026-07-08T09:05:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got, _ := attendanceStatus(t, ctx, conn, myBooking)["value"].(string); got != "attended" {
		t.Fatalf("a bound instructor must be able to mark their own class: status.value = %q", got)
	}

	// The same instructor, another instructor's class, naming themselves.
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		attendanceEnv(t, "wdattendtheirs000001", theirBooking, theirSession, "noShow", mineKey, instructorActorKey, "2026-07-08T09:05:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("marking another instructor's class = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "does not lead session") {
		t.Fatalf("rejection should be the ledBy denial, got %+v", reply.Error)
	}
	if got, _ := attendanceStatus(t, ctx, conn, theirBooking)["value"].(string); got != "booked" {
		t.Fatalf("another instructor's booking was written: status.value = %q, want booked", got)
	}

	// Naming an instructor they are NOT bound to must be answered by the
	// binding, not by the ledBy check — the real leader and a decoy must be
	// indistinguishable.
	// The candidate key is echoed back, but the caller supplied it, so it tells
	// them nothing they did not already know. What must NOT differ is the
	// denial itself — that is what would separate "this instructor leads that
	// class" from "this one does not".
	denial := func(label, candidate string) string {
		_, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
			attendanceEnv(t, label, theirBooking, theirSession, "attended", candidate, instructorActorKey, "2026-07-08T09:05:00Z"))
		if reply.Error == nil {
			t.Fatalf("%s: expected a rejection", label)
		}
		msg := reply.Error.Message
		i := strings.Index(msg, "fail: ")
		if i < 0 {
			t.Fatalf("%s: rejection carries no script failure: %s", label, msg)
		}
		return strings.ReplaceAll(msg[i+len("fail: "):], candidate, "<candidate>")
	}
	realLeader := denial("wdattendleader000001", theirsKey)
	decoy := denial("wdattenddecoy0000001", mkInstructor("wdattendinstr000003", "Decoy"))
	if realLeader != decoy {
		t.Errorf("naming a class's real leader is distinguishable from naming a stranger:\n  leader → %s\n  decoy  → %s", realLeader, decoy)
	}
	if !strings.Contains(realLeader, "not identifiedBy-bound") {
		t.Errorf("both should be answered by the caller's own binding, got %q", realLeader)
	}
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

// TestCreateSession_DeriveReadsClaimsCellsWithNoClientDeclaration proves the
// DDL's own derive_reads(op) (Contract #2 §2.5 class (g)) — not the caller —
// is what makes the studioSlotClaim cells hydrated and conflict-checked: the
// envelope here declares NO optionalReads at all, unlike every other
// CreateSession test in this file via the createSession() helper.
func TestCreateSession_DeriveReadsClaimsCellsWithNoClientDeclaration(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "deriveclaimcells")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdderivestudio000001", "Flow Room")
	reqID := testutil.GenReqID("wdderivesessio000001")
	payload, _ := json.Marshal(map[string]any{
		"studio": studioKey, "name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20,
	})
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateSession",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "session",
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Reads: []string{studioKey}},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession (no declared optionalReads) outcome = %v, want Accepted", outcome)
	}
	for _, cc := range []string{"20260708t090000z", "20260708t091500z"} {
		if !keyExists(t, ctx, conn, studioKey+".slot"+cc) {
			t.Fatalf("expected studioSlotClaim at %s (derived, not client-declared)", cc)
		}
	}
}

// TestCreateSession_DeriveReadsCatchesStudioDoubleBookWithNoClientDeclaration
// is the collision twin of the above: with no optionalReads on either
// submission, the SECOND overlapping CreateSession must still see the FIRST
// session's claim via derive_reads alone and reject (StudioConflict) — never
// a bare commit-time RevisionConflict, which would be indistinguishable at
// the OutcomeRejected level but is exactly the degraded error the DDL's own
// derive_reads doc comment (and this package's opmetas.go) says is now
// eliminated for every caller shape.
func TestCreateSession_DeriveReadsCatchesStudioDoubleBookWithNoClientDeclaration(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "deriveclaimconflict")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdderivestudio000002", "Flow Room")
	submit := func(label, name, startsAt, endsAt string) processor.MessageOutcome {
		reqID := testutil.GenReqID(label)
		payload, _ := json.Marshal(map[string]any{
			"studio": studioKey, "name": name, "startsAt": startsAt, "endsAt": endsAt, "capacity": 20,
		})
		env := &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "CreateSession",
			Actor:         domainActorKey,
			SubmittedAt:   "2026-07-07T12:00:00Z",
			Class:         "session",
			Payload:       payload,
			ContextHint:   &processor.ContextHint{Reads: []string{studioKey}},
		}
		testutil.PublishOp(t, conn, env)
		return testutil.DriveOne(t, ctx, cp, cons, "")
	}
	if first := submit("wdderivesessio000002", "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z"); first != processor.OutcomeAccepted {
		t.Fatalf("first CreateSession outcome = %v, want Accepted", first)
	}
	if second := submit("wdderivesessio000003", "Power Sculpt", "2026-07-08T09:15:00Z", "2026-07-08T09:45:00Z"); second != processor.OutcomeRejected {
		t.Fatalf("overlapping CreateSession outcome = %v, want Rejected (StudioConflict, via derive_reads alone)", second)
	}
}

// TestCreateSessionSeries_DeriveReadsClaimsCellsForEveryOccurrence exercises
// CreateSessionSeries's occurrence loop and derive_reads' matching loop
// together. Three weekly occurrences, no client-declared optionalReads:
// every occurrence's studioSlotClaim cells must be derived and claimed, not
// just the first.
func TestCreateSessionSeries_DeriveReadsClaimsCellsForEveryOccurrence(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "deriveseriescells")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdderiveseries000001", "Flow Room")
	reqID := testutil.GenReqID("wdderiveseries000002")
	payload, _ := json.Marshal(map[string]any{
		"studio": studioKey, "name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z",
		"capacity": 20, "intervalDays": 7, "occurrenceCount": 3,
	})
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateSessionSeries",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "sessionseries",
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Reads: []string{studioKey}},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSessionSeries (no declared optionalReads) outcome = %v, want Accepted", outcome)
	}

	// Three occurrences, 7 days apart, each covering the same [09:00, 09:30)
	// span (2 cells) — mirrors the script's own occ_starts/occ_ends offset
	// loop (ddls.go) independently of session/series id, which nanoid.new()
	// mints and this test cannot predict.
	occurrenceStarts := []string{"2026-07-08T09:00:00Z", "2026-07-15T09:00:00Z", "2026-07-22T09:00:00Z"}
	occurrenceEnds := []string{"2026-07-08T09:30:00Z", "2026-07-15T09:30:00Z", "2026-07-22T09:30:00Z"}
	for i := range occurrenceStarts {
		for _, key := range wdSlotClaimKeys(t, studioKey, occurrenceStarts[i], occurrenceEnds[i]) {
			if !keyExists(t, ctx, conn, key) {
				t.Fatalf("occurrence %d: expected studioSlotClaim at %s (derived, not client-declared)", i, key)
			}
		}
	}

	// A fourth, independent CreateSession overlapping the SECOND occurrence
	// must still collide — proving the series' derived claims are real
	// conflict-checked cells, not merely keys this test computed in parallel.
	_, collision := createSession(t, ctx, conn, cp, cons, "wdderiveseries000003", studioKey, "Power Sculpt", "2026-07-15T09:15:00Z", "2026-07-15T09:45:00Z", 20)
	if collision != processor.OutcomeRejected {
		t.Fatalf("CreateSession overlapping series occurrence 1 outcome = %v, want Rejected (StudioConflict)", collision)
	}
}

// TestCreateSession_StoresPriceCentsOnSchedule proves an explicit priceCents
// is stored on .schedule alongside capacity.
func TestCreateSession_StoresPriceCentsOnSchedule(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "sessionprice")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudioprc001", "Flow Room")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdcreatesessioprc001", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession outcome = %v, want Accepted", outcome)
	}

	schedDoc := readDoc(t, ctx, conn, sessionKey+".schedule")
	schedData, _ := schedDoc["data"].(map[string]any)
	if got, _ := schedData["priceCents"].(float64); got != 1500 {
		t.Fatalf("schedule.priceCents = %v, want 1500", got)
	}
}

// TestCreateSession_OmittedPriceCentsIsFree proves an omitted priceCents
// leaves the key absent from .schedule — a free class, not a stored zero.
func TestCreateSession_OmittedPriceCentsIsFree(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "sessionpricefree")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudioprc002", "Flow Room")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdcreatesessioprc002", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession outcome = %v, want Accepted", outcome)
	}

	schedDoc := readDoc(t, ctx, conn, sessionKey+".schedule")
	schedData, _ := schedDoc["data"].(map[string]any)
	if _, present := schedData["priceCents"]; present {
		t.Fatalf("schedule.priceCents must be absent when omitted, got %v", schedData["priceCents"])
	}
}

// TestCreateSession_RejectsNegativePriceCents proves a negative priceCents is
// InvalidArgument, not silently clamped or accepted.
func TestCreateSession_RejectsNegativePriceCents(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "sessionpriceneg")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudioprc003", "Flow Room")
	_, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdcreatesessioprc003", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20, -100)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("CreateSession with priceCents=-100 outcome = %v, want Rejected (InvalidArgument)", outcome)
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
			bookingKey, bookingKey + ".status", sessionKey + ".schedule",
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

// TestCreateBooking_RejectsDoubleBook proves the per-(session, booker)
// double-book guard: a booker already holding a live booking on a session
// cannot book it again. Capacity is 20 (not 1) so the SECOND booking cannot be
// rejected for SessionFull — the only thing that rejects it is the guard; and
// the FIRST booking being Accepted is the positive vector that keeps this from
// passing for the wrong reason (feedback_negative_test_false_pass).
func TestCreateBooking_RejectsDoubleBook(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "bookingdouble")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000020", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000020", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	bookerKey := seedIdentity(t, ctx, conn, "BBWELLBKERDBL2HJKMNP")

	_, first := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000020", sessionKey, bookerKey, "")
	if first != processor.OutcomeAccepted {
		t.Fatalf("first CreateBooking outcome = %v, want Accepted", first)
	}

	_, second := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000021", sessionKey, bookerKey, "")
	if second != processor.OutcomeRejected {
		t.Fatalf("second CreateBooking (same booker + session, seats remain) outcome = %v, want Rejected (DoubleBooked)", second)
	}

	// A DIFFERENT booker on the same session is unaffected — the guard is
	// per-(session, booker), not a per-session lock.
	otherBookerKey := seedIdentity(t, ctx, conn, "BBWELLBKERUTH2HJKMNP")
	_, other := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000022", sessionKey, otherBookerKey, "")
	if other != processor.OutcomeAccepted {
		t.Fatalf("a different booker on the same session outcome = %v, want Accepted", other)
	}
}

// TestCreateBooking_RejectsPastSession proves the soft past-time guard: a
// booking whose op.submittedAt is at or after the session's startsAt is
// rejected (SessionInPast), mirroring clinic's ScheduleInPast. The session is
// booked once BEFORE its start (Accepted, the positive vector) and once AFTER
// (Rejected) — same session, same capacity, so the only difference is time.
func TestCreateBooking_RejectsPastSession(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "bookingpast")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000021", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000021", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)

	// Positive vector: booking BEFORE the session starts is accepted (the
	// createBooking helper submits at 2026-07-07T12:00:00Z, well before start).
	earlyBookerKey := seedIdentity(t, ctx, conn, "BBWELLBKEREAR2HJKMNP")
	_, early := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000023", sessionKey, earlyBookerKey, "")
	if early != processor.OutcomeAccepted {
		t.Fatalf("booking before start outcome = %v, want Accepted", early)
	}

	// Negative vector: submittedAt AFTER the session's startsAt → SessionInPast.
	lateBookerKey := seedIdentity(t, ctx, conn, "BBWELLBKERLAT2HJKMNP")
	_, lateBookerID, _ := substrate.ParseVertexKey(lateBookerKey)
	reqID := testutil.GenReqID("wdcreatebookin000024")
	payload, _ := json.Marshal(map[string]any{"session": sessionKey, "booker": lateBookerKey})
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T10:00:00Z", // after the 09:00 start
		Class:         "booking",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         []string{sessionKey, sessionKey + ".schedule", lateBookerKey},
			OptionalReads: append(wdSeatKeys(sessionKey, 20), sessionKey+".bkr"+lateBookerID),
		},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("booking after session start outcome = %v, want Rejected (SessionInPast)", outcome)
	}
}

// TestCancelBooking_ReleasesGuardForRebook proves the double-book guard is
// released on cancel so the same booker can re-book the same session — the
// path that would silently fail if the guard were undeclared (its OCC-revive
// needs the tombstoned revision from state). Distinct from the seat-release
// test: same booker, re-booking the same session.
func TestCancelBooking_ReleasesGuardForRebook(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "bookingrebook")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcreatestudio000022", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdcreatesessio000022", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	bookerKey := seedIdentity(t, ctx, conn, "BBWELLBKERRBK2HJKMNP")

	bookingKey, first := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000025", sessionKey, bookerKey, "")
	if first != processor.OutcomeAccepted {
		t.Fatalf("first CreateBooking outcome = %v, want Accepted", first)
	}

	// A re-book WITHOUT cancelling must still be rejected (guard is live).
	_, dup := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000026", sessionKey, bookerKey, "")
	if dup != processor.OutcomeRejected {
		t.Fatalf("re-book before cancel outcome = %v, want Rejected (guard still live)", dup)
	}

	cancelReqID := testutil.GenReqID("wdcancelbookin000010")
	cancelEnv := &processor.OperationEnvelope{
		RequestID:     cancelReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			bookingKey, bookingKey + ".status", sessionKey + ".schedule",
			forSessionLnkKey(t, bookingKey, sessionKey),
		}},
	}
	testutil.PublishOp(t, conn, cancelEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The guard was released, so the booker can re-book — this OCC-revives the
	// tombstoned guard aspect.
	_, rebook := createBooking(t, ctx, conn, cp, cons, "wdcreatebookin000027", sessionKey, bookerKey, "")
	if rebook != processor.OutcomeAccepted {
		t.Fatalf("re-book after cancel outcome = %v, want Accepted (guard released + revived)", rebook)
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

// reassignSessionEnv builds a ReassignSession envelope with the read posture
// its dispatcher declares (ddls.go): the session + its schedule are required
// reads (mirroring TombstoneSession, which always declares .schedule even
// though only a time move actually consumes it); the atStudio validation
// link and the instructor-standing ownership probes are optionalReads,
// exactly like TombstoneSession's; a supplied newInstructor is a required
// read, exactly like CreateSession's instructor field.
func reassignSessionEnv(t *testing.T, label, sessionKey, studioKey, actorKey string, payload map[string]any, submittedAt string) *processor.OperationEnvelope {
	t.Helper()
	_, sessID, _ := substrate.ParseVertexKey(sessionKey)
	reads := []string{sessionKey, sessionKey + ".schedule"}
	optionalReads := []string{atStudioLnkKey(t, sessionKey, studioKey)}
	if instr, ok := payload["instructor"].(string); ok && instr != "" {
		_, instrID, _ := substrate.ParseVertexKey(instr)
		_, actorID, _ := substrate.ParseVertexKey(actorKey)
		optionalReads = append(optionalReads,
			"lnk.instructor."+instrID+".identifiedBy.identity."+actorID,
			"lnk.session."+sessID+".ledBy.instructor."+instrID)
	}
	if newInstr, ok := payload["newInstructor"].(string); ok && newInstr != "" {
		reads = append(reads, newInstr)
	}
	payloadBytes, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ReassignSession",
		Actor:         actorKey,
		SubmittedAt:   submittedAt,
		Class:         "session",
		Payload:       payloadBytes,
		ContextHint:   &processor.ContextHint{Reads: reads, OptionalReads: optionalReads},
	}
}

func sessionLedByLnkKey(t *testing.T, sessionKey, instructorKey string) string {
	t.Helper()
	_, sessID, _ := substrate.ParseVertexKey(sessionKey)
	_, instrID, _ := substrate.ParseVertexKey(instructorKey)
	return "lnk.session." + sessID + ".ledBy.instructor." + instrID
}

func TestReassignSession_SwapsInstructor(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassigninstr")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	mkInstructor := func(label, name string) string {
		reqID := testutil.GenReqID(label)
		testutil.PublishOp(t, conn, &processor.OperationEnvelope{
			RequestID: reqID, Lane: processor.LaneDefault, OperationType: "CreateInstructor",
			Actor: domainActorKey, SubmittedAt: "2026-07-07T12:00:00Z", Class: "instructor",
			Payload: json.RawMessage(`{"displayName":"` + name + `"}`),
		})
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
		return "vtx.instructor." + nanoIDFromRequestID(reqID)
	}
	instructorA := mkInstructor("wdreassigninstra0001", "A")
	instructorB := mkInstructor("wdreassigninstrb0001", "B")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdreassignstudio0001", "Flow Room")
	sessReqID := testutil.GenReqID("wdreassignsessio0001")
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID: sessReqID, Lane: processor.LaneDefault, OperationType: "CreateSession",
		Actor: domainActorKey, SubmittedAt: "2026-07-07T12:00:00Z", Class: "session",
		Payload: json.RawMessage(`{"studio":"` + studioKey + `","name":"Vinyasa Flow","instructor":"` + instructorA +
			`","startsAt":"2026-07-08T09:00:00Z","endsAt":"2026-07-08T09:30:00Z","capacity":20}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{studioKey, instructorA},
			OptionalReads: wdSlotClaimKeys(t, studioKey, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z"),
		},
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	sessionKey := "vtx.session." + nanoIDFromRequestID(sessReqID)
	if !keyExists(t, ctx, conn, sessionLedByLnkKey(t, sessionKey, instructorA)) {
		t.Fatalf("CreateSession did not write the initial ledBy link")
	}

	outcome, _ := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, reassignSessionEnv(t, "wdreassignswap00001",
		sessionKey, studioKey, domainActorKey, map[string]any{"sessionKey": sessionKey, "studio": studioKey, "newInstructor": instructorB},
		"2026-07-08T08:00:00Z"))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("ReassignSession newInstructor outcome = %v, want Accepted", outcome)
	}
	if keyExists(t, ctx, conn, sessionLedByLnkKey(t, sessionKey, instructorA)) {
		t.Errorf("the OLD ledBy link (instructor A) must be tombstoned after reassignment")
	}
	if !keyExists(t, ctx, conn, sessionLedByLnkKey(t, sessionKey, instructorB)) {
		t.Errorf("the NEW ledBy link (instructor B) must be live after reassignment")
	}
}

// TestReassignSession_OperatorMovesToNewStudio proves the newStudio repair
// capability's happy path (verticals.md "retiring a studio strands its
// classes"): an operator moves a still-live session from studio A to studio
// B. The atStudio link swaps (old tombstoned, new live) and the
// studioSlotClaim cells migrate hub-to-hub for the session's own span — a
// hub change, not a reschedule delta, so ALL of A's covered cells release and
// ALL of B's claim, mirroring the instructor hub-change branch this mirrors.
func TestReassignSession_OperatorMovesToNewStudio(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassignstudio")

	studioA := createStudio(t, ctx, conn, cp, cons, "wdrsmvstudioa000001", "Studio A")
	studioB := createStudio(t, ctx, conn, cp, cons, "wdrsmvstudiob000001", "Studio B")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdrsmvsession0000001",
		studioA, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession at studio A = %v, want Accepted", outcome)
	}
	oldCellKey := studioA + ".slot" + wdSlotCellCode("2026-07-08T09:00:00Z")
	newCellKey := studioB + ".slot" + wdSlotCellCode("2026-07-08T09:00:00Z")
	if !keyExists(t, ctx, conn, oldCellKey) {
		t.Fatalf("CreateSession did not claim %s on studio A", oldCellKey)
	}

	env := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("wdrsmvmove0000001"), Lane: processor.LaneDefault,
		OperationType: "ReassignSession", Actor: domainActorKey, SubmittedAt: "2026-07-08T08:00:00Z", Class: "session",
		Payload: json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + studioA + `","newStudio":"` + studioB + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{sessionKey, sessionKey + ".schedule", studioB},
			OptionalReads: append([]string{atStudioLnkKey(t, sessionKey, studioA)}, wdSlotClaimKeys(t, studioB, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z")...),
		},
	}
	outcome2, _ := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome2 != processor.OutcomeAccepted {
		t.Fatalf("ReassignSession newStudio (operator) outcome = %v, want Accepted", outcome2)
	}
	if keyExists(t, ctx, conn, atStudioLnkKey(t, sessionKey, studioA)) {
		t.Errorf("the OLD atStudio link (studio A) must be tombstoned after the move")
	}
	if !keyExists(t, ctx, conn, atStudioLnkKey(t, sessionKey, studioB)) {
		t.Errorf("the NEW atStudio link (studio B) must be live after the move")
	}
	if keyExists(t, ctx, conn, oldCellKey) {
		t.Errorf("studio A's slot cell must be released after the move")
	}
	if !keyExists(t, ctx, conn, newCellKey) {
		t.Errorf("studio B's slot cell must be claimed after the move")
	}

	// Move it BACK to studio A. The atStudio link and the studioSlotClaim
	// cell wdrsmvmove0000001 just tombstoned are both being asked to come
	// back alive at the SAME keys — a plain CreateOnly write (expectedRevision
	// 0) would reject RevisionConflict against a subject that already exists,
	// dead or not, which is exactly why the swap uses the revive-OCC idiom
	// (make_link_create_or_revive) instead of make_link.
	backEnv := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("wdrsmvmoveback0001"), Lane: processor.LaneDefault,
		OperationType: "ReassignSession", Actor: domainActorKey, SubmittedAt: "2026-07-08T08:01:00Z", Class: "session",
		Payload: json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + studioB + `","newStudio":"` + studioA + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{sessionKey, sessionKey + ".schedule", studioA},
			OptionalReads: append([]string{atStudioLnkKey(t, sessionKey, studioB)}, wdSlotClaimKeys(t, studioA, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z")...),
		},
	}
	outcome3, reply3 := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, backEnv)
	if outcome3 != processor.OutcomeAccepted {
		t.Fatalf("ReassignSession newStudio A->B->A round trip = %v, reply = %+v, want Accepted", outcome3, reply3)
	}
	if keyExists(t, ctx, conn, atStudioLnkKey(t, sessionKey, studioB)) {
		t.Errorf("the atStudio link to studio B must be tombstoned after moving back to A")
	}
	if !keyExists(t, ctx, conn, atStudioLnkKey(t, sessionKey, studioA)) {
		t.Errorf("the REVIVED atStudio link to studio A must be live after moving back")
	}
	if !keyExists(t, ctx, conn, oldCellKey) {
		t.Errorf("studio A's slot cell must be RE-CLAIMED (revived) after moving back")
	}
	if keyExists(t, ctx, conn, newCellKey) {
		t.Errorf("studio B's slot cell must be released after moving back")
	}
}

// TestReassignSession_NonOperatorCannotMoveStudio proves newStudio is
// operator-only regardless of hat: a front-of-house caller who otherwise
// holds a full ReassignSession grant (and correctly names the session's
// CURRENT studio) is still denied a studio move.
func TestReassignSession_NonOperatorCannotMoveStudio(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassignstudiodeny")

	studioA := createStudio(t, ctx, conn, cp, cons, "wdrsdnystudioa000001", "Studio A")
	studioB := createStudio(t, ctx, conn, cp, cons, "wdrsdnystudiob000001", "Studio B")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdrsdnysession0000001",
		studioA, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession at studio A = %v, want Accepted", outcome)
	}

	const staffActorID = "BBWELLRSDENYSTAFFHJK"
	staffActorKey := "vtx.identity." + staffActorID
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key: "cap.identity." + staffActorID, Actor: staffActorKey, Version: "1.0",
		ProjectedAt: time.Now().UTC().Format(time.RFC3339Nano), ProjectedFromRevisions: map[string]uint64{staffActorKey: 1},
		Lanes:               []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{{OperationType: "ReassignSession", Scope: "any"}},
		ServiceAccess:       []processor.ServiceAccessEntry{},
		EphemeralGrants:     []processor.EphemeralGrant{},
		Roles:               []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	})
	// NO holdsRole->operator link for staffActorKey — actor_holds_operator
	// must resolve false, the same graph-not-capdoc gate every other
	// operator-only test in this file relies on.

	env := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("wdrsdnymove0000001"), Lane: processor.LaneDefault,
		OperationType: "ReassignSession", Actor: staffActorKey, SubmittedAt: "2026-07-08T08:00:00Z", Class: "session",
		Payload: json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + studioA + `","newStudio":"` + studioB + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{sessionKey, sessionKey + ".schedule", studioB},
			OptionalReads: append([]string{atStudioLnkKey(t, sessionKey, studioA)}, wdSlotClaimKeys(t, studioB, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z")...),
		},
	}
	outcome2, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome2 != processor.OutcomeRejected {
		t.Fatalf("a non-operator moving a session's studio = %v, want Rejected", outcome2)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "only an operator may move a session") {
		t.Fatalf("rejection should be the studio-move AuthDenied, got %+v", reply.Error)
	}

	// The SAME staffer, this time omitting `studio` entirely (as if the
	// session's studio were already dead and unknown to the FE) and relying
	// only on newStudio — the OTHER auth surface this exercises (ddls.go's
	// "studio is required unless newStudio is also supplied by an operator"
	// branch), distinct from the studio_changed gate proven above.
	env2 := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("wdrsdnyrepair00001"), Lane: processor.LaneDefault,
		OperationType: "ReassignSession", Actor: staffActorKey, SubmittedAt: "2026-07-08T08:01:00Z", Class: "session",
		Payload: json.RawMessage(`{"sessionKey":"` + sessionKey + `","newStudio":"` + studioB + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{sessionKey, sessionKey + ".schedule", studioB},
			OptionalReads: wdSlotClaimKeys(t, studioB, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z"),
		},
	}
	outcome3, reply2 := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env2)
	if outcome3 != processor.OutcomeRejected {
		t.Fatalf("a non-operator omitting studio while supplying newStudio = %v, want Rejected", outcome3)
	}
	if reply2.Error == nil || !strings.Contains(reply2.Error.Message, "only an operator may reassign a session's studio without confirming the current one") {
		t.Fatalf("rejection should be the studio-omitted AuthDenied, got %+v", reply2.Error)
	}
}

// TestReassignSession_OperatorRepairsSessionWithTombstonedStudio is the
// board row's actual scenario (verticals.md "retiring a studio strands its
// classes"): TombstoneStudio already removed the session's studio — exactly
// as reapDuplicateStudios / an operator's own TombstoneStudio call leaves it
// — so wellnessSessionsSpec's projection can no longer hand a caller that
// dead key back to round-trip as `studio`. An operator omits `studio`
// entirely and supplies only newStudio; the script derives the CURRENT
// (dead) studio off the session's still-live atStudio link instead.
func TestReassignSession_OperatorRepairsSessionWithTombstonedStudio(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassignstudiofix")

	deadStudio := createStudio(t, ctx, conn, cp, cons, "wdrsfxstudioa000001", "Old Room")
	liveStudio := createStudio(t, ctx, conn, cp, cons, "wdrsfxstudiob000001", "New Room")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdrsfxsession0000001",
		deadStudio, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession at the soon-to-be-dead studio = %v, want Accepted", outcome)
	}
	tsEnv := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("wdrsfxtombstone01"), Lane: processor.LaneDefault,
		OperationType: "TombstoneStudio", Actor: domainActorKey, SubmittedAt: "2026-07-08T08:00:00Z", Class: "studio",
		Payload:     json.RawMessage(`{"studioKey":"` + deadStudio + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{deadStudio}},
	}
	testutil.PublishOp(t, conn, tsEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The atStudio link survives the studio's own tombstone (no cascade,
	// package.go) — this is what session_atstudio_link (ddls.go) reads.
	if !keyExists(t, ctx, conn, atStudioLnkKey(t, sessionKey, deadStudio)) {
		t.Fatalf("the atStudio link must survive TombstoneStudio (no-cascade doctrine)")
	}

	env := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("wdrsfxrepair0000001"), Lane: processor.LaneDefault,
		OperationType: "ReassignSession", Actor: domainActorKey, SubmittedAt: "2026-07-08T08:05:00Z", Class: "session",
		Payload: json.RawMessage(`{"sessionKey":"` + sessionKey + `","newStudio":"` + liveStudio + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{sessionKey, sessionKey + ".schedule", liveStudio},
			OptionalReads: wdSlotClaimKeys(t, liveStudio, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z"),
		},
	}
	outcome2, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome2 != processor.OutcomeAccepted {
		t.Fatalf("operator repair (studio omitted, newStudio supplied) outcome = %v, reply = %+v, want Accepted", outcome2, reply)
	}
	if !keyExists(t, ctx, conn, atStudioLnkKey(t, sessionKey, liveStudio)) {
		t.Errorf("the NEW atStudio link (live studio) must be live after the repair")
	}
	if keyExists(t, ctx, conn, atStudioLnkKey(t, sessionKey, deadStudio)) {
		t.Errorf("the OLD atStudio link (the tombstoned studio) must be tombstoned after the repair")
	}

	// Even an operator must supply ONE of studio or newStudio — omitting both
	// hits the InvalidArgument branch before the operator-only gate ever runs.
	badEnv := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("wdrsfxbadargs00001"), Lane: processor.LaneDefault,
		OperationType: "ReassignSession", Actor: domainActorKey, SubmittedAt: "2026-07-08T08:10:00Z", Class: "session",
		Payload:     json.RawMessage(`{"sessionKey":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{sessionKey, sessionKey + ".schedule"}},
	}
	outcome3, reply3 := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, badEnv)
	if outcome3 != processor.OutcomeRejected {
		t.Fatalf("omitting both studio and newStudio = %v, want Rejected", outcome3)
	}
	if reply3.Error == nil || !strings.Contains(reply3.Error.Message, "studio is required unless newStudio") {
		t.Fatalf("rejection should be the missing-studio InvalidArgument, got %+v", reply3.Error)
	}
}

// TestReassignSession_CarriesPriceCentsForwardOnInstructorSwap proves an
// instructor swap leaves .schedule.priceCents untouched, exactly like
// name/capacity.
func TestReassignSession_CarriesPriceCentsForwardOnInstructorSwap(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassignpriceinstr")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	reqID := testutil.GenReqID("wdreassignprcinstr01")
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID: reqID, Lane: processor.LaneDefault, OperationType: "CreateInstructor",
		Actor: domainActorKey, SubmittedAt: "2026-07-07T12:00:00Z", Class: "instructor",
		Payload: json.RawMessage(`{"displayName":"B"}`),
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	instructorB := "vtx.instructor." + nanoIDFromRequestID(reqID)

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdreassignprcstudio1", "Flow Room")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdreassignprcsessio1",
		studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession outcome = %v, want Accepted", outcome)
	}

	outcome2, _ := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, reassignSessionEnv(t, "wdreassignprcswap001",
		sessionKey, studioKey, domainActorKey, map[string]any{"sessionKey": sessionKey, "studio": studioKey, "newInstructor": instructorB},
		"2026-07-08T08:00:00Z"))
	if outcome2 != processor.OutcomeAccepted {
		t.Fatalf("ReassignSession newInstructor outcome = %v, want Accepted", outcome2)
	}

	schedDoc := readDoc(t, ctx, conn, sessionKey+".schedule")
	schedData, _ := schedDoc["data"].(map[string]any)
	if got, _ := schedData["priceCents"].(float64); got != 1500 {
		t.Errorf("schedule.priceCents = %v, must carry forward unchanged on an instructor swap", got)
	}
}

func TestReassignSession_ClearsInstructorWithNoReplacement(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassignclear")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	reqID := testutil.GenReqID("wdreassignclrinstr01")
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID: reqID, Lane: processor.LaneDefault, OperationType: "CreateInstructor",
		Actor: domainActorKey, SubmittedAt: "2026-07-07T12:00:00Z", Class: "instructor",
		Payload: json.RawMessage(`{"displayName":"A"}`),
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	instructorA := "vtx.instructor." + nanoIDFromRequestID(reqID)

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdreassignclrstudio1", "Flow Room")
	sessReqID := testutil.GenReqID("wdreassignclrsessio1")
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID: sessReqID, Lane: processor.LaneDefault, OperationType: "CreateSession",
		Actor: domainActorKey, SubmittedAt: "2026-07-07T12:00:00Z", Class: "session",
		Payload: json.RawMessage(`{"studio":"` + studioKey + `","name":"Vinyasa Flow","instructor":"` + instructorA +
			`","startsAt":"2026-07-08T09:00:00Z","endsAt":"2026-07-08T09:30:00Z","capacity":20}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{studioKey, instructorA},
			OptionalReads: wdSlotClaimKeys(t, studioKey, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z"),
		},
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	sessionKey := "vtx.session." + nanoIDFromRequestID(sessReqID)

	outcome, _ := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, reassignSessionEnv(t, "wdreassignclr00001",
		sessionKey, studioKey, domainActorKey, map[string]any{"sessionKey": sessionKey, "studio": studioKey, "clearInstructor": true},
		"2026-07-08T08:00:00Z"))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("ReassignSession clearInstructor outcome = %v, want Accepted", outcome)
	}
	if keyExists(t, ctx, conn, sessionLedByLnkKey(t, sessionKey, instructorA)) {
		t.Errorf("the ledBy link must be tombstoned after clearInstructor, with no replacement written")
	}
}

// TestReassignSession_MovesTimeLeavingSharedCellClaimed is the core
// regression the symmetric-difference cell logic exists for: moving
// [09:00,09:30) to [09:15,09:45) shares cell 09:15. A naive
// release-then-claim would tombstone 09:15 and then try to re-claim it in
// the SAME script execution — but a script's own reads see live state as it
// stood when the op was submitted, not its own not-yet-applied mutations, so
// claim_cell would read the cell as still live (about to be released by a
// mutation it cannot see yet) and reject with a spurious StudioConflict
// against itself. Excluding the shared cell from both the release and the
// claim set is what keeps this Accepted. It also proves an EXISTING booking
// on the class survives the move untouched.
func TestReassignSession_MovesTimeLeavingSharedCellClaimed(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassignmove")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdreassignmvstudio01", "Flow Room")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdreassignmvsessio01",
		studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession outcome = %v, want Accepted", outcome)
	}
	booker := seedIdentity(t, ctx, conn, "BBWELLREASGNBKRHJKMN")
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdreassignmvbookin01", sessionKey, booker, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", outcome)
	}

	env := reassignSessionEnv(t, "wdreassignmvmove0001", sessionKey, studioKey, domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "startsAt": "2026-07-08T09:15:00Z", "endsAt": "2026-07-08T09:45:00Z"},
		"2026-07-08T08:00:00Z")
	outcome2, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome2 != processor.OutcomeAccepted {
		msg := ""
		if reply != nil && reply.Error != nil {
			msg = reply.Error.Message
		}
		t.Fatalf("ReassignSession time move outcome = %v (%s), want Accepted", outcome2, msg)
	}

	if keyExists(t, ctx, conn, studioKey+".slot20260708t090000z") {
		t.Errorf("cell 09:00 (only the OLD span) must be released")
	}
	if !keyExists(t, ctx, conn, studioKey+".slot20260708t091500z") {
		t.Errorf("cell 09:15 (BOTH spans) must remain claimed")
	}
	if !keyExists(t, ctx, conn, studioKey+".slot20260708t093000z") {
		t.Errorf("cell 09:30 (only the NEW span) must be claimed")
	}

	schedDoc := readDoc(t, ctx, conn, sessionKey+".schedule")
	schedData, _ := schedDoc["data"].(map[string]any)
	if got, _ := schedData["startsAt"].(string); got != "2026-07-08T09:15:00Z" {
		t.Errorf("schedule.startsAt = %q, want the moved start", got)
	}
	if got, _ := schedData["endsAt"].(string); got != "2026-07-08T09:45:00Z" {
		t.Errorf("schedule.endsAt = %q, want the moved end", got)
	}
	if got, _ := schedData["name"].(string); got != "Vinyasa Flow" {
		t.Errorf("schedule.name = %q, must carry forward unchanged", got)
	}
	if got, _ := schedData["capacity"].(float64); got != 20 {
		t.Errorf("schedule.capacity = %v, must carry forward unchanged", got)
	}

	if !keyExists(t, ctx, conn, bookingKey) {
		t.Errorf("the existing booking must survive the time move untouched")
	}
}

// TestReassignSession_CarriesPriceCentsForwardOnTimeMove proves a time move
// leaves .schedule.priceCents untouched, exactly like name/capacity.
func TestReassignSession_CarriesPriceCentsForwardOnTimeMove(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassignpricemove")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdreassignprcmvstd01", "Flow Room")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdreassignprcmvsess1",
		studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession outcome = %v, want Accepted", outcome)
	}

	env := reassignSessionEnv(t, "wdreassignprcmvmove1", sessionKey, studioKey, domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "startsAt": "2026-07-08T09:15:00Z", "endsAt": "2026-07-08T09:45:00Z"},
		"2026-07-08T08:00:00Z")
	outcome2, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome2 != processor.OutcomeAccepted {
		msg := ""
		if reply != nil && reply.Error != nil {
			msg = reply.Error.Message
		}
		t.Fatalf("ReassignSession time move outcome = %v (%s), want Accepted", outcome2, msg)
	}

	schedDoc := readDoc(t, ctx, conn, sessionKey+".schedule")
	schedData, _ := schedDoc["data"].(map[string]any)
	if got, _ := schedData["priceCents"].(float64); got != 1500 {
		t.Errorf("schedule.priceCents = %v, must carry forward unchanged on a time move", got)
	}
}

// TestReassignSession_EditsNameCapacityAndPrice is the fix this backlog item
// asks for: a full or mispriced class no longer needs TombstoneSession +
// recreate (which would strand every booking) just to add seats, fix a
// typo'd name, or reprice.
func TestReassignSession_EditsNameCapacityAndPrice(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassignedit")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdreassigneditstd01", "Flow Room")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdreassigneditsess1",
		studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession outcome = %v, want Accepted", outcome)
	}

	env := reassignSessionEnv(t, "wdreassigneditedit01", sessionKey, studioKey, domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "name": "Power Vinyasa", "capacity": 30, "priceCents": 1800},
		"2026-07-08T08:00:00Z")
	outcome2, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome2 != processor.OutcomeAccepted {
		msg := ""
		if reply != nil && reply.Error != nil {
			msg = reply.Error.Message
		}
		t.Fatalf("ReassignSession edit outcome = %v (%s), want Accepted", outcome2, msg)
	}

	schedDoc := readDoc(t, ctx, conn, sessionKey+".schedule")
	schedData, _ := schedDoc["data"].(map[string]any)
	if got, _ := schedData["name"].(string); got != "Power Vinyasa" {
		t.Errorf("schedule.name = %q, want %q", got, "Power Vinyasa")
	}
	if got, _ := schedData["capacity"].(float64); got != 30 {
		t.Errorf("schedule.capacity = %v, want 30", got)
	}
	if got, _ := schedData["priceCents"].(float64); got != 1800 {
		t.Errorf("schedule.priceCents = %v, want 1800", got)
	}
	// startsAt/endsAt are untouched by a pure edit call.
	if got, _ := schedData["startsAt"].(string); got != "2026-07-08T09:00:00Z" {
		t.Errorf("schedule.startsAt = %q, must be untouched by a name/capacity/price-only edit", got)
	}
}

func TestReassignSession_RejectsCapacityAboveMax(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassigncapmax")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdreassigncapstd01", "Flow Room")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdreassigncapsess01",
		studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession outcome = %v, want Accepted", outcome)
	}

	env := reassignSessionEnv(t, "wdreassigncapedit01", sessionKey, studioKey, domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "capacity": 500},
		"2026-07-08T08:00:00Z")
	outcome2, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome2 != processor.OutcomeRejected {
		t.Fatalf("ReassignSession capacity=500 outcome = %v, want Rejected (InvalidArgument)", outcome2)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") {
		t.Errorf("rejection should be InvalidArgument, got %+v", reply.Error)
	}
}

func TestReassignSession_RejectsCollisionWithAnotherSession(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassigncollide")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdreassigncolstudio1", "Flow Room")
	sessionA, outcome := createSession(t, ctx, conn, cp, cons, "wdreassigncolsessa01",
		studioKey, "Morning Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession A outcome = %v, want Accepted", outcome)
	}
	_, outcome = createSession(t, ctx, conn, cp, cons, "wdreassigncolsessb01",
		studioKey, "Power Sculpt", "2026-07-08T10:00:00Z", "2026-07-08T10:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession B outcome = %v, want Accepted", outcome)
	}

	env := reassignSessionEnv(t, "wdreassigncolmove001", sessionA, studioKey, domainActorKey,
		map[string]any{"sessionKey": sessionA, "studio": studioKey, "startsAt": "2026-07-08T10:00:00Z", "endsAt": "2026-07-08T10:30:00Z"},
		"2026-07-08T08:00:00Z")
	outcome2, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome2 != processor.OutcomeRejected {
		t.Fatalf("ReassignSession moving session A onto session B's slot = %v, want Rejected (StudioConflict)", outcome2)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "StudioConflict") {
		t.Errorf("rejection should be StudioConflict, got %+v", reply.Error)
	}
	// The rejected move must not have partially released session A's own cells.
	if !keyExists(t, ctx, conn, studioKey+".slot20260708t090000z") {
		t.Errorf("session A's own cells must survive a rejected move")
	}
}

func TestReassignSession_InstructorConfinedToTheirOwnClass(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "reassignconfine")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	const instructorActorID = "BBWELLREASGNSTRACTRK"
	instructorActorKey := "vtx.identity." + instructorActorID
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + instructorActorID,
		Actor:                  instructorActorKey,
		Version:                "1.0",
		ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{instructorActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ReassignSession", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "provider")},
	})

	mkInstructor := func(label, name string) string {
		reqID := testutil.GenReqID(label)
		testutil.PublishOp(t, conn, &processor.OperationEnvelope{
			RequestID: reqID, Lane: processor.LaneDefault, OperationType: "CreateInstructor",
			Actor: domainActorKey, SubmittedAt: "2026-07-07T12:00:00Z", Class: "instructor",
			Payload: json.RawMessage(`{"displayName":"` + name + `"}`),
		})
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
		return "vtx.instructor." + nanoIDFromRequestID(reqID)
	}
	mineKey := mkInstructor("wdreassignconfinstr1", "Mine")
	theirsKey := mkInstructor("wdreassignconfinstr2", "Theirs")

	_, mineID, _ := substrate.ParseVertexKey(mineKey)
	seedLink(t, ctx, conn, "lnk.instructor."+mineID+".identifiedBy.identity."+instructorActorID,
		mineKey, instructorActorKey, "identifiedBy", "identifiedBy")

	ledSession := func(label, studioLabel, instructorKey, startsAt, endsAt string) string {
		studioKey := createStudio(t, ctx, conn, cp, cons, studioLabel, studioLabel)
		reqID := testutil.GenReqID(label)
		testutil.PublishOp(t, conn, &processor.OperationEnvelope{
			RequestID: reqID, Lane: processor.LaneDefault, OperationType: "CreateSession",
			Actor: domainActorKey, SubmittedAt: "2026-07-07T12:00:00Z", Class: "session",
			Payload: json.RawMessage(`{"studio":"` + studioKey + `","name":"Class","instructor":"` + instructorKey +
				`","startsAt":"` + startsAt + `","endsAt":"` + endsAt + `","capacity":4}`),
			ContextHint: &processor.ContextHint{
				Reads:         []string{studioKey, instructorKey},
				OptionalReads: wdSlotClaimKeys(t, studioKey, startsAt, endsAt),
			},
		})
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
		return "vtx.session." + nanoIDFromRequestID(reqID)
	}
	mySession := ledSession("wdreassignconfsess01", "wdreassignconfstd01", mineKey, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z")
	theirSession := ledSession("wdreassignconfsess02", "wdreassignconfstd02", theirsKey, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z")

	// The instructor reschedules the class they lead.
	myStudioKey := "vtx.studio." + nanoIDFromRequestID(testutil.GenReqID("wdreassignconfstd01"))
	theirStudioKey := "vtx.studio." + nanoIDFromRequestID(testutil.GenReqID("wdreassignconfstd02"))
	outcome, _ := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, reassignSessionEnv(t, "wdreassignconfmine01",
		mySession, myStudioKey, instructorActorKey,
		map[string]any{"sessionKey": mySession, "studio": myStudioKey, "instructor": mineKey, "startsAt": "2026-07-08T10:00:00Z", "endsAt": "2026-07-08T10:30:00Z"},
		"2026-07-08T08:05:00Z"))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("a bound instructor must be able to reschedule their own class, got %v", outcome)
	}

	// The same instructor, another instructor's class.
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, reassignSessionEnv(t, "wdreassignconfthr01",
		theirSession, theirStudioKey, instructorActorKey,
		map[string]any{"sessionKey": theirSession, "studio": theirStudioKey, "instructor": mineKey, "startsAt": "2026-07-08T10:00:00Z", "endsAt": "2026-07-08T10:30:00Z"},
		"2026-07-08T08:05:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("reassigning another instructor's class = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "does not lead session") {
		t.Fatalf("rejection should be the ledBy denial, got %+v", reply.Error)
	}
}

// TestReleaseOrphanedBooking_ReleasesSeatAndGuardAfterSessionTombstoned proves
// the convergence-target op TombstoneSession's deliberate no-cascade
// (package.go) leaves for Weaver: once a session is tombstoned out from under
// a still-`booked` booking, ReleaseOrphanedBooking{bookingKey} — no session
// param, unlike CancelBooking — reads the booking's own .status.session
// anchor, releases the seat cell and the per-(session, booker) double-book
// guard, and soft-deletes the booking, the same footprint CancelBooking
// leaves. The guard release is proven positively: the SAME booker can book a
// brand-new session afterward, which would DoubleBook-reject if the guard
// (keyed only on booker id, not session) had leaked across sessions — it
// isn't, so this also confirms the guard was scoped to the dead session's own
// key, not the booker's alone.
func TestReleaseOrphanedBooking_ReleasesSeatAndGuardAfterSessionTombstoned(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "orphanrelease")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdorphanstudio000001", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdorphansessio000001", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)
	bookerKey := seedIdentity(t, ctx, conn, "BBWELLBKERURP1HJKMNP")
	bookingKey, bookingOutcome := createBooking(t, ctx, conn, cp, cons, "wdorphanbookin000001", sessionKey, bookerKey, "")
	if bookingOutcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", bookingOutcome)
	}

	tombstoneReqID := testutil.GenReqID("wdorphantombst000001")
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

	// The booking is left live and still `booked` — exactly the leak this op
	// exists to drain.
	if !keyExists(t, ctx, conn, bookingKey) {
		t.Fatalf("booking must still be live immediately after TombstoneSession (no cascade)")
	}

	releaseReqID := testutil.GenReqID("wdorphanreleas000001")
	releaseEnv := &processor.OperationEnvelope{
		RequestID:     releaseReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReleaseOrphanedBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:15:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			bookingKey, bookingKey + ".status", sessionKey,
		}},
	}
	testutil.PublishOp(t, conn, releaseEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, bookingKey) {
		t.Fatalf("booking must be tombstoned after ReleaseOrphanedBooking")
	}
	if keyExists(t, ctx, conn, sessionKey+".seat1") {
		t.Fatalf("seat1 must be released after ReleaseOrphanedBooking")
	}

	// Positive proof the double-book guard released too: the same booker,
	// booking a DIFFERENT session, is accepted (DoubleBooked would reject
	// only if the guard leaked past the dead session).
	otherSessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdorphansessio000002", studioKey, "Power Sculpt", "2026-07-09T09:00:00Z", "2026-07-09T09:30:00Z", 1)
	_, outcome := createBooking(t, ctx, conn, cp, cons, "wdorphanbookin000002", otherSessionKey, bookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("same booker on a different session after release outcome = %v, want Accepted", outcome)
	}
}

// TestReleaseOrphanedBooking_RejectsWhenSessionStillLive proves the
// defense-in-depth re-check: ReleaseOrphanedBooking trusts nothing about why
// it was dispatched and independently confirms the booking's session is
// genuinely dead before releasing anything. A live session must go through
// CancelBooking instead.
func TestReleaseOrphanedBooking_RejectsWhenSessionStillLive(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "orphanstilllive")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdorphanstudio000002", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdorphansessio000003", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)
	bookerKey := seedIdentity(t, ctx, conn, "BBWELLBKERURP2HJKMNP")
	bookingKey, _ := createBooking(t, ctx, conn, cp, cons, "wdorphanbookin000003", sessionKey, bookerKey, "")

	releaseReqID := testutil.GenReqID("wdorphanreleas000002")
	releaseEnv := &processor.OperationEnvelope{
		RequestID:     releaseReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReleaseOrphanedBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:15:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			bookingKey, bookingKey + ".status", sessionKey,
		}},
	}
	testutil.PublishOp(t, conn, releaseEnv)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("ReleaseOrphanedBooking against a live session outcome = %v, want Rejected (SessionStillLive)", outcome)
	}
	if !keyExists(t, ctx, conn, bookingKey) {
		t.Fatalf("booking must remain live — the release must not have applied")
	}
	if !keyExists(t, ctx, conn, sessionKey+".seat1") {
		t.Fatalf("seat1 must remain claimed — the release must not have applied")
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
		ContextHint:   &processor.ContextHint{Reads: []string{sessionKey, sessionKey + ".schedule", domainConsumerKey}, OptionalReads: append(wdSeatKeys(sessionKey, 20), sessionKey+".bkr"+domainConsumerID)},
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
	_, otherBookerID, _ := substrate.ParseVertexKey(otherBookerKey)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateBooking",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "booking",
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Reads: []string{sessionKey, sessionKey + ".schedule", otherBookerKey}, OptionalReads: append(wdSeatKeys(sessionKey, 20), sessionKey+".bkr"+otherBookerID)},
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
			Reads:         []string{bookingKey, bookingKey + ".status", sessionKey + ".schedule", forSessionLnkKey(t, bookingKey, sessionKey)},
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

// TestTombstoneSession_StudioProbeRevealsNothingToANonInstructor: the studio
// check answers differently for this session's own studio than for any other,
// so ahead of the instructor binder it tells anyone holding a TombstoneSession
// grant where a class they have no part in is held. Behind it, a caller who
// cannot name the instructor they are bound to learns nothing either way.
func TestTombstoneSession_StudioProbeRevealsNothingToANonInstructor(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "tombprobe")

	// The operator mints the instructors this vector needs; the shared operator
	// cap doc carries the studio/session ops but not instructor provisioning.
	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	// A non-operator holding the standing TombstoneSession grant a bound
	// instructor holds — the capability plane cannot tell them apart, so the
	// script's binder is the whole boundary.
	const proberID = "BBWELLPRBNQNSTRCTHJK"
	proberKey := "vtx.identity." + proberID
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + proberID,
		Actor:                  proberKey,
		Version:                "1.0",
		ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{proberKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "TombstoneSession", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "provider")},
	})

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdtprstudio000001", "Held Here")
	decoyStudio := createStudio(t, ctx, conn, cp, cons, "wdtprstudio000002", "Not Here")

	// The session is led by a real instructor, and a second instructor exists —
	// the roster a prober would walk. The prober is bound to neither.
	mkInstructor := func(label, name string) string {
		reqID := testutil.GenReqID(label)
		env := &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "CreateInstructor",
			Actor:         domainActorKey,
			SubmittedAt:   "2026-07-07T12:00:00Z",
			Class:         "instructor",
			Payload:       json.RawMessage(`{"displayName":"` + name + `"}`),
		}
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
		if outcome != processor.OutcomeAccepted {
			t.Fatalf("CreateInstructor %s = %v, want Accepted: %+v", name, outcome, reply.Error)
		}
		return "vtx.instructor." + nanoIDFromRequestID(reqID)
	}
	leaderKey := mkInstructor("wdtprinstr000001", "Session Leader")
	rosterPeerKey := mkInstructor("wdtprinstr000002", "Roster Peer")

	sessReqID := testutil.GenReqID("wdtprsession00001")
	sessionKey := "vtx.session." + nanoIDFromRequestID(sessReqID)
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     sessReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateSession",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "session",
		Payload: json.RawMessage(`{"studio":"` + studioKey + `","name":"Private Class","instructor":"` + leaderKey +
			`","startsAt":"2026-07-09T09:00:00Z","endsAt":"2026-07-09T09:30:00Z","capacity":4}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{studioKey, leaderKey},
			OptionalReads: wdSlotClaimKeys(t, studioKey, "2026-07-09T09:00:00Z", "2026-07-09T09:30:00Z"),
		},
	})
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("CreateSession with instructor = %v, want Accepted", got)
	}
	sessionID := sessionKey[len("vtx.session."):]

	probe := func(label, guessStudio string) string {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "TombstoneSession",
			Actor:         proberKey,
			SubmittedAt:   "2026-07-07T12:00:00Z",
			Class:         "session",
			Payload:       json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + guessStudio + `"}`),
			ContextHint: &processor.ContextHint{
				Reads: []string{sessionKey, sessionKey + ".schedule"},
				OptionalReads: []string{
					"lnk.session." + sessionID + ".atStudio.studio." + guessStudio[len("vtx.studio."):],
				},
			},
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

	real, decoy := probe("wdtprreal0000001", studioKey), probe("wdtprdecoy000001", decoyStudio)
	if real != decoy {
		t.Errorf("the studio probe locates a class for a caller with no part in it:\n  its studio → %s\n  decoy      → %s", real, decoy)
	}
	if !strings.Contains(real, "no instructor supplied") {
		t.Errorf("probes should be answered by the binder, got %q", real)
	}

	// The roster probe: supplying an instructor candidate, naming the session's
	// actual leader must be indistinguishable from naming any other instructor —
	// otherwise one bound instructor reads off who leads a stranger's class by
	// walking the studio's published roster.
	instructorProbe := func(label, candidate string) string {
		candidateID := candidate[len("vtx.instructor."):]
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "TombstoneSession",
			Actor:         proberKey,
			SubmittedAt:   "2026-07-07T12:00:00Z",
			Class:         "session",
			Payload:       json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + studioKey + `","instructor":"` + candidate + `"}`),
			ContextHint: &processor.ContextHint{
				Reads: []string{sessionKey, sessionKey + ".schedule"},
				OptionalReads: []string{
					"lnk.session." + sessionID + ".ledBy.instructor." + candidateID,
					"lnk.instructor." + candidateID + ".identifiedBy.identity." + proberID,
					"lnk.session." + sessionID + ".atStudio.studio." + studioKey[len("vtx.studio."):],
				},
			},
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

	// Compared with the candidate key redacted: a denial that echoes back the
	// key the caller just supplied tells them nothing they did not already
	// know, so what must match is the rest — WHICH check answered.
	leader := strings.ReplaceAll(instructorProbe("wdtprleader00001", leaderKey), leaderKey, "<candidate>")
	peer := strings.ReplaceAll(instructorProbe("wdtprpeer0000001", rosterPeerKey), rosterPeerKey, "<candidate>")
	if leader != peer {
		t.Errorf("the ledBy probe names who leads a stranger's class:\n  its leader  → %s\n  roster peer → %s", leader, peer)
	}
	if !strings.Contains(leader, "not identifiedBy-bound") {
		t.Errorf("probes should be answered by the caller's own binding, got %q", leader)
	}
}

// TestCancelBooking_ConsumerSelfScope_SessionProbeRevealsNothing: the session
// check answers differently for this booking's own session than for any other,
// and a studio's class schedule is public — so ahead of the bookedBy binding it
// would tell a self-scoped consumer which class a stranger is attending, by
// walking the schedule until the error changes. Behind the binding, every guess
// on someone else's booking answers the same way.
func TestCancelBooking_ConsumerSelfScope_SessionProbeRevealsNothing(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "cancelselfprobe")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdprbstudio000001", "Probe Studio")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdprbsession00001", studioKey, "Attended Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)
	decoyKey, _ := createSession(t, ctx, conn, cp, cons, "wdprbsession00002", studioKey, "Decoy Class", "2026-07-08T11:00:00Z", "2026-07-08T11:30:00Z", 1)
	seedIdentity(t, ctx, conn, domainConsumerID)
	strangerKey := seedIdentity(t, ctx, conn, "BBWELLPRBSTRNGR1HJKM")
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdprbbooking00001", sessionKey, strangerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("setup CreateBooking outcome = %v, want Accepted", outcome)
	}
	bookingID := bookingKey[len("vtx.booking."):]

	probe := func(label, guessKey string) string {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "CancelBooking",
			Actor:         domainConsumerKey,
			SubmittedAt:   "2026-07-07T12:10:00Z",
			Class:         "booking",
			Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","session":"` + guessKey + `"}`),
			// The forSession link is declared optional, the shape a prober's own
			// envelope takes: declared in Reads a miss faults at hydration before
			// the script runs, so the submitter simply declares it optional and
			// the in-script order becomes the only guard.
			ContextHint: &processor.ContextHint{
				Reads: []string{bookingKey, bookingKey + ".status", guessKey + ".schedule"},
				OptionalReads: []string{
					"lnk.booking." + bookingID + ".forSession.session." + guessKey[len("vtx.session."):],
					"lnk.booking." + bookingID + ".bookedBy.identity." + domainConsumerID,
				},
			},
			AuthContext: &processor.AuthContext{Target: domainConsumerKey},
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

	real, decoy := probe("wdprbreal0000001", sessionKey), probe("wdprbdecoy000001", decoyKey)
	if real != decoy {
		t.Errorf("the session probe distinguishes the booked class from another:\n  booked class → %s\n  decoy class  → %s\n"+
			"walking a public schedule then names which class a stranger attends", real, decoy)
	}
	// Equality alone would also hold if both probes died at a common earlier
	// error, so pin the denial they must agree on.
	if !strings.Contains(real, "AuthDenied") {
		t.Errorf("probes should be answered by the bookedBy binding, got %q", real)
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
			Reads:         []string{bookingKey, bookingKey + ".status", sessionKey + ".schedule", forSessionLnkKey(t, bookingKey, sessionKey)},
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

// profileEnv builds a SetInstructorProfile envelope. The identifiedBy probe is
// an optionalRead (the standing guard renders its absence as AuthDenied), which
// is what the op-meta declares — the script never reads a key no dispatcher
// named.
func profileEnv(t *testing.T, label, instructorKey, displayName, actorKey string) *processor.OperationEnvelope {
	t.Helper()
	_, instrID, _ := substrate.ParseVertexKey(instructorKey)
	_, actorID, _ := substrate.ParseVertexKey(actorKey)
	payload, _ := json.Marshal(map[string]any{"instructorKey": instructorKey, "displayName": displayName})
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetInstructorProfile",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "instructor",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         []string{instructorKey},
			OptionalReads: []string{"lnk.instructor." + instrID + ".identifiedBy.identity." + actorID},
		},
	}
}

func instructorDisplayName(t *testing.T, ctx context.Context, conn *substrate.Conn, instructorKey string) string {
	t.Helper()
	doc := readDoc(t, ctx, conn, instructorKey+".profile")
	data, _ := doc["data"].(map[string]any)
	name, _ := data["displayName"].(string)
	return name
}

// TestSetInstructorProfile_ConfinedToTheCallersOwnRecord is the security proof
// for the instructor hat's record-administering op.
//
// The grant it runs under is `provider` at scope=any — and `provider` is the
// GENERIC archetype role that all three bind ops mint (wellness's
// BindInstructorIdentity, clinic's BindProviderIdentity, service-domain's
// BindServiceProviderIdentity). So the capability plane cannot tell a bound
// instructor from a bound clinician: every one of them arrives holding exactly
// the role seeded below. The in-script standing guard is the ONLY thing that
// confines the write, and that is what this test pins.
//
// The negative vector is therefore an actor holding the SAME role but bound
// elsewhere — not an unbound stranger, who would be refused by the capability
// plane before the script ever ran and would prove nothing about the guard.
func TestSetInstructorProfile_ConfinedToTheCallersOwnRecord(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "instrprofile")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	// Two logins, each holding the identical generic `provider` role and the
	// identical standing grant. Nothing in either capability doc says WHICH
	// instructor record it may touch — mine is bound to an instructor, theirs
	// stands in for a bound clinic provider / service provider, who holds the
	// same role from a different domain's bind ceremony.
	providerRole := "vtx.role." + pkgmgr.RoleID("identity-domain", "provider")
	seedProviderLogin := func(actorID string) string {
		actorKey := "vtx.identity." + actorID
		testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
			Key:                    "cap.identity." + actorID,
			Actor:                  actorKey,
			Version:                "1.0",
			ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
			ProjectedFromRevisions: map[string]uint64{actorKey: 1},
			Lanes:                  []string{"default"},
			PlatformPermissions: []processor.PlatformPermission{
				{OperationType: "SetInstructorProfile", Scope: "any"},
			},
			ServiceAccess:   []processor.ServiceAccessEntry{},
			EphemeralGrants: []processor.EphemeralGrant{},
			Roles:           []string{providerRole},
		})
		return actorKey
	}
	mineActorKey := seedProviderLogin("BBWELLPRFLMNEACTRKAB")
	otherArchetypeKey := seedProviderLogin("BBWELLPRFLTHRACTRKAB")

	mkInstructor := func(label, name string) string {
		reqID := testutil.GenReqID(label)
		testutil.PublishOp(t, conn, &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "CreateInstructor",
			Actor:         domainActorKey,
			SubmittedAt:   "2026-07-07T12:00:00Z",
			Class:         "instructor",
			Payload:       json.RawMessage(`{"displayName":"` + name + `"}`),
		})
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
		return "vtx.instructor." + nanoIDFromRequestID(reqID)
	}
	mineKey := mkInstructor("wdprofileinstr000001", "Mine")
	theirsKey := mkInstructor("wdprofileinstr000002", "Theirs")

	// The identifiedBy binding BindInstructorIdentity would mint. Seeded
	// directly so this test proves the profile guard, not the bind.
	_, mineID, _ := substrate.ParseVertexKey(mineKey)
	_, mineActorID, _ := substrate.ParseVertexKey(mineActorKey)
	seedLink(t, ctx, conn, "lnk.instructor."+mineID+".identifiedBy.identity."+mineActorID,
		mineKey, mineActorKey, "identifiedBy", "identifiedBy")

	// A bound instructor edits their own profile.
	testutil.PublishOp(t, conn, profileEnv(t, "wdprofilemine0000001", mineKey, "Kai Nakamura, RYT-500", mineActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := instructorDisplayName(t, ctx, conn, mineKey); got != "Kai Nakamura, RYT-500" {
		t.Fatalf("a bound instructor must be able to edit their own profile: displayName = %q", got)
	}

	// The same instructor, someone else's record.
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		profileEnv(t, "wdprofiletheirs00001", theirsKey, "Hijacked", mineActorKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("editing another instructor's profile = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "may not set the profile of instructor") {
		t.Fatalf("rejection should be the standing-guard denial, got %+v", reply.Error)
	}
	if got := instructorDisplayName(t, ctx, conn, theirsKey); got != "Theirs" {
		t.Fatalf("another instructor's profile was written: displayName = %q, want %q", got, "Theirs")
	}

	// The cross-archetype vector: the same generic `provider` role, bound to no
	// instructor at all. This is the shape a bound clinic provider arrives in,
	// and it must be refused by the binding, not by the role.
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		profileEnv(t, "wdprofilecross000001", mineKey, "Hijacked", otherArchetypeKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a provider-role holder bound elsewhere = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "may not set the profile of instructor") {
		t.Fatalf("rejection should be the standing-guard denial, got %+v", reply.Error)
	}
	if got := instructorDisplayName(t, ctx, conn, mineKey); got != "Kai Nakamura, RYT-500" {
		t.Fatalf("a cross-archetype caller overwrote the profile: displayName = %q", got)
	}
}

// TestSetInstructorProfile_RejectsAnEmptyDisplayName pins the field the
// member-facing lenses depend on. The .profile aspect is REPLACED wholesale, so
// a blank displayName would null the instructorName column wellnessSessions
// projects to members and wellnessInstructors projects to the scheduling form.
func TestSetInstructorProfile_RejectsAnEmptyDisplayName(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "instrprofblank")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"},
		processor.PlatformPermission{OperationType: "SetInstructorProfile", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	reqID := testutil.GenReqID("wdprofblankinst00001")
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateInstructor",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "instructor",
		Payload:       json.RawMessage(`{"displayName":"Kai"}`),
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	instructorKey := "vtx.instructor." + nanoIDFromRequestID(reqID)

	_, instrID, _ := substrate.ParseVertexKey(instructorKey)
	_, actorID, _ := substrate.ParseVertexKey(domainActorKey)
	seedLink(t, ctx, conn, "lnk.instructor."+instrID+".identifiedBy.identity."+actorID,
		instructorKey, domainActorKey, "identifiedBy", "identifiedBy")

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		profileEnv(t, "wdprofblank000000001", instructorKey, "   ", domainActorKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a blank displayName = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "displayName: required non-empty string") {
		t.Fatalf("rejection should name the empty displayName, got %+v", reply.Error)
	}
	if got := instructorDisplayName(t, ctx, conn, instructorKey); got != "Kai" {
		t.Fatalf("the profile was overwritten by a rejected edit: displayName = %q", got)
	}
}

// TestSetInstructorProfile_OperatorPassesUnbound proves the guard's FIRST
// binder accepts. Every other test exercises actor_holds_operator only in its
// False direction, so a helper broken to always return False would still let
// them all pass while silently locking operators out of the op.
func TestSetInstructorProfile_OperatorPassesUnbound(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "instrprofop")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	reqID := testutil.GenReqID("wdprofopinstr0000001")
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateInstructor",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "instructor",
		Payload:       json.RawMessage(`{"displayName":"Before"}`),
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	instructorKey := "vtx.instructor." + nanoIDFromRequestID(reqID)

	// An operator bound to NO instructor at all: the binding branch cannot carry
	// this call, so only the operator branch can.
	const actorID = "BBWELLPRFLPACTRHJKMN"
	actorKey := "vtx.identity." + actorID
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + actorID,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "SetInstructorProfile", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	})

	// actor_holds_operator resolves the role from the GRAPH, not the cap doc,
	// so the holdsRole link has to actually exist.
	roleID := bootstrap.RoleOperatorKey[len("vtx.role."):]
	seedLink(t, ctx, conn, "lnk.identity."+actorID+".holdsRole.role."+roleID,
		actorKey, bootstrap.RoleOperatorKey, "holdsRole", "holdsRole")

	testutil.PublishOp(t, conn, profileEnv(t, "wdprofoperator000001", instructorKey, "After", actorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := instructorDisplayName(t, ctx, conn, instructorKey); got != "After" {
		t.Fatalf("an operator bound to no instructor must pass: displayName = %q", got)
	}
}

// TestJoinWaitlist_ClaimsSlotWhenFull proves the mirror-image write:
// JoinWaitlist claims a sessionWaitlistClaim slot instead of a seat, stamping
// .status {value: waitlisted, waitlistSlot}, never a seat field.
func TestJoinWaitlist_ClaimsSlotWhenFull(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "waitlistjoin")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdwlstudio000001", "Small Studio")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdwlsession000001", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)

	bookerOneKey := seedIdentity(t, ctx, conn, "BBWELLBKERWL1AHJKMNP")
	_, firstOutcome := createBooking(t, ctx, conn, cp, cons, "wdwlbooking000001", sessionKey, bookerOneKey, "")
	if firstOutcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", firstOutcome)
	}

	bookerTwoKey := seedIdentity(t, ctx, conn, "BBWELLBKERWL2AHJKMNP")
	waitlistKey, outcome := joinWaitlist(t, ctx, conn, cp, cons, "wdwljoin000001", sessionKey, bookerTwoKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("JoinWaitlist outcome = %v, want Accepted", outcome)
	}

	statusDoc := readDoc(t, ctx, conn, waitlistKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["value"].(string); got != "waitlisted" {
		t.Fatalf("status.value = %q, want waitlisted", got)
	}
	if got, _ := statusData["waitlistSlot"].(float64); got != 1 {
		t.Fatalf("status.waitlistSlot = %v, want 1 (first claimed slot)", got)
	}
	if _, hasSeat := statusData["seat"]; hasSeat {
		t.Fatalf("a waitlisted booking must not carry a seat field: %v", statusData)
	}
	if !keyExists(t, ctx, conn, sessionKey+".wl1") {
		t.Fatalf("expected sessionWaitlistClaim at wl1")
	}
}

// TestCancelBooking_PromotesEarliestWaitlistedBooking is the load-bearing
// promotion test: cancelling a booked seat with two waitlisted candidates
// must promote the EARLIEST (lowest waitlistSlot) one, hand it the freed
// seat directly (seat cell stays claimed, never tombstoned), and leave the
// later candidate untouched — proving find_promotion_candidate's ordering,
// not just that promotion happens at all.
func TestCancelBooking_PromotesEarliestWaitlistedBooking(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "waitlistpromote")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdwlstudio000002", "Small Studio")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdwlsession000002", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)

	bookerOneKey := seedIdentity(t, ctx, conn, "BBWELLBKERWL1BHJKMNP")
	bookingOneKey, bkOutcome := createBooking(t, ctx, conn, cp, cons, "wdwlbooking000002", sessionKey, bookerOneKey, "")
	if bkOutcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", bkOutcome)
	}

	bookerTwoKey := seedIdentity(t, ctx, conn, "BBWELLBKERWL2BHJKMNP")
	waitlistTwoKey, wl2Outcome := joinWaitlist(t, ctx, conn, cp, cons, "wdwljoin000002", sessionKey, bookerTwoKey, "")
	if wl2Outcome != processor.OutcomeAccepted {
		t.Fatalf("first JoinWaitlist outcome = %v, want Accepted", wl2Outcome)
	}

	bookerThreeKey := seedIdentity(t, ctx, conn, "BBWELLBKERWL3BHJKMNP")
	waitlistThreeKey, wl3Outcome := joinWaitlist(t, ctx, conn, cp, cons, "wdwljoin000003", sessionKey, bookerThreeKey, "")
	if wl3Outcome != processor.OutcomeAccepted {
		t.Fatalf("second JoinWaitlist outcome = %v, want Accepted", wl3Outcome)
	}

	cancelReqID := testutil.GenReqID("wdwlcancel000001")
	cancelEnv := &processor.OperationEnvelope{
		RequestID:     cancelReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingOneKey + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			bookingOneKey, bookingOneKey + ".status", sessionKey + ".schedule",
			forSessionLnkKey(t, bookingOneKey, sessionKey),
		}},
	}
	testutil.PublishOp(t, conn, cancelEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, bookingOneKey) {
		t.Fatalf("cancelled booking must be tombstoned")
	}

	// bookerTwo (earliest, wl1) must be promoted: booked, holding the freed
	// seat1, wl1 released.
	twoStatus := readDoc(t, ctx, conn, waitlistTwoKey+".status")
	twoData, _ := twoStatus["data"].(map[string]any)
	if got, _ := twoData["value"].(string); got != "booked" {
		t.Fatalf("bookerTwo status.value = %q, want booked (promoted)", got)
	}
	if got, _ := twoData["seat"].(float64); got != 1 {
		t.Fatalf("bookerTwo status.seat = %v, want 1 (the freed seat)", got)
	}
	if keyExists(t, ctx, conn, sessionKey+".wl1") {
		t.Fatalf("wl1 must be released once bookerTwo is promoted")
	}
	if !keyExists(t, ctx, conn, sessionKey+".seat1") {
		t.Fatalf("seat1 must remain claimed — handed to the promoted booking, never released back open")
	}

	// bookerThree (wl2, later) must remain waitlisted and untouched.
	threeStatus := readDoc(t, ctx, conn, waitlistThreeKey+".status")
	threeData, _ := threeStatus["data"].(map[string]any)
	if got, _ := threeData["value"].(string); got != "waitlisted" {
		t.Fatalf("bookerThree status.value = %q, want waitlisted (not yet promoted)", got)
	}
	if !keyExists(t, ctx, conn, sessionKey+".wl2") {
		t.Fatalf("wl2 must remain claimed — bookerThree was not the promoted candidate")
	}
}

// TestCancelBooking_WaitlistedBookerLeavesWaitlist proves the other
// CancelBooking branch: a waitlisted booking cancelling itself just releases
// its own waitlist slot — it never held a seat, so no promotion runs and the
// live booked seat is left untouched.
func TestCancelBooking_WaitlistedBookerLeavesWaitlist(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "waitlistleave")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdwlstudio000003", "Small Studio")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdwlsession000003", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)

	bookerOneKey := seedIdentity(t, ctx, conn, "BBWELLBKERWL1CHJKMNP")
	_, bkOutcome := createBooking(t, ctx, conn, cp, cons, "wdwlbooking000003", sessionKey, bookerOneKey, "")
	if bkOutcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", bkOutcome)
	}

	bookerTwoKey := seedIdentity(t, ctx, conn, "BBWELLBKERWL2CHJKMNP")
	waitlistKey, wlOutcome := joinWaitlist(t, ctx, conn, cp, cons, "wdwljoin000004", sessionKey, bookerTwoKey, "")
	if wlOutcome != processor.OutcomeAccepted {
		t.Fatalf("JoinWaitlist outcome = %v, want Accepted", wlOutcome)
	}

	cancelReqID := testutil.GenReqID("wdwlcancel000002")
	cancelEnv := &processor.OperationEnvelope{
		RequestID:     cancelReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + waitlistKey + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			waitlistKey, waitlistKey + ".status", sessionKey + ".schedule",
			forSessionLnkKey(t, waitlistKey, sessionKey),
		}},
	}
	testutil.PublishOp(t, conn, cancelEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, waitlistKey) {
		t.Fatalf("waitlisted booking must be tombstoned after cancelling")
	}
	if keyExists(t, ctx, conn, sessionKey+".wl1") {
		t.Fatalf("wl1 must be released once the waitlisted booker cancels")
	}
	if !keyExists(t, ctx, conn, sessionKey+".seat1") {
		t.Fatalf("seat1 (bookerOne's live seat) must be untouched — a waitlisted cancel promotes nobody")
	}
}

// TestReleaseOrphanedBooking_ReleasesWaitlistSlotAfterSessionTombstoned
// proves the missing_release gap's waitlisted branch (lenses.go): a
// waitlisted booking left behind by a called-off class releases its .wl<n>
// slot, not a seat cell — the ReleaseOrphanedBooking else-branch (ddls.go).
func TestReleaseOrphanedBooking_ReleasesWaitlistSlotAfterSessionTombstoned(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "waitlistorphan")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdwlstudio000004", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdwlsession000004", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)

	bookerOneKey := seedIdentity(t, ctx, conn, "BBWELLBKERWL1DHJKMNP")
	_, bkOutcome := createBooking(t, ctx, conn, cp, cons, "wdwlbooking000004", sessionKey, bookerOneKey, "")
	if bkOutcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", bkOutcome)
	}

	bookerTwoKey := seedIdentity(t, ctx, conn, "BBWELLBKERWL2DHJKMNP")
	waitlistKey, wlOutcome := joinWaitlist(t, ctx, conn, cp, cons, "wdwljoin000005", sessionKey, bookerTwoKey, "")
	if wlOutcome != processor.OutcomeAccepted {
		t.Fatalf("JoinWaitlist outcome = %v, want Accepted", wlOutcome)
	}

	tombstoneReqID := testutil.GenReqID("wdwltombstone00001")
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

	releaseReqID := testutil.GenReqID("wdwlrelease000001")
	releaseEnv := &processor.OperationEnvelope{
		RequestID:     releaseReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReleaseOrphanedBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:15:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + waitlistKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			waitlistKey, waitlistKey + ".status", sessionKey,
		}},
	}
	testutil.PublishOp(t, conn, releaseEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, waitlistKey) {
		t.Fatalf("waitlisted booking must be tombstoned after ReleaseOrphanedBooking")
	}
	if keyExists(t, ctx, conn, sessionKey+".wl1") {
		t.Fatalf("wl1 must be released after ReleaseOrphanedBooking")
	}

	// Positive proof the double-book guard released too: the same booker,
	// booking a DIFFERENT session, is accepted.
	otherSessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdwlsession000005", studioKey, "Power Sculpt", "2026-07-09T09:00:00Z", "2026-07-09T09:30:00Z", 1)
	_, outcome := createBooking(t, ctx, conn, cp, cons, "wdwlbooking000005", otherSessionKey, bookerTwoKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("same booker on a different session after release outcome = %v, want Accepted", outcome)
	}
}

// mkWdInstructor mints a bare CreateInstructor vertex, granting the caller's
// capability doc CreateInstructor first — the shape TestReassignSession_
// SwapsInstructor and TestSetBookingAttendance_InstructorConfinedToTheirOwnClass
// each already duplicate locally; factored here for the instructorSlotClaim
// tests below.
func mkWdInstructor(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, name string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID: reqID, Lane: processor.LaneDefault, OperationType: "CreateInstructor",
		Actor: domainActorKey, SubmittedAt: "2026-07-07T12:00:00Z", Class: "instructor",
		Payload: json.RawMessage(`{"displayName":"` + name + `"}`),
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.instructor." + nanoIDFromRequestID(reqID)
}

// createSessionWithInstructor mirrors createSession but names an instructor —
// the raw-envelope shape TestReassignSession_SwapsInstructor already uses
// inline, factored here for reuse.
func createSessionWithInstructor(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, studioKey, instructorKey, name, startsAt, endsAt string, capacity int) (string, processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload, _ := json.Marshal(map[string]any{
		"studio": studioKey, "instructor": instructorKey, "name": name,
		"startsAt": startsAt, "endsAt": endsAt, "capacity": capacity,
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
			Reads: []string{studioKey, instructorKey},
			OptionalReads: append(
				wdSlotClaimKeys(t, studioKey, startsAt, endsAt),
				wdSlotClaimKeys(t, instructorKey, startsAt, endsAt)...,
			),
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	return "vtx.session." + nanoIDFromRequestID(reqID), outcome, reply
}

// TestCreateSession_RejectsInstructorDoubleBook proves instructorSlotClaim
// (the providerSlotClaim mirror, verticals.md "One instructor can teach two
// classes at once") catches the exact gap studioSlotClaim alone cannot: the
// SAME instructor named at TWO DIFFERENT studios for an overlapping span.
func TestCreateSession_RejectsInstructorDoubleBook(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "instrdoublebook")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	instructorKey := mkWdInstructor(t, ctx, conn, cp, cons, "wdinstrdblinstr0001", "Solo")
	studioA := createStudio(t, ctx, conn, cp, cons, "wdinstrdblstudioa001", "Room A")
	studioB := createStudio(t, ctx, conn, cp, cons, "wdinstrdblstudiob001", "Room B")

	_, first, _ := createSessionWithInstructor(t, ctx, conn, cp, cons, "wdinstrdblsessa0001",
		studioA, instructorKey, "Morning Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if first != processor.OutcomeAccepted {
		t.Fatalf("first CreateSession outcome = %v, want Accepted", first)
	}

	_, second, reply := createSessionWithInstructor(t, ctx, conn, cp, cons, "wdinstrdblsessb0001",
		studioB, instructorKey, "Power Sculpt", "2026-07-08T09:15:00Z", "2026-07-08T09:45:00Z", 20)
	if second != processor.OutcomeRejected {
		t.Fatalf("same instructor at a DIFFERENT studio, overlapping span, outcome = %v, want Rejected (InstructorConflict)", second)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InstructorConflict") {
		t.Errorf("rejection should be InstructorConflict, got %+v", reply.Error)
	}
}

// TestTombstoneSession_ReleasesInstructorSlotCells proves TombstoneSession
// releases the CURRENT instructor's slot cells too, not just the studio's —
// read fresh via session_ledby_link (ddls.go), independent of whichever
// standing path authorized the cancellation.
func TestTombstoneSession_ReleasesInstructorSlotCells(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "instrtombstone")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	instructorKey := mkWdInstructor(t, ctx, conn, cp, cons, "wdinstrtmbinstr0001", "Solo")
	_, instructorID, _ := substrate.ParseVertexKey(instructorKey)
	studioA := createStudio(t, ctx, conn, cp, cons, "wdinstrtmbstudioa001", "Room A")

	sessionKey, outcome, _ := createSessionWithInstructor(t, ctx, conn, cp, cons, "wdinstrtmbsessa0001",
		studioA, instructorKey, "Morning Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession outcome = %v, want Accepted", outcome)
	}
	if !keyExists(t, ctx, conn, instructorKey+".slot20260708t090000z") {
		t.Fatalf("expected instructorSlotClaim at 09:00")
	}

	tombstoneEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wdinstrtmbcancel001"),
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneSession",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "session",
		Payload:       json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + studioA + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			sessionKey, sessionKey + ".schedule",
			atStudioLnkKey(t, sessionKey, studioA),
			"lnk.session." + sessionKey[len("vtx.session."):] + ".ledBy.instructor." + instructorID,
		}},
	}
	testutil.PublishOp(t, conn, tombstoneEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, instructorKey+".slot20260708t090000z") {
		t.Fatalf("instructorSlotClaim at 09:00 must be released after TombstoneSession")
	}

	// The freed instructor cells are re-bookable at a DIFFERENT studio, the
	// same overlapping span.
	studioB := createStudio(t, ctx, conn, cp, cons, "wdinstrtmbstudiob001", "Room B")
	_, outcome2, reply := createSessionWithInstructor(t, ctx, conn, cp, cons, "wdinstrtmbsessb0001",
		studioB, instructorKey, "Power Sculpt", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome2 != processor.OutcomeAccepted {
		msg := ""
		if reply != nil && reply.Error != nil {
			msg = reply.Error.Message
		}
		t.Fatalf("CreateSession on freed instructor cells outcome = %v (%s), want Accepted", outcome2, msg)
	}
}

// TestReassignSession_MigratesInstructorSlotClaimOnSwap proves an instructor
// swap RELEASES the old instructor's cells (they become bookable elsewhere at
// the same span) and CLAIMS the new instructor's (a second overlapping class
// naming them now collides) — the hub-can-change generalization of the
// studio cell handling ReassignSession already does for a time move.
func TestReassignSession_MigratesInstructorSlotClaimOnSwap(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "instrreassignswap")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)

	instructorA := mkWdInstructor(t, ctx, conn, cp, cons, "wdinstrswpinstra001", "A")
	instructorB := mkWdInstructor(t, ctx, conn, cp, cons, "wdinstrswpinstrb001", "B")
	studioA := createStudio(t, ctx, conn, cp, cons, "wdinstrswpstudioa001", "Room A")

	sessionKey, outcome, _ := createSessionWithInstructor(t, ctx, conn, cp, cons, "wdinstrswpsessa0001",
		studioA, instructorA, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession outcome = %v, want Accepted", outcome)
	}
	if !keyExists(t, ctx, conn, instructorA+".slot20260708t090000z") {
		t.Fatalf("expected instructorA's slot claim at 09:00")
	}

	env := reassignSessionEnv(t, "wdinstrswpreassign1", sessionKey, studioA, domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioA, "newInstructor": instructorB},
		"2026-07-08T08:00:00Z")
	outcome2, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome2 != processor.OutcomeAccepted {
		msg := ""
		if reply != nil && reply.Error != nil {
			msg = reply.Error.Message
		}
		t.Fatalf("ReassignSession newInstructor outcome = %v (%s), want Accepted", outcome2, msg)
	}
	if keyExists(t, ctx, conn, instructorA+".slot20260708t090000z") {
		t.Errorf("instructorA's OLD slot claim at 09:00 must be released after the swap")
	}
	if !keyExists(t, ctx, conn, instructorB+".slot20260708t090000z") {
		t.Errorf("instructorB's NEW slot claim at 09:00 must be claimed after the swap")
	}

	// instructorA is now free to lead an overlapping class elsewhere.
	studioB := createStudio(t, ctx, conn, cp, cons, "wdinstrswpstudiob001", "Room B")
	_, outcomeA, replyA := createSessionWithInstructor(t, ctx, conn, cp, cons, "wdinstrswpsessa0002",
		studioB, instructorA, "Power Sculpt", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcomeA != processor.OutcomeAccepted {
		msg := ""
		if replyA != nil && replyA.Error != nil {
			msg = replyA.Error.Message
		}
		t.Fatalf("freed instructorA outcome = %v (%s), want Accepted", outcomeA, msg)
	}

	// instructorB is now blocked from a SECOND overlapping class.
	studioC := createStudio(t, ctx, conn, cp, cons, "wdinstrswpstudioc001", "Room C")
	_, outcomeB, replyB := createSessionWithInstructor(t, ctx, conn, cp, cons, "wdinstrswpsessb0002",
		studioC, instructorB, "Restorative", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcomeB != processor.OutcomeRejected {
		t.Fatalf("instructorB double-booked after the swap outcome = %v, want Rejected (InstructorConflict)", outcomeB)
	}
	if replyB.Error == nil || !strings.Contains(replyB.Error.Message, "InstructorConflict") {
		t.Errorf("rejection should be InstructorConflict, got %+v", replyB.Error)
	}
}

// TestCreateBooking_RejectsBookerAcrossOverlappingSessions proves
// bookerSlotClaim (the patientSlotClaim mirror, verticals.md) catches the
// gap sessionBookerClaim alone cannot see: the SAME booker claimed into TWO
// DIFFERENT overlapping sessions.
func TestCreateBooking_RejectsBookerAcrossOverlappingSessions(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "bookerdoublebook")

	studioA := createStudio(t, ctx, conn, cp, cons, "wdbkrdblstudioa00001", "Room A")
	studioB := createStudio(t, ctx, conn, cp, cons, "wdbkrdblstudiob00001", "Room B")
	sessionA, outcome := createSession(t, ctx, conn, cp, cons, "wdbkrdblsessa000001", studioA, "Morning Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession A outcome = %v, want Accepted", outcome)
	}
	sessionB, outcome := createSession(t, ctx, conn, cp, cons, "wdbkrdblsessb000001", studioB, "Power Sculpt", "2026-07-08T09:15:00Z", "2026-07-08T09:45:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession B outcome = %v, want Accepted", outcome)
	}

	booker := seedIdentity(t, ctx, conn, "BBWELLBKRDBLHJKMNPQR")
	_, first := createBooking(t, ctx, conn, cp, cons, "wdbkrdblbookina0001", sessionA, booker, "")
	if first != processor.OutcomeAccepted {
		t.Fatalf("first CreateBooking outcome = %v, want Accepted", first)
	}

	reqID := testutil.GenReqID("wdbkrdblbookinb0001")
	payload, _ := json.Marshal(map[string]any{"session": sessionB, "booker": booker})
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "booking",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         []string{sessionB, sessionB + ".schedule", booker},
			OptionalReads: append(wdSeatKeys(sessionB, 20), wdSlotClaimKeys(t, booker, "2026-07-08T09:15:00Z", "2026-07-08T09:45:00Z")...),
		},
	}
	outcome2, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome2 != processor.OutcomeRejected {
		t.Fatalf("same booker into a DIFFERENT overlapping session outcome = %v, want Rejected (BookerConflict)", outcome2)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "BookerConflict") {
		t.Errorf("rejection should be BookerConflict, got %+v", reply.Error)
	}
}

// TestCancelBooking_ReleasesBookerSlotCells proves CancelBooking releases the
// booker's bookerSlotClaim cells too, not just the per-session guard — the
// booker can then book a DIFFERENT, overlapping session.
func TestCancelBooking_ReleasesBookerSlotCells(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "bookercancelrelease")

	studioA := createStudio(t, ctx, conn, cp, cons, "wdbkrcxlstudioa00001", "Room A")
	studioB := createStudio(t, ctx, conn, cp, cons, "wdbkrcxlstudiob00001", "Room B")
	sessionA, outcome := createSession(t, ctx, conn, cp, cons, "wdbkrcxlsessa000001", studioA, "Morning Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession A outcome = %v, want Accepted", outcome)
	}
	sessionB, outcome := createSession(t, ctx, conn, cp, cons, "wdbkrcxlsessb000001", studioB, "Power Sculpt", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession B outcome = %v, want Accepted", outcome)
	}

	booker := seedIdentity(t, ctx, conn, "BBWELLBKRCXLHJKMNPQR")
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdbkrcxlbookina0001", sessionA, booker, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", outcome)
	}
	if !keyExists(t, ctx, conn, booker+".slot20260708t090000z") {
		t.Fatalf("expected bookerSlotClaim at 09:00")
	}

	cancelEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wdbkrcxlcancel00001"),
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T08:30:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","session":"` + sessionA + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			bookingKey, bookingKey + ".status", sessionA + ".schedule",
			forSessionLnkKey(t, bookingKey, sessionA),
		}},
	}
	testutil.PublishOp(t, conn, cancelEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, booker+".slot20260708t090000z") {
		t.Fatalf("bookerSlotClaim at 09:00 must be released after CancelBooking")
	}

	_, outcome2 := createBooking(t, ctx, conn, cp, cons, "wdbkrcxlbookinb0001", sessionB, booker, "")
	if outcome2 != processor.OutcomeAccepted {
		t.Fatalf("same booker into a different overlapping session after release outcome = %v, want Accepted", outcome2)
	}
}
