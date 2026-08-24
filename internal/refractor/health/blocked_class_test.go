// CapabilityRepairBlocked / LensRepairBlocked, read by CLASS. The blocked
// counter is the capability plane's most severe standing alert, and one counter
// with one streak ladder renders a benign provenance drift and a grant
// divergence with no known producer identically, at the same severity, on the
// same schedule — so an alert already at maximum volume for the benign one
// cannot raise its voice when the other arrives.
package health

import (
	"strings"
	"testing"
	"time"
)

// capBlockedSnap is an active auth-plane lens holding a blocked set of the given
// shape. The four counts are what the sweep's census publishes; the streak is
// what the old ladder read alone.
func capBlockedSnap(name string, streak int, retraction, content, unknown, provenance int, worst, reason string) CapabilityLensStatus {
	return CapabilityLensStatus{
		CanonicalName:          name,
		RuleID:                 "cap-" + name,
		Status:                 "active",
		SweepInterval:          time.Minute,
		SweepBlocked:           retraction + content + unknown + provenance,
		SweepBlockedStreak:     streak,
		SweepLastBlocked:       reason,
		SweepBlockedRetraction: retraction,
		SweepBlockedContent:    content,
		SweepBlockedUnknown:    unknown,
		SweepBlockedProvenance: provenance,
		SweepWorstBlocked:      worst,
	}
}

// lensBlockedSnap is capBlockedSnap's business-lens twin, so both issue paths
// are driven by the same shapes rather than by two hand-tuned fixtures.
func lensBlockedSnap(name string, streak int, retraction, content, unknown, provenance int, worst, reason string) LensLivenessStatus {
	return LensLivenessStatus{
		CanonicalName:          name,
		RuleID:                 "lns-" + name,
		Status:                 "active",
		SweepInterval:          time.Minute,
		SweepBlocked:           retraction + content + unknown + provenance,
		SweepBlockedStreak:     streak,
		SweepLastBlocked:       reason,
		SweepBlockedRetraction: retraction,
		SweepBlockedContent:    content,
		SweepBlockedUnknown:    unknown,
		SweepBlockedProvenance: provenance,
		SweepWorstBlocked:      worst,
	}
}

func capBlocked(t *testing.T, snap CapabilityLensStatus) (map[string]any, issueRecord) {
	t.Helper()
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{snap}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	is, ok := issueByCode(issues, issueCapabilityRepairBlocked)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilityRepairBlocked, issues)
	}
	return metric[snap.CanonicalName], is
}

func lensBlocked(t *testing.T, snap LensLivenessStatus) (map[string]any, issueRecord) {
	t.Helper()
	h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
		return []LensLivenessStatus{snap}
	}}
	metric, issues := h.evalLenses(time.Now())
	is, ok := issueByCode(issues, issueLensRepairBlocked)
	if !ok {
		t.Fatalf("expected %s, got %v", issueLensRepairBlocked, issues)
	}
	return metric[snap.CanonicalName], is
}

// TestBlockedSeverity_ContentReachesErrorOnTheFirstPass is the green bar's
// sharpest line. A content divergence at a resting watermark has no observed
// producer, so the FIRST sighting is the finding — waiting for a second
// consecutive pass only delays an alert about a permission set the graph no
// longer grants.
//
// Fails without the class-driven severity: at streak 1 the old ladder returns
// `warning` and the instance stays `degraded`.
func TestBlockedSeverity_ContentReachesErrorOnTheFirstPass(t *testing.T) {
	_, is := capBlocked(t, capBlockedSnap("capabilityRoles", 1, 0, 1, 0, 0, "content",
		"stored watermark >= reconciliation token; content divergence unrepairable"))
	if is.Severity != "error" {
		t.Fatalf("severity = %q, want error on the first pass — a content divergence has no ordinary producer", is.Severity)
	}
	if !strings.Contains(is.Message, "content 1") {
		t.Fatalf("the issue must name the per-class census: %q", is.Message)
	}
}

// TestBlockedSeverity_RetractionReachesErrorOnTheFirstPass covers the fourth,
// worse case that used to hide inside the same counter: a declined retraction is
// the OVER-GRANT direction — a revoked grant stays live and honoured — and it is
// not a divergence class at all.
func TestBlockedSeverity_RetractionReachesErrorOnTheFirstPass(t *testing.T) {
	_, is := capBlocked(t, capBlockedSnap("capabilityRoles", 1, 1, 0, 0, 0, "retraction",
		"stored watermark >= reconciliation token; retraction unrepairable"))
	if is.Severity != "error" {
		t.Fatalf("severity = %q, want error on the first pass", is.Severity)
	}
	if !strings.Contains(is.Message, "retraction 1") {
		t.Fatalf("the issue must name the retraction class: %q", is.Message)
	}
	if strings.Contains(is.Message, "divergence unrepairable") {
		t.Fatalf("a declined retraction must not be reported as a divergence class: %q", is.Message)
	}
}

