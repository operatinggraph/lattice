// RecordBookingReminder integration test for the wellness-reminders Capability
// Package — the op's write path driven end to end through a real Processor.
//
// External test package (wellnessreminders_test) so it exercises the public
// Lattice surface: seed the kernel, install rbac + identity + hygiene (via the
// harness) + orchestration-base + wellness-domain + wellness-reminders through
// the Processor, then drive RecordBookingReminder and assert the committed
// Core-KV shape. Mirrors clinic-reminders/integration_test.go, which is this
// package's direct sibling.
//
// The booking root is SEEDED directly rather than minted through
// wellness-domain's CreateBooking. RecordBookingReminder only requires a live
// vtx.booking.<id> (its `vertex_alive` guard); driving the real booking flow
// would drag in a studio, a session, seat/booker slot claims and the residency
// read, none of which this op or its actor guard touches. The seeded root is
// the same fixture idiom clinic-reminders uses for its tombstoned-appointment
// case.
//
// Both directions of the primordial actor guard are proven here: the ADMIT path
// (Weaver's dispatch actor is accepted and the notification actually goes out)
// and the DENY path (an operator-role holder that is not Weaver is refused).
// The admit half matters as much as the deny half — a wrongly wired engine key
// would silently stop every wellness reminder, and nothing else in this package
// exercises the op at runtime.
package wellnessreminders_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
	wellnessreminders "github.com/operatinggraph/lattice/packages/wellness-reminders"
)

const (
	wrStaffActorID  = "WRstaffActHJKMNPQRST"
	wrStaffActorKey = "vtx.identity." + wrStaffActorID
	wrStaffCapKey   = "cap.identity." + wrStaffActorID

	// wrSubmittedAnchor is the op's submittedAt; the script normalizes it into
	// the .reminder marker's sentAt, so the assertion below is exact rather
	// than a wall-clock comparison.
	wrSubmittedAnchor = "2026-01-01T00:00:00Z"
	wrRemindedFor     = "2026-07-01T15:00:00Z"
)

// wrStaffCapDoc grants the staff actor RecordBookingReminder at Scope:"any"
// with the operator role — deliberately the SAME grant Weaver holds. That is
// what makes the negative test below attributable: the refusal can only come
// from the script's actor guard, never from a missing or narrower grant.
func wrStaffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    wrStaffCapKey,
		Actor:                  wrStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{wrStaffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "RecordBookingReminder", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// wrWeaverCapDoc grants Weaver's primordial dispatch actor the same op. Read
// through a func, not a package var: bootstrap's primordial globals are
// populated by SetupPackageTestEnv's EnsurePrimordials, well after package var
// initialization.
func wrWeaverCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + bootstrap.WeaverIdentityID,
		Actor:                  bootstrap.WeaverIdentityKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{bootstrap.WeaverIdentityKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "RecordBookingReminder", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

func setupWellRemEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // rbac + identity + hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	// wrConsumerRoleID stands in for identity-domain's real `consumer` role
	// NanoID: wellness-domain's own scope=self grants name it, and these tests
	// do not install identity-domain (the clinic-reminders remConsumerRoleID
	// idiom).
	const wrConsumerRoleID = "WRMConsumerRoZeHJKMN"
	inst.RoleIDs = map[string]string{
		"operator":     bootstrap.RoleOperatorID,
		"consumer":     wrConsumerRoleID,
		"frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"),
		"backOfHouse":  pkgmgr.RoleID("identity-domain", "backOfHouse"),
		"provider":     pkgmgr.RoleID("identity-domain", "provider"),
	}
	for _, p := range []pkgmgr.Definition{orchestrationbase.Package, wellnessdomain.Package, wellnessreminders.Package} {
		if _, err := inst.Install(ctx, p); err != nil {
			stop()
			t.Fatalf("install %s: %v", p.Name, err)
		}
	}
	stop()
	testutil.SeedCapDoc(t, ctx, conn, wrStaffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, wrWeaverCapDoc())
	testutil.SeedHoldsRole(t, ctx, conn, wrStaffActorKey, bootstrap.RoleOperatorKey)
	return ctx, conn
}

// wrSeedBooking writes a live vtx.booking.<id> root — the only state
// RecordBookingReminder's liveness guard reads.
func wrSeedBooking(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.booking." + id
	doc := map[string]any{"class": "booking", "isDeleted": false, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed booking %s: %v", key, err)
	}
	return key
}

// wrSeedSession writes a live vtx.session.<id> root plus a .schedule aspect
// (class sessionSchedule, wellness-domain's own class name) carrying only
// startsAt — the one field RecordBookingReminder's already-started guard
// reads. wellness-domain's real CreateSession stamps a richer document (name,
// endsAt, capacity, remindAt); the op never reads any of that, so the
// fixture carries the minimum the guard depends on, the same seeded-root
// idiom wrSeedBooking uses for the booking.
func wrSeedSession(t *testing.T, ctx context.Context, conn *substrate.Conn, id, startsAt string) string {
	t.Helper()
	key := "vtx.session." + id
	doc := map[string]any{"class": "session", "isDeleted": false, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed session %s: %v", key, err)
	}
	schedKey := key + ".schedule"
	sched := map[string]any{"class": "sessionSchedule", "vertexKey": key, "localName": "schedule", "isDeleted": false,
		"data": map[string]any{"startsAt": startsAt}}
	sb, _ := json.Marshal(sched)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, schedKey, sb); err != nil {
		t.Fatalf("seed session schedule %s: %v", schedKey, err)
	}
	return key
}

