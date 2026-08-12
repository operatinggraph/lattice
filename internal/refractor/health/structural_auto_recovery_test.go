// The reader for a structural pause that healed itself. The supervisor's probe
// can now clear a structural pause with nobody involved, and the entry it leaves
// behind reads `active` exactly like a lens that never faulted — so without the
// recorder below and the issue it feeds, the self-heal is silent, which is the
// "a frozen row renders green" failure the recovery design exists to refuse
// (structural-pause-recovery-design.md §4.2f).
package health

import (
	"strings"
	"testing"
	"time"
)

// assertHonestRecoveryMessage pins the mechanism BOTH issue messages have to
// state, on both planes, because getting it wrong misdirects the repair the
// issue exists to prompt.
//
// The pause's own backlog DOES replay: the failing message is left un-acked (or
// Nak'd), the ack floor never advances, and everything published during the
// pause is still pending when the consumer resumes. So a cause an operator fixed
// in the schema — a 42703 column, a 42P10 constraint — leaves every row intact
// and owes nothing, and an unconditional "a rebuild is owed" would cry wolf on
// the common case, which is how a warning becomes one operators skip.
//
// What is genuinely at risk is what the recovery itself destroyed: a re-provision
// or a restore from an older backup. Those rows' CDC messages were acked long
// ago and never redeliver — a LARGER hole than the outage window, so an operator
// told "the pause window was lost" would scope the repair too narrowly and miss
// the rest.
func assertHonestRecoveryMessage(t *testing.T, msg string) {
	t.Helper()
	for _, want := range []string{"replays on resume", "never acked", "owes nothing"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message must say the pause's own backlog replays (%q missing); got %q", want, msg)
		}
	}
	for _, want := range []string{"RE-PROVISIONING", "RESTORING", "before the pause"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message must condition the rebuild on re-provision/restore (%q missing); got %q", want, msg)
		}
	}
	// The claim the reviewers falsified, in the words it was written in.
	for _, forbidden := range []string{"fell in the pause window", "replays nothing from that window"} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("message repeats the falsified mechanism %q; got %q", forbidden, msg)
		}
	}
}

func recoveredSnap(name string, at time.Time, cause string, attempts int) LensLivenessStatus {
	return LensLivenessStatus{
		CanonicalName:                  name,
		RuleID:                         "lns-" + name,
		Status:                         "active",
		StructuralAutoRecoveredAt:      at,
		StructuralAutoRecoveredCause:   cause,
		StructuralAutoRecoveryAttempts: attempts,
	}
}