// TestBlockedSeverity_ProvenanceOnlyNeverEscalates is the discrimination twin,
// and the case the whole classification turns on. Provenance drift is reachable by an
// ordinary lens-definition write that leaves the MATCH unchanged, and the row's
// meaning is identical — so a set made entirely of it must stay a warning
// however long it stands, or the plane sits at maximum volume permanently and
// the class that matters cannot be heard over it.
//
// Fails without the fix: the old ladder reaches `error` at streak 2 and the
// instance reads `unhealthy` forever.
func TestBlockedSeverity_ProvenanceOnlyNeverEscalates(t *testing.T) {
	for _, streak := range []int{1, 2, 17, 250} {
		metric, is := capBlocked(t, capBlockedSnap("capabilityRoles", streak, 0, 0, 0, 16, "provenance",
			"stored watermark >= reconciliation token; provenance-only divergence (projectedFromRevisions) unrepairable"))
		if is.Severity != "warning" {
			t.Fatalf("streak %d: severity = %q, want warning at every streak length", streak, is.Severity)
		}
		if s := aggregateStatus([]issueRecord{is}); s != "degraded" {
			t.Fatalf("streak %d: status = %q, want degraded — benign standing drift must stay visible without drowning the plane", streak, s)
		}
		if got := metric["blockedWorstClass"]; got != "provenance" {
			t.Fatalf("streak %d: blockedWorstClass = %v, want provenance", streak, got)
		}
	}
}

// TestBlockedSeverity_UnknownKeepsTheStreakLadder pins the fail-closed middle.
// A row whose class could not be proven is neither demoted to the benign class
// nor escalated on evidence nobody gathered: it keeps exactly the behaviour that
// shipped, which is the streak ladder.
func TestBlockedSeverity_UnknownKeepsTheStreakLadder(t *testing.T) {
	_, first := capBlocked(t, capBlockedSnap("capabilityRoles", 1, 0, 0, 3, 0, "unknown",
		"reconciliation write carried no ordering token; divergence of unknown kind (no read-back) unrepairable"))
	if first.Severity != "warning" {
		t.Fatalf("severity = %q, want warning on the first pass", first.Severity)
	}
	_, second := capBlocked(t, capBlockedSnap("capabilityRoles", capabilityDivergenceErrorStreak, 0, 0, 3, 0, "unknown",
		"reconciliation write carried no ordering token; divergence of unknown kind (no read-back) unrepairable"))
	if second.Severity != "error" {
		t.Fatalf("severity = %q, want error at the existing streak threshold", second.Severity)
	}
}

// TestBlockedSeverity_OneContentRowAmongProvenanceOnesDecides is the mixed-set
// case, and the shape the live auth-plane signal actually has: sixteen benign
// rows and one that matters. The one must decide.
func TestBlockedSeverity_OneContentRowAmongProvenanceOnesDecides(t *testing.T) {
	metric, is := capBlocked(t, capBlockedSnap("capabilityRoles", 1, 0, 1, 0, 16, "content",
		"stored watermark >= reconciliation token; content divergence unrepairable"))
	if is.Severity != "error" {
		t.Fatalf("severity = %q, want error: one content divergence is not diluted by sixteen benign rows", is.Severity)
	}
	for _, want := range []string{"content 1", "provenance-only 16", "17 row(s) unrepairable"} {
		if !strings.Contains(is.Message, want) {
			t.Fatalf("message missing %q: %q", want, is.Message)
		}
	}
	byClass, ok := metric["blockedByClass"].(map[string]any)
	if !ok {
		t.Fatalf("blockedByClass = %v, want a per-class map", metric["blockedByClass"])
	}
	if byClass["content"] != 1 || byClass["provenance"] != 16 {
		t.Fatalf("blockedByClass = %v, want content 1 and provenance 16", byClass)
	}
	if _, present := byClass["retraction"]; present {
		t.Fatalf("a class that did not fire must read as ABSENT, not as a zero: %v", byClass)
	}
}

// TestBlockedIssue_NamesTheSanctionedRemedy pins the second half of §2. An alert
// that says a row is permanently wrong and stops there leaves the operator to
// rediscover a remedy Contract #6 §6.2 already sanctions.
func TestBlockedIssue_NamesTheSanctionedRemedy(t *testing.T) {
	_, cap := capBlocked(t, capBlockedSnap("capabilityRoles", 1, 0, 1, 0, 0, "content", "content divergence unrepairable"))
	_, lens := lensBlocked(t, lensBlockedSnap("myTasks", 1, 0, 1, 0, 0, "content", "content divergence unrepairable"))
	for _, is := range []issueRecord{cap, lens} {
		for _, want := range []string{"REBUILD", "truncate=true", "watermarks", "replays from empty"} {
			if !strings.Contains(is.Message, want) {
				t.Fatalf("%s must name the sanctioned repair (%q missing): %q", is.Code, want, is.Message)
			}
		}
	}
}