// wrSubmit drives one RecordBookingReminder as `actor`, against the class
// wrSeedSession seeds at wrRemindedFor (still in the future relative to
// wrSubmittedAnchor). Class is LEFT EMPTY, exactly as Weaver's actuator
// dispatches a directOp (it relies on the Processor's operationType→class
// reverse index, which resolves to the bookingReminderOp vertexType
// handler).
func wrSubmit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	actor, label, bookingKey, sessionKey string, want processor.MessageOutcome) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RecordBookingReminder",
		Actor:         actor,
		SubmittedAt:   wrSubmittedAnchor,
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","sessionKey":"` + sessionKey + `","remindedFor":"` + wrRemindedFor + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{bookingKey, sessionKey + ".schedule"}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return outcome, reply
}

func wrReadDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
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

// TestRecordBookingReminder_WeaverActorAccepted is the ADMIT half of the
// primordial actor guard: submitted as bootstrap.WeaverIdentityKey — the actor
// Weaver's own actuator stamps on a directOp (internal/weaver/actuator.go, from
// cmd/weaver's ActorKey) — the op commits, writes the .reminder marker, and
// emits the external.notification the bridge turns into a real message. Without
// this, a mis-wired `weaver` registry entry would deny every wellness reminder
// and no test in this package would notice.
func TestRecordBookingReminder_WeaverActorAccepted(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrremok", Instance: "wr-remok",
	})

	bookingKey := wrSeedBooking(t, ctx, conn, "WRbookAKHJKMNPQRSTVW")
	sessionKey := wrSeedSession(t, ctx, conn, "WRsessAKHJKMNPQRSTVW", wrRemindedFor)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wrremok00001"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordBookingReminder",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   wrSubmittedAnchor,
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","sessionKey":"` + sessionKey + `","remindedFor":"` + wrRemindedFor + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{bookingKey, sessionKey + ".schedule"}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("Weaver-actor reminder: outcome = %v, want Accepted (reply: %+v)", outcome, reply.Error)
	}

	// (a) the marker aspect: class bookingReminder, sentAt normalized from the
	// op's own submittedAt, remindedFor recorded verbatim (the column the
	// convergence gate compares against the session's startsAt).
	rem := wrReadDoc(t, ctx, conn, bookingKey+".reminder")
	if cls, _ := rem["class"].(string); cls != "bookingReminder" {
		t.Fatalf("reminder class = %q, want bookingReminder", cls)
	}
	rd, _ := rem["data"].(map[string]any)
	if s, _ := rd["sentAt"].(string); s != wrSubmittedAnchor {
		t.Fatalf("reminder sentAt = %q, want %q", s, wrSubmittedAnchor)
	}
	if rf, _ := rd["remindedFor"].(string); rf != wrRemindedFor {
		t.Fatalf("reminder remindedFor = %q, want %q", rf, wrRemindedFor)
	}

	// (b) the egress the actor guard exists to protect actually fires: the
	// external.notification event on this op's own transactional outbox,
	// carrying the bridge-reader shape keyed on (bookingKey, remindedFor).
	outboxKey := processor.OutboxAspectKey(env.RequestID)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, outboxKey)
	if err != nil {
		t.Fatalf("read outbox aspect %s: %v", outboxKey, err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect %s: %v", outboxKey, err)
	}
	var notif map[string]any
	var seen []string
	for _, e := range ob.Data.Events {
		seen = append(seen, e.EventType)
		if e.EventType == "external.notification" {
			notif = e.Payload
		}
	}
	if notif == nil {
		t.Fatalf("no external.notification emitted (events: %v)", seen)
	}
	wantRef := bookingKey + ":" + wrRemindedFor
	if got, _ := notif["externalRef"].(string); got != wantRef {
		t.Fatalf("external.notification externalRef = %q, want %q", got, wantRef)
	}
	if got, _ := notif["adapter"].(string); got != "notification" {
		t.Fatalf("external.notification adapter = %q, want notification", got)
	}
}