// The positive vector. A lens that is active, caught up and green in every other
// field, which was dark until its own probe let it back in — the exact state no
// other field in the snapshot can express.
func TestEvalLenses_BusinessStructuralAutoRecoveryIsRaisedInsideTheWindow(t *testing.T) {
	h := &LatticeHeartbeater{}
	now := time.Now()
	recoveredAt := now.Add(-h.structuralAutoRecoveryWindow() / 2)

	metric, issues := beat(h, now, recoveredSnap("clinicEncountersRead", recoveredAt,
		`ERROR: column "discharged_at" does not exist (SQLSTATE 42703)`, 1))

	is, ok := issueByCode(issues, issueLensStructuralPauseAutoRecovered)
	if !ok {
		t.Fatalf("a self-healed structural pause must raise an issue; got %+v", issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning", is.Severity)
	}
	// Name, cause and attempt count are the whole payload: the name says which
	// read model has a hole, the cause says what was wrong with it, and the
	// attempt count says how close the lens is to its relapse latch.
	for _, want := range []string{"clinicEncountersRead", "attempt 1", "discharged_at"} {
		if !strings.Contains(is.Message, want) {
			t.Fatalf("message must carry %q; got %q", want, is.Message)
		}
	}
	assertHonestRecoveryMessage(t, is.Message)
	if got := metric["clinicEncountersRead"]["alert"]; got != "structural-pause-auto-recovered" {
		t.Fatalf("the lens's own alert must reflect it; got %v", got)
	}
}

// The negative, and the reason the issue is age-gated at all: the stamp lives on
// the health entry for the life of the lens, so an ungated raise would sit open
// forever on a lens that is fine, and an issue that never clears is one nobody
// reads.
func TestEvalLenses_BusinessStructuralAutoRecoveryIsSilentOutsideTheWindow(t *testing.T) {
	h := &LatticeHeartbeater{}
	now := time.Now()
	recoveredAt := now.Add(-h.structuralAutoRecoveryWindow() - time.Second)

	metric, issues := beat(h, now, recoveredSnap("clinicEncountersRead", recoveredAt, "table absent", 1))

	if _, ok := issueByCode(issues, issueLensStructuralPauseAutoRecovered); ok {
		t.Fatalf("a recovery older than the window must raise nothing; got %+v", issues)
	}
	if got := metric["clinicEncountersRead"]["alert"]; got != "ok" {
		t.Fatalf("alert must fall back to ok; got %v", got)
	}
}

// A lens that has never self-healed — the whole corpus until one does — must
// stay silent, and must not be scored as having recovered at the zero time.
func TestEvalLenses_BusinessNeverRecoveredRaisesNothing(t *testing.T) {
	h := &LatticeHeartbeater{}
	_, issues := beat(h, time.Now(), recoveredSnap("clinicPatientsRead", time.Time{}, "", 0))

	if _, ok := issueByCode(issues, issueLensStructuralPauseAutoRecovered); ok {
		t.Fatalf("a lens that never self-healed must raise nothing; got %+v", issues)
	}
}

// Two cycles, not one. The pump stamps the recovery at an arbitrary point inside
// a cycle, so a one-cycle window can be straddled and emit NOTHING — the silent
// self-heal this signal exists to refuse. This pins that a recovery stamped a
// full cycle before the beat that observes it is still announced.
func TestEvalLenses_BusinessStructuralAutoRecoverySurvivesAStraddledCycle(t *testing.T) {
	h := NewLatticeHeartbeater(nil, "health", "rfx-test", 30*time.Second, nil)
	now := time.Now()
	// Stamped just after the previous beat read the entry: a strict one-cycle
	// window would have expired by the time this beat runs.
	recoveredAt := now.Add(-31 * time.Second)

	_, issues := beat(h, now, recoveredSnap("clinicEncountersRead", recoveredAt, "table absent", 2))

	if _, ok := issueByCode(issues, issueLensStructuralPauseAutoRecovered); !ok {
		t.Fatalf("a recovery one cycle old must still be announced; got %+v", issues)
	}
}

// The window is scaled off the heartbeat's own cadence, so a deployment that
// beats slowly still gets its emissions rather than a window that expires
// between beats.
func TestStructuralAutoRecoveryWindow_ScalesWithTheHeartbeatCadence(t *testing.T) {
	h := NewLatticeHeartbeater(nil, "health", "rfx-test", time.Minute, nil)
	if got, want := h.structuralAutoRecoveryWindow(), 2*time.Minute; got != want {
		t.Fatalf("window = %s, want %s", got, want)
	}
	// A heartbeater assembled without the constructor must not compute a
	// zero-length window: zero raises nothing, which is indistinguishable from
	// the recovery never having happened.
	zero := &LatticeHeartbeater{}
	if got := zero.structuralAutoRecoveryWindow(); got != 2*minHeartbeatInterval {
		t.Fatalf("floored window = %s, want %s", got, 2*minHeartbeatInterval)
	}
}

// A fault reading one input must not erase a fact already in hand about another.
// The recovery reaches the snapshot from the same successful status read the
// redaction count does, and the cycle a reader most needs to be told about a
// self-heal is the one where something else about the lens could not be
// observed.
func TestEvalLenses_BusinessStructuralAutoRecoverySurvivesAnUnreadableCycle(t *testing.T) {
	h := &LatticeHeartbeater{}
	now := time.Now()
	s := recoveredSnap("clinicEncountersRead", now.Add(-time.Second), "table absent", 1)
	s.Status = "unknown"
	s.Unreadable = "consumer pending count: timeout"

	_, issues := beat(h, now, s)

	if _, ok := issueByCode(issues, issueLensStructuralPauseAutoRecovered); !ok {
		t.Fatalf("the recovery must survive an unreadable cycle; got %+v", issues)
	}
}

// The token is the quietest in the table: it reports a window that has already
// closed, so anything currently wrong outranks it. A lens that self-healed and
// is now paused again must render `paused`, never the recovery.
func TestEvalLenses_BusinessStructuralAutoRecoveryYieldsToALivePause(t *testing.T) {
	h := &LatticeHeartbeater{}
	now := time.Now()
	s := recoveredSnap("clinicEncountersRead", now.Add(-time.Second), "table absent", 2)
	s.Status = StatusPaused
	s.PauseReason = PauseReasonStructural
	s.LastError = "table absent"

	metric, issues := beat(h, now, s)

	if got := metric["clinicEncountersRead"]["alert"]; got != "paused" {
		t.Fatalf("a live pause must outrank the recovery token; got %v", got)
	}
	// Both issues are still raised — the alert field is single-valued, the
	// issues array is not, and "it healed once and is down again" is the shape
	// an operator needs to see whole.
	if _, ok := issueByCode(issues, issueLensStructuralPauseAutoRecovered); !ok {
		t.Fatalf("the recovery issue must still be raised alongside the pause; got %+v", issues)
	}
	if _, ok := issueByCode(issues, issueLensProjectionPaused); !ok {
		t.Fatalf("the pause issue must still be raised; got %+v", issues)
	}
}

// A long cause is truncated on the same cap as pausedLabel: several lenses can
// heal in one cycle, and the issue message must not push the health entry past
// what it should carry.
func TestStructuralRecoveryLabel_TruncatesALongCause(t *testing.T) {
	long := strings.Repeat("x", 400)
	got := structuralRecoveryLabel("clinicEncountersRead", structuralRecovery{cause: long, attempts: 3})
	if len(got) > 260 {
		t.Fatalf("label must be truncated; got %d chars", len(got))
	}
	if !strings.Contains(got, "attempt 3") || !strings.HasSuffix(got, "...)") {
		t.Fatalf("label = %q", got)
	}
}

// A recovery with no recorded cause still names the lens and the attempt. The
// pump can clear a structural pause whose cause was never persisted (a restored
// pause from a previous process), and a label that swallowed the whole recovery
// for a missing string would be the silent case again.
func TestStructuralRecoveryLabel_CauselessRecoveryStillReports(t *testing.T) {
	got := structuralRecoveryLabel("clinicEncountersRead", structuralRecovery{attempts: 1})
	if got != "clinicEncountersRead (attempt 1)" {
		t.Fatalf("label = %q", got)
	}
}

// capBeat drives one capability-lens evaluation, the auth-plane sibling of beat.
func capBeat(h *LatticeHeartbeater, at time.Time, snaps ...CapabilityLensStatus) (map[string]map[string]any, []issueRecord) {
	h.CapabilityLensProvider = func() []CapabilityLensStatus { return snaps }
	return h.evalCapabilityLenses(at)
}

func capRecoveredSnap(name string, at time.Time, cause string, attempts int) CapabilityLensStatus {
	return CapabilityLensStatus{
		CanonicalName:                  name,
		RuleID:                         "lns-" + name,
		Status:                         "active",
		StructuralAutoRecoveredAt:      at,
		StructuralAutoRecoveredCause:   cause,
		StructuralAutoRecoveryAttempts: attempts,
	}
}

// The auth-plane positive vector, and the reason this path exists at all.
// projection.IsAuthPlane returns true for postgres+GrantTable, so EVERY
// grant-table lens is auth-plane — and every grant-table lens is opted into
// structural self-heal, because its Probe is VerifyGrantTable. Without this
// evaluation the lenses feeding actor_read_grants (the read-path authorization
// source of truth) would be the only class in the system able to clear a
// structural pause completely silently.
func TestEvalCapabilityLenses_AuthPlaneStructuralAutoRecoveryIsRaisedInsideTheWindow(t *testing.T) {
	h := &LatticeHeartbeater{}
	now := time.Now()
	recoveredAt := now.Add(-h.structuralAutoRecoveryWindow() / 2)

	metric, issues := capBeat(h, now, capRecoveredSnap("clinicGrants", recoveredAt,
		`ERROR: relation "actor_read_grants" does not exist (SQLSTATE 42P01)`, 1))

	is, ok := issueByCode(issues, issueCapabilityLensStructuralPauseAutoRecovered)
	if !ok {
		t.Fatalf("a self-healed capability lens must raise an issue; got %+v", issues)
	}
	// warning, NOT error — the one place this path's severity ladder is
	// deliberately not mirrored. A lens that successfully recovered is not
	// unhealthy, and taking the whole instance unhealthy for a working self-heal
	// trains operators to ignore the signal.
	if is.Severity != "warning" {
		t.Fatalf("a successful self-heal must not take the instance unhealthy; severity = %q", is.Severity)
	}
	for _, want := range []string{"clinicGrants", "attempt 1", "actor_read_grants"} {
		if !strings.Contains(is.Message, want) {
			t.Fatalf("message must carry %q; got %q", want, is.Message)
		}
	}
	// The message has to state the mechanism CORRECTLY, in both directions, or
	// it misdirects the repair it exists to prompt. The pause's own backlog does
	// replay — the failing message was never acked — so a schema fix owes
	// nothing, and an unconditional "a rebuild is owed" would cry wolf on the
	// common case. What is actually at risk is what a RE-PROVISION or RESTORE
	// destroyed: rows acked long before the pause, which never redeliver. That
	// is a larger scope than the outage window, and an operator told "the pause
	// window was lost" would repair the wrong set.
	assertHonestRecoveryMessage(t, is.Message)
	// And the grant-specific consequence, conditioned the same way.
	if !strings.Contains(is.Message, "under-granted") || !strings.Contains(is.Message, "fail closed") {
		t.Fatalf("message must name the under-grant and its safe direction; got %q", is.Message)
	}
	if got := metric["clinicGrants"]["alert"]; got != "structural-pause-auto-recovered" {
		t.Fatalf("the lens's own alert must reflect it; got %v", got)
	}
}

// The negative. Same age gate as the business path, from the same window
// helper — the two planes must never disagree about what "recently" means.
func TestEvalCapabilityLenses_AuthPlaneStructuralAutoRecoveryIsSilentOutsideTheWindow(t *testing.T) {
	h := &LatticeHeartbeater{}
	now := time.Now()
	recoveredAt := now.Add(-h.structuralAutoRecoveryWindow() - time.Second)

	metric, issues := capBeat(h, now, capRecoveredSnap("clinicGrants", recoveredAt, "table absent", 1))

	if _, ok := issueByCode(issues, issueCapabilityLensStructuralPauseAutoRecovered); ok {
		t.Fatalf("a recovery older than the window must raise nothing; got %+v", issues)
	}
	if got := metric["clinicGrants"]["alert"]; got != "ok" {
		t.Fatalf("alert must fall back to ok; got %v", got)
	}
}

// A capability lens that has never self-healed — the whole corpus until one
// does — must stay silent rather than score the zero time as a recovery.
func TestEvalCapabilityLenses_AuthPlaneNeverRecoveredRaisesNothing(t *testing.T) {
	h := &LatticeHeartbeater{}
	_, issues := capBeat(h, time.Now(), capRecoveredSnap("capabilityRoles", time.Time{}, "", 0))

	if _, ok := issueByCode(issues, issueCapabilityLensStructuralPauseAutoRecovered); ok {
		t.Fatalf("a lens that never self-healed must raise nothing; got %+v", issues)
	}
}

// Same doctrine as the business path: the recovery comes from a status read that
// succeeded, so a fault observing the consumer's pending count must not erase
// it. On the auth plane this matters more, not less — an unobserved
// authorization lens is the worst cycle to also go quiet about a self-heal.
func TestEvalCapabilityLenses_AuthPlaneStructuralAutoRecoverySurvivesAnUnreadableCycle(t *testing.T) {
	h := &LatticeHeartbeater{}
	now := time.Now()
	s := capRecoveredSnap("clinicGrants", now.Add(-time.Second), "table absent", 1)
	s.Status = "unknown"
	s.Unreadable = "consumer pending count: timeout"

	_, issues := capBeat(h, now, s)

	if _, ok := issueByCode(issues, issueCapabilityLensStructuralPauseAutoRecovered); !ok {
		t.Fatalf("the recovery must survive an unreadable cycle; got %+v", issues)
	}
}

// Both planes read the same window from the same helper. A drift here would let
// one plane announce a recovery the other had already aged out, which is the
// duplicated-time-comparison failure the shared helper exists to prevent.
//
// The two CODES are deliberately distinct (each plane reconciles its own `since`
// under its own identity); it is the window that is shared, and it is the window
// this pins.
func TestStructuralAutoRecovery_BothPlanesShareOneWindow(t *testing.T) {
	h := NewLatticeHeartbeater(nil, "health", "rfx-test", 30*time.Second, nil)
	now := time.Now()
	at := now.Add(-59 * time.Second) // inside 2×30s, by one second

	_, capIssues := capBeat(h, now, capRecoveredSnap("clinicGrants", at, "table absent", 1))
	_, lensIssues := beat(h, now, recoveredSnap("clinicEncountersRead", at, "table absent", 1))

	_, capRaised := issueByCode(capIssues, issueCapabilityLensStructuralPauseAutoRecovered)
	_, lensRaised := issueByCode(lensIssues, issueLensStructuralPauseAutoRecovered)
	if capRaised != lensRaised {
		t.Fatalf("the two planes disagreed on one window: cap=%v business=%v", capRaised, lensRaised)
	}
	if !capRaised {
		t.Fatal("both planes must raise a recovery one second inside the window")
	}
}