// TestBlockedCensus_SumsToTheTotal pins the standing checklist's second item at
// the surface an operator reads: every census is a premise, so the numbers
// published per class must add up to the total published beside them.
func TestBlockedCensus_SumsToTheTotal(t *testing.T) {
	snap := capBlockedSnap("capabilityRoles", 3, 1, 2, 4, 8, "retraction", "retraction unrepairable")
	metric, _ := capBlocked(t, snap)
	total, ok := metric["blocked"].(int)
	if !ok {
		t.Fatalf("blocked = %v, want an int", metric["blocked"])
	}
	byClass, ok := metric["blockedByClass"].(map[string]any)
	if !ok {
		t.Fatalf("blockedByClass = %v, want a per-class map", metric["blockedByClass"])
	}
	sum := 0
	for _, n := range byClass {
		count, ok := n.(int)
		if !ok {
			t.Fatalf("blockedByClass carries a non-int count: %v", byClass)
		}
		sum += count
	}
	if sum != total {
		t.Fatalf("per-class counts sum to %d, blocked total is %d — a census that disagrees with its total is not a census", sum, total)
	}
}

// TestBlockedSeverity_PlainLensPathClassifiesButStaysWarning drives the business
// path through the same shapes as the auth plane and asserts the two halves that
// differ between the planes.
//
// The CLASSIFICATION crosses in full: the same per-class census, the same worst
// class, the same class-named reason. One surface classifying and the other not
// would be a split with no defensible boundary — a declined retraction means the
// same thing wherever it is found.
//
// The SEVERITY does not. It is capped at `warning` for every class at every
// streak length, like every other business-lens code, and that cap is a
// mechanism (clampToWarning) rather than an omission — see
// TestBlockedSeverity_ThePlainCeilingHoldsWhereTheCapPathEscalates for why.
func TestBlockedSeverity_PlainLensPathClassifiesButStaysWarning(t *testing.T) {
	cases := []struct {
		name                                     string
		streak                                   int
		retraction, content, unknown, provenance int
		worst                                    string
	}{
		{"content on the first pass", 1, 0, 1, 0, 0, "content"},
		{"retraction on the first pass", 1, 1, 0, 0, 0, "retraction"},
		{"content at a long streak", 250, 0, 1, 0, 0, "content"},
		{"retraction at a long streak", 250, 3, 0, 0, 0, "retraction"},
		{"provenance-only at a long streak", 250, 0, 0, 0, 16, "provenance"},
		{"unknown before the threshold", 1, 0, 0, 2, 0, "unknown"},
		{"unknown at the threshold", capabilityDivergenceErrorStreak, 0, 0, 2, 0, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metric, is := lensBlocked(t, lensBlockedSnap("myTasks", tc.streak,
				tc.retraction, tc.content, tc.unknown, tc.provenance, tc.worst,
				"stored watermark >= reconciliation token; "+tc.worst+" unrepairable"))

			// The classification crossed.
			if got := metric["blockedWorstClass"]; got != tc.worst {
				t.Fatalf("blockedWorstClass = %v, want %q", got, tc.worst)
			}
			byClass, ok := metric["blockedByClass"].(map[string]any)
			if !ok {
				t.Fatalf("blockedByClass = %v, want a per-class map", metric["blockedByClass"])
			}
			if len(byClass) == 0 {
				t.Fatalf("the census must cross to the business plane too: %v", byClass)
			}
			if !strings.Contains(is.Message, tc.worst) {
				t.Fatalf("the issue must name the governing class: %q", is.Message)
			}
			if got := metric["alert"]; got != "repair-blocked" {
				t.Fatalf("alert = %v, want repair-blocked", got)
			}

			// The ceiling held.
			if is.Severity != "warning" {
				t.Fatalf("severity = %q, want warning at every class and every streak length", is.Severity)
			}
			if s := aggregateStatus([]issueRecord{is}); s != "degraded" {
				t.Fatalf("status = %q, want degraded — a business lens degrades the instance, it never fails it", s)
			}
		})
	}
}