// TestRecordBookingReminder_NonWeaverOperatorDenied is the DENY half.
// wrStaffActorKey holds the operator role AND the identical Scope:"any"
// RecordBookingReminder grant, so step 3 authorizes it; only the script's
// `op.actor != primordialActor["weaver"]` check stops it from having the
// platform notify an arbitrary client about an arbitrary booking. The payload
// is the one the admit test commits, differing in the actor alone.
func TestRecordBookingReminder_NonWeaverOperatorDenied(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrremforged", Instance: "wr-remforged",
	})

	bookingKey := wrSeedBooking(t, ctx, conn, "WRbookFGHJKMNPQRSTVW")
	sessionKey := wrSeedSession(t, ctx, conn, "WRsessFGHJKMNPQRSTVW", wrRemindedFor)
	_, reply := wrSubmit(t, ctx, conn, cp, cons, wrStaffActorKey, "wrremfg00001", bookingKey, sessionKey, processor.OutcomeRejected)

	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
		t.Fatalf("want an AuthDenied rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "Weaver's dispatch actor") {
		t.Fatalf("the denial must name the actor guard, got %q", reply.Error.Message)
	}
	// Nothing minted: a denied reminder leaves the convergence gap OPEN, so
	// Weaver's own dispatch still fires later. A written marker would have
	// closed the gap on the strength of a forged send.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, bookingKey+".reminder"); err == nil {
		t.Fatalf("a denied reminder op must write NO .reminder marker")
	}
}

// TestRecordBookingReminder_RejectsStartedSession proves the already-started
// guard itself (§7 role (c)): a LIVE session whose .schedule.startsAt is
// before op.submittedAt is refused by name (ClassAlreadyStarted), and — like
// every other refusal in this op — writes no .reminder marker, leaving the
// gap open for Weaver to keep retrying (harmlessly; the op always declines
// while the class stays started). The deadline lives on the SESSION
// neighbour, not the booking, which is what makes this a distinct vector
// from clinic-reminders' single-anchor guard.
func TestRecordBookingReminder_RejectsStartedSession(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrremstart", Instance: "wr-remstart",
	})

	bookingKey := wrSeedBooking(t, ctx, conn, "WRbookSTHJKMNPQRSTVW")
	sessionKey := wrSeedSession(t, ctx, conn, "WRsessSTHJKMNPQRSTVW", "2025-12-31T00:00:00Z") // before wrSubmittedAnchor
	_, reply := wrSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "wrremstart01", bookingKey, sessionKey, processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "ClassAlreadyStarted") {
		t.Fatalf("want a ClassAlreadyStarted rejection, got %+v", reply.Error)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, bookingKey+".reminder"); err == nil {
		t.Fatalf("a reminder marker must NOT be written for a started class")
	}
}

// TestRecordBookingReminder_NonWeaverActorRefusedBeforeClock proves the
// GUARD ORDER §16.4 requires: the primordial actor-guard runs before the
// already-started clock check, so a non-Weaver actor is refused on actor
// grounds even when it names a session that HAS already started (which would
// independently fail the clock guard too, if execution ever reached it).
// Same started-session fixture as
// TestRecordBookingReminder_RejectsStartedSession; only the actor differs.
func TestRecordBookingReminder_NonWeaverActorRefusedBeforeClock(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrremstfg", Instance: "wr-remstfg",
	})

	bookingKey := wrSeedBooking(t, ctx, conn, "WRbookSFHJKMNPQRSTVW")
	sessionKey := wrSeedSession(t, ctx, conn, "WRsessSFHJKMNPQRSTVW", "2025-12-31T00:00:00Z")
	_, reply := wrSubmit(t, ctx, conn, cp, cons, wrStaffActorKey, "wrremstfg01", bookingKey, sessionKey, processor.OutcomeRejected)

	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
		t.Fatalf("want an AuthDenied rejection (the actor guard, not the clock guard), got %+v", reply.Error)
	}
	if strings.Contains(reply.Error.Message, "ClassAlreadyStarted") {
		t.Fatalf("the actor guard must fire BEFORE the clock is read; got the clock guard's message: %q", reply.Error.Message)
	}
}

// TestRecordBookingReminder_FractionalSecondAlreadyStarted proves the guard
// normalizes op.submittedAt (time.rfc3339_utc) before comparing — without it
// a raw lexical compare mis-answers for the first second after an instant:
// the byte '.' (0x2E) sorts BELOW 'Z' (0x5A), so the raw string
// "2026-01-01T10:00:00.120Z" compares less than "2026-01-01T10:00:00Z" even
// though 10:00:00.120 is 120ms AFTER 10:00:00.000 — a raw compare would
// wrongly treat the class as still in the future. Normalizing submittedAt to
// whole-second RFC3339 makes it equal to startsAt, and equal fails the
// strict `<` guard, so this must be refused.
func TestRecordBookingReminder_FractionalSecondAlreadyStarted(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrremfrac", Instance: "wr-remfrac",
	})

	bookingKey := wrSeedBooking(t, ctx, conn, "WRbookFRHJKMNPQRSTVW")
	sessionKey := wrSeedSession(t, ctx, conn, "WRsessFRHJKMNPQRSTVW", "2026-01-01T10:00:00Z")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wrremfrac01"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordBookingReminder",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-01-01T10:00:00.120Z",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","sessionKey":"` + sessionKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{bookingKey, sessionKey + ".schedule"}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("fractional-second-after-start: outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "ClassAlreadyStarted") {
		t.Fatalf("want a ClassAlreadyStarted rejection (normalized compare), got %+v", reply.Error)
	}
}
