package weaver

import (
	"strconv"
	"strings"
	"testing"
)

// TestListingRank_EveryIssueFamilyIsClassified pins that every issue-key family
// the engine raises is named in exactly one of the two listing classes. The
// classification decides which entries survive the heartbeat's 50-entry cut, and
// an unnamed family gets the bounded tier by default — safe, but silently, so a
// family added without a decision would never be noticed. This fails until it is
// named.
func TestListingRank_EveryIssueFamilyIsClassified(t *testing.T) {
	all := []string{
		issuePrefixGapEntity,
		issuePrefixGapConfig,
		issuePrefixGapOpen,
		issuePrefixData,
		issuePrefixTemplate,
		issuePrefixSweep,
		issuePrefixConsumer,
		issuePrefixTarget,
		issuePrefixTimer,
		issuePrefixPending,
		issuePrefixOscillate,
	}
	seen := map[string]int{}
	for _, p := range perEntityIssuePrefixes {
		seen[p]++
	}
	for _, p := range targetScopedIssuePrefixes {
		seen[p]++
	}
	for _, p := range all {
		switch seen[p] {
		case 1:
		case 0:
			t.Errorf("issue family %q is in neither perEntityIssuePrefixes nor targetScopedIssuePrefixes — "+
				"classify it: does its population grow with the target's ROW COUNT, or with the number of targets?", p)
		default:
			t.Errorf("issue family %q is in BOTH listing classes", p)
		}
	}
	for p := range seen {
		found := false
		for _, q := range all {
			if p == q {
				found = true
			}
		}
		if !found {
			t.Errorf("listing class names %q, which is not a known issue family", p)
		}
	}
}

// TestBoundIssues_ConfigFaultSurvivesItsOwnPerRowFlood is the acceptance test
// for the family ranking. A broken playbook raises ONE target-scoped
// GapWithoutPlaybook and, on the same target, one per-row RowDataError for every
// malformed entity. Both are `warning` — the config codes were demoted
// deliberately, since a package-authoring typo degrades Weaver rather than
// making it unable to fulfil its responsibility.
//
// Ranked on severity alone the two are indistinguishable and the cut falls back
// to key order, where `data:` sorts ahead of `gapConfig:` — so the one entry
// naming the CAUSE loses its place to sixty entries counting the effect, and the
// operator reads a document full of identical per-row warnings that never says
// why. The mutation this pins is the whole listingRank per-entity tier: without
// it, this test finds no config entry in the listing.
func TestBoundIssues_ConfigFaultSurvivesItsOwnPerRowFlood(t *testing.T) {
	const target = "brokenTarget"
	var in []healthIssue
	for i := 0; i < 60; i++ {
		id := strconv.Itoa(i)
		in = append(in, healthIssue{
			Severity: "warning",
			Code:     "RowDataError",
			Message:  "row " + id + " column missing_x is not a bool",
			Since:    "2026-08-29T00:00:00Z",
			key:      issueKeyDataEntity(target, "entity"+id, "missing_x"),
		})
	}
	cause := healthIssue{
		Severity: "warning",
		Code:     "GapWithoutPlaybook",
		Message:  "target " + target + ": row column missing_x is true but the playbook defines no gaps entry for it",
		Since:    "2026-08-29T00:00:00Z",
		key:      issueKeyGapConfig(target, "missing_x"),
	}
	// Placed where key order alone would bury it: `data:` < `gapConfig:`, so
	// every per-row entry above sorts ahead of this one.
	in = append(in, cause)

	got := boundIssues(in, maxHeartbeatIssues)

	listed := false
	for _, is := range got {
		if is.Code == "GapWithoutPlaybook" {
			listed = true
			break
		}
	}
	if !listed {
		t.Fatalf("the config fault explaining the flood was truncated out of the heartbeat; "+
			"listed codes: %s", codesOf(got))
	}

	// And the truncation entry must still be honest about what it dropped.
	last := got[len(got)-1]
	if last.Code != issuesTruncatedCode {
		t.Fatalf("expected a truncation entry last, got %q", last.Code)
	}
	if !strings.Contains(last.Message, "RowDataError") {
		t.Errorf("truncation entry should name the omitted per-row code, got %q", last.Message)
	}
}

// TestBoundIssues_SurfaceBacklogEntrySurvivesAPerRowFlood is the same acceptance
// for the `gapOpen:` family, and it is the reason that family is classified
// target-scoped rather than left to default.
//
// One entry says "this target has N rows of work standing open on this column";
// the per-row faults beside it each say "this one row is broken". Key order puts
// `data:` ahead of `gapOpen:`, so ranked together the one entry that explains a
// backlog loses the cut to sixty that merely count faults — and the entry an
// operator opens the page for is the one truncated away.
func TestBoundIssues_SurfaceBacklogEntrySurvivesAPerRowFlood(t *testing.T) {
	t.Parallel()
	const target = "unroutedTasks"
	const col = "missing_claim"

	if rank := listingRank(healthIssue{Severity: "warning", Code: "UnroutedTasks", key: issueKeyGapOpen(target, col)}); rank != 2 {
		t.Fatalf("listingRank for a gapOpen: entry = %d, want 2 (target-scoped): its population is gap "+
			"COLUMNS, not rows, and it explains the flood rather than counting it", rank)
	}

	var in []healthIssue
	for i := 0; i < 60; i++ {
		id := strconv.Itoa(i)
		in = append(in, healthIssue{
			Severity: "warning",
			Code:     "RowDataError",
			Message:  "row " + id + " column missing_x is not a bool",
			Since:    "2026-09-04T00:00:00Z",
			key:      issueKeyDataEntity(target, "entity"+id, "missing_x"),
		})
	}
	// Placed where key order alone would bury it: `data:` < `gapOpen:`.
	in = append(in, healthIssue{
		Severity: "warning",
		Code:     "UnroutedTasks",
		Message:  "target " + target + ": 137 rows have column " + col + " true",
		Since:    "2026-09-04T00:00:00Z",
		key:      issueKeyGapOpen(target, col),
	})

	got := boundIssues(in, maxHeartbeatIssues)

	for _, is := range got {
		if is.Code == "UnroutedTasks" {
			return
		}
	}
	t.Fatalf("the target's open-workload entry was truncated out of the heartbeat; listed codes: %s", codesOf(got))
}

func codesOf(issues []healthIssue) string {
	var b strings.Builder
	for i, is := range issues {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(is.Code)
	}
	return b.String()
}