// TestBlockedSeverity_ThePlainCeilingHoldsWhereTheCapPathEscalates pins the cap
// as a CONTRAST rather than as an arbitrary constant: the identical blocked set
// is posed to both paths, and the two answers must differ in exactly one way.
//
// Why the plain path refuses to escalate. Severity here is not a statement about
// how bad the finding is — both planes agree a declined retraction leaves a row
// the graph says should not exist — it is a statement about blast radius. An
// `error` on any lens code takes the whole Refractor INSTANCE unhealthy, and the
// instance serving a business vertical's read model is the same instance serving
// the authorization read model. Escalating for a business lens would take the
// auth plane down with it, which is a strictly worse outcome than the wrong
// business row it was reporting. The classification is what an operator acts on
// there; the severity is what protects the plane next door.
//
// Fails without clampToWarning: the plain path returns `error` and the two
// planes become indistinguishable.
func TestBlockedSeverity_ThePlainCeilingHoldsWhereTheCapPathEscalates(t *testing.T) {
	cases := []struct {
		name                                     string
		streak                                   int
		retraction, content, unknown, provenance int
		worst                                    string
	}{
		{"a content divergence on the first pass", 1, 0, 1, 0, 0, "content"},
		{"a declined retraction on the first pass", 1, 1, 0, 0, 0, "retraction"},
		{"a content divergence at a long streak", 250, 0, 1, 0, 16, "content"},
		{"an unprovable class at the streak threshold", capabilityDivergenceErrorStreak, 0, 0, 4, 0, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := "stored watermark >= reconciliation token; " + tc.worst + " unrepairable"

			_, capIs := capBlocked(t, capBlockedSnap("capabilityRoles", tc.streak,
				tc.retraction, tc.content, tc.unknown, tc.provenance, tc.worst, reason))
			if capIs.Severity != "error" {
				t.Fatalf("capability severity = %q, want error — the class is what decides on the auth plane", capIs.Severity)
			}
			if s := aggregateStatus([]issueRecord{capIs}); s != "unhealthy" {
				t.Fatalf("capability status = %q, want unhealthy", s)
			}

			_, lensIs := lensBlocked(t, lensBlockedSnap("myTasks", tc.streak,
				tc.retraction, tc.content, tc.unknown, tc.provenance, tc.worst, reason))
			if lensIs.Severity != "warning" {
				t.Fatalf("business severity = %q, want warning — an error here takes the instance unhealthy "+
					"and the auth plane down with it, for one vertical's wrong row", lensIs.Severity)
			}
			if s := aggregateStatus([]issueRecord{lensIs}); s != "degraded" {
				t.Fatalf("business status = %q, want degraded", s)
			}

			// The finding itself is NOT downgraded with the severity: both planes
			// name the same class, or the cap would be silencing rather than
			// scoping.
			if !strings.Contains(lensIs.Message, tc.worst) {
				t.Fatalf("the capped issue must still name the class the cap path escalated on: %q", lensIs.Message)
			}
		})
	}
}

// TestBlockedSeverity_TheWorstLensDecidesTheIssue pins the aggregation across
// lenses: one issue code is raised for the whole set, so a later lens's warning
// must never overwrite an earlier lens's error. Map iteration order is not
// stable, so both orders are posed.
func TestBlockedSeverity_TheWorstLensDecidesTheIssue(t *testing.T) {
	benign := capBlockedSnap("capabilityRoles", 250, 0, 0, 0, 16, "provenance", "provenance-only divergence unrepairable")
	finding := capBlockedSnap("capabilityEphemeral", 1, 0, 1, 0, 0, "content", "content divergence unrepairable")

	for _, order := range [][]CapabilityLensStatus{{benign, finding}, {finding, benign}} {
		snaps := order
		h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus { return snaps }}
		_, issues := h.evalCapabilityLenses(time.Now())
		is, ok := issueByCode(issues, issueCapabilityRepairBlocked)
		if !ok {
			t.Fatalf("expected %s, got %v", issueCapabilityRepairBlocked, issues)
		}
		if is.Severity != "error" {
			t.Fatalf("severity = %q, want error — the issue describes its worst member", is.Severity)
		}
	}
}

// TestBlockedMetrics_AreAbsentWhenNothingIsBlocked pins the quiet case: a lens
// with no blocked rows publishes an empty census and no worst class, rather than
// a class name nothing holds.
func TestBlockedMetrics_AreAbsentWhenNothingIsBlocked(t *testing.T) {
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{{
			CanonicalName: "capabilityRoles", RuleID: "cap-roles",
			Status: "active", SweepInterval: time.Minute, SweepLastPassAt: time.Now(),
		}}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	if _, raised := issueByCode(issues, issueCapabilityRepairBlocked); raised {
		t.Fatalf("nothing is blocked, so nothing may be raised: %v", issues)
	}
	byClass, ok := metric["capabilityRoles"]["blockedByClass"].(map[string]any)
	if !ok {
		t.Fatalf("blockedByClass = %v, want a per-class map even when empty", metric["capabilityRoles"]["blockedByClass"])
	}
	if len(byClass) != 0 {
		t.Fatalf("blockedByClass = %v, want empty", byClass)
	}
	if got, present := metric["capabilityRoles"]["blockedWorstClass"]; present {
		t.Fatalf("blockedWorstClass = %v, want absent when nothing is blocked", got)
	}
}
