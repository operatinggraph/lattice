package weaver

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// The issue-cache bound (design weaver-decline-retry-substrate-native-design.md
// §3.6). Two of Weaver's issue families carry an ENTITY segment — the per-row
// `data:` and `template:` errors — so both are O(rows of the target): a
// systemically broken lens with a hundred thousand rows would grow the in-memory
// map, and the per-heartbeat sort over it, without limit.
//
// The bound is on the CACHE, which is a different job from boundIssues' bound on
// the heartbeat DOCUMENT. The document cap decides what one heartbeat lists; this
// one decides what the map can hold. Both are asserted here so the two are not
// conflated.

// fillRowIssues raises n per-row issues for one target under distinct entity
// segments, returning the keys it used.
func fillRowIssues(c *issueCache, targetID string, n int) []string {
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		key := issueKeyDataEntity(targetID, "entity"+strconv.Itoa(i), "violating")
		c.set(key, "warning", "RowDataError", "row "+strconv.Itoa(i)+" is malformed")
		keys = append(keys, key)
	}
	return keys
}

// cappedEntry returns the synthetic per-target overflow entry from a snapshot.
func cappedEntry(issues []healthIssue, targetID string) (healthIssue, bool) {
	for _, is := range issues {
		if is.Code == issueCacheCappedCode && strings.Contains(is.Message, "target "+targetID+":") {
			return is, true
		}
	}
	return healthIssue{}, false
}

// TestIssueCache_PerRowFamiliesAreCappedPerTarget pins the refusal itself: past
// the cap the map stops growing, the entries already standing are untouched
// (they are facts with an age, and evicting one to admit an identical newer one
// would restamp the fault as young), and the refusals are audible as one
// synthetic entry per target rather than as silence.
func TestIssueCache_PerRowFamiliesAreCappedPerTarget(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const targetID = "capTarget"

	keys := fillRowIssues(c, targetID, maxPerTargetRowIssues)
	if got := len(c.issues); got != maxPerTargetRowIssues {
		t.Fatalf("cache holds %d entries, want the full cap of %d", got, maxPerTargetRowIssues)
	}
	if _, capped := cappedEntry(c.snapshot(), targetID); capped {
		t.Fatalf("a target exactly AT the cap has refused nothing and must raise no overflow entry")
	}

	// Past the cap: refused, and the map does not grow.
	overflowKey := issueKeyTemplateEntity(targetID, "entityOverflow", "missing_x")
	c.set(overflowKey, "warning", "TemplateDataError", "one row too many")
	if got := len(c.issues); got != maxPerTargetRowIssues {
		t.Fatalf("cache grew to %d past its %d cap: the per-row families are O(rows), so an "+
			"unbounded map is what a systemically broken lens fills", got, maxPerTargetRowIssues)
	}
	if _, ok := issueAt(c, overflowKey); ok {
		t.Fatalf("the refused raise must not be stored")
	}
	if _, ok := issueAt(c, keys[0]); !ok {
		t.Fatalf("a standing entry must never be evicted to admit a newer one — its `since` is the " +
			"age of a fault, and re-minting it would report a week-old fault as seconds old")
	}

	ov, capped := cappedEntry(c.snapshot(), targetID)
	if !capped {
		t.Fatalf("a refused raise must be audible as a per-target overflow entry, snapshot = %d entries",
			len(c.snapshot()))
	}
	if ov.Severity != "warning" {
		t.Fatalf("overflow severity = %q, want warning (the families it stands in for are all warnings)",
			ov.Severity)
	}
	if ov.Since == "" {
		t.Fatalf("the overflow entry must carry the instant the cap engaged")
	}
	if !strings.Contains(ov.Message, "1 further") {
		t.Fatalf("the overflow entry must name how many raises it stands in for, got %q", ov.Message)
	}

	// Maintained IN PLACE: more refusals bump the one entry rather than minting
	// entries of their own.
	for i := 0; i < 5; i++ {
		c.set(issueKeyDataEntity(targetID, "extra"+strconv.Itoa(i), "violating"), "warning", "RowDataError", "more")
	}
	snap := c.snapshot()
	if got := len(snap); got != maxPerTargetRowIssues+1 {
		t.Fatalf("snapshot carries %d entries, want the cap plus exactly one overflow entry", got)
	}
	ov, _ = cappedEntry(snap, targetID)
	if !strings.Contains(ov.Message, "6 further") {
		t.Fatalf("the overflow entry must count every refused raise, got %q", ov.Message)
	}
}

// TestIssueCache_CapRefreshesAndReleases pins the cap's arithmetic at the two
// points a count can go wrong: a REFRESH of a standing key is not growth and must
// never be refused, and the overflow record must survive the population moving
// under and back over the cap while refusals keep arriving.
//
// The record's retirement is LEVEL-driven, read at the snapshot, not edge-driven
// on the count. A live population turns entries over constantly — a repaired row,
// an entity tombstone — and each turnover frees a slot the next refusal retakes
// at once. Retiring on the count dipping under the cap therefore destroys the
// record on every turnover and re-mints it moments later with `since = now` and
// `refused = 1`, so a suppression running for days reads as one raise, seconds
// old. The record is the same kind of thing as the entries it stands for: a fact
// with an age.
func TestIssueCache_CapRefreshesAndReleases(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const targetID = "capRelease"

	keys := fillRowIssues(c, targetID, maxPerTargetRowIssues)

	// A refresh at a standing key: the message changes, the population does not.
	c.set(keys[0], "warning", "RowDataError", "the same row, a newer message")
	if got := len(c.issues); got != maxPerTargetRowIssues {
		t.Fatalf("a refresh counted as growth: cache holds %d, want %d", got, maxPerTargetRowIssues)
	}
	if is, _ := issueAt(c, keys[0]); is.Message != "the same row, a newer message" {
		t.Fatalf("a refresh at a standing key must land, got %q", is.Message)
	}

	refused := issueKeyDataEntity(targetID, "entityRefused", "violating")
	c.set(refused, "warning", "RowDataError", "refused")
	first, capped := cappedEntry(c.snapshot(), targetID)
	if !capped {
		t.Fatalf("setup: the target must be standing at its cap")
	}
	if !strings.Contains(first.Message, "1 further") {
		t.Fatalf("setup: the record must count the one refusal so far, got %q", first.Message)
	}

	// A full turnover cycle: one entry clears (under the cap), the freed slot is
	// retaken, and the next raise is refused again. Throughout, the suppression
	// never stopped — so the record must be the SAME record, keeping its arrival
	// stamp and carrying its count forward.
	c.clear(keys[0])
	c.set(refused, "warning", "RowDataError", "admitted into the freed slot")
	if _, ok := issueAt(c, refused); !ok {
		t.Fatalf("a raise must be admitted again once the population is under the cap")
	}
	c.set(issueKeyDataEntity(targetID, "entityRefusedAgain", "violating"), "warning", "RowDataError", "refused again")

	second, capped := cappedEntry(c.snapshot(), targetID)
	if !capped {
		t.Fatalf("the record must survive a turnover that never interrupted the suppression — a refusal " +
			"arrived on the other side of it")
	}
	if second.Since != first.Since {
		t.Fatalf("since moved %q -> %q across one entry turning over: the record was destroyed and "+
			"re-minted, so a suppression running for days reports as seconds old",
			first.Since, second.Since)
	}
	if !strings.Contains(second.Message, "2 further") {
		t.Fatalf("the refused count must carry forward across the turnover (1 then 2), got %q", second.Message)
	}

	// The suppression genuinely ends: room again, and a whole snapshot interval
	// passes with nothing refused. THAT is what retires the record.
	c.clear(keys[1])
	if _, capped := cappedEntry(c.snapshot(), targetID); capped {
		t.Fatalf("a snapshot that observes no refusal since the previous one, for a target back under " +
			"its cap, must retire the record — the refusals are level-driven and would re-arrive")
	}

	// And a later refusal mints a FRESH record, dated from then: this suppression
	// is a new one, not the old one resumed. One raise refills the freed slot;
	// the next is the refusal.
	c.set(issueKeyDataEntity(targetID, "entityRefill", "violating"), "warning", "RowDataError", "back to the cap")
	c.set(issueKeyDataEntity(targetID, "entityLater", "violating"), "warning", "RowDataError", "refused later")
	third, capped := cappedEntry(c.snapshot(), targetID)
	if !capped {
		t.Fatalf("a refusal after the record retired must mint a new one")
	}
	if third.Since == first.Since || !strings.Contains(third.Message, "1 further") {
		t.Fatalf("a new suppression must start from its own arrival and its own count, got since=%q message=%q",
			third.Since, third.Message)
	}
}

// TestIssueCache_PaceMemoryIsCappedPerTarget pins the SAME bound on the pace
// memory, which the latch's bound does not reach.
//
// `clear` deliberately leaves a pace entry standing — a clear is not evidence the
// fault ended — so the paced map accumulates keys whose latch entries are long
// gone, and the family it serves per row (`template:`, a TemplateDataError per
// (target, entity, gap)) is O(rows of the target) exactly like the latch. Every
// heartbeat walks that map in full, so an unbounded paced map is an unbounded
// per-heartbeat walk. The refusal must also be QUIET: a target at its cap is
// already flooding, so the one thing this bound must never do is make a raise
// louder than it was.
func TestIssueCache_PaceMemoryIsCappedPerTarget(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const targetID = "pacedCap"
	now := time.Now()

	for i := 0; i < maxPerTargetRowIssues; i++ {
		key := issueKeyTemplateEntity(targetID, "entity"+strconv.Itoa(i), "missing_x")
		c.pacedRaise(key, "warning", "TemplateDataError", now)
		// The latch entries are cleared as fast as they are raised (a
		// re-projection, a gap closing) — the pace memory is what keeps growing.
		c.clear(key)
	}
	if got := len(c.paced); got != maxPerTargetRowIssues {
		t.Fatalf("pace memory holds %d entries, want the full cap of %d", got, maxPerTargetRowIssues)
	}

	overflow := issueKeyTemplateEntity(targetID, "entityOverflow", "missing_x")
	loud, _ := c.pacedRaise(overflow, "warning", "TemplateDataError", now)
	if got := len(c.paced); got != maxPerTargetRowIssues {
		t.Fatalf("pace memory grew to %d past its %d cap: every heartbeat walks this map in full",
			got, maxPerTargetRowIssues)
	}
	if loud {
		t.Fatalf("a refused pace raise must be QUIET — a target at its cap is already flooding, so the " +
			"failure direction of a bound on the log has to be fewer records, never more")
	}

	// The bounded families are never refused: a target-scoped config fault is one
	// entry per (target, gap), and it is the entry that explains the flood.
	if loud, _ := c.pacedRaise(issueKeyGapConfig(targetID, "missing_x"), "warning", "PlaybookConfigError", now); !loud {
		t.Fatalf("a target-scoped config fault is bounded by construction: its arrival must be loud " +
			"however many rows of the target are stuck")
	}

	// A second target is unaffected, and the count is released with the entries:
	// the target's teardown hands the whole allowance back.
	other := issueKeyTemplateEntity("pacedQuiet", "e1", "missing_x")
	if loud, _ := c.pacedRaise(other, "warning", "TemplateDataError", now); !loud {
		t.Fatalf("the cap is per target: one broken lens must not silence another target's diagnostics")
	}
	for _, prefix := range issueKeyTargetPrefixes(targetID) {
		c.clearPrefix(prefix)
	}
	if got := c.pacedRows[targetID]; got != 0 {
		t.Fatalf("the teardown left the pace count at %d, so a re-registered target would start refused", got)
	}
	if loud, _ := c.pacedRaise(overflow, "warning", "TemplateDataError", now); !loud {
		t.Fatalf("a target coming back must be admitted from an empty count")
	}
}

// TestIssueCache_CapIsPerTargetAndPerFamily pins the cap's scope. It counts the
// two per-row families TOGETHER for one target, because they share the same
// O(rows) growth; it counts each target separately, so one broken lens cannot
// starve another target's diagnostics; and it never counts the families that are
// bounded by construction (a target-scoped config fault, a consumer entry), which
// must always be admitted.
func TestIssueCache_CapIsPerTargetAndPerFamily(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const busy, quiet = "capBusy", "capQuiet"

	// Half the cap in each per-row family for the same target: together they fill it.
	for i := 0; i < maxPerTargetRowIssues/2; i++ {
		c.set(issueKeyDataEntity(busy, "d"+strconv.Itoa(i), "violating"), "warning", "RowDataError", "d")
		c.set(issueKeyTemplateEntity(busy, "t"+strconv.Itoa(i), "missing_x"), "warning", "TemplateDataError", "t")
	}
	c.set(issueKeyDataEntity(busy, "oneTooMany", "violating"), "warning", "RowDataError", "refused")
	if _, ok := issueAt(c, issueKeyDataEntity(busy, "oneTooMany", "violating")); ok {
		t.Fatalf("the two per-row families share one per-target cap: they have the same O(rows) growth")
	}

	// A second target is unaffected.
	quietKey := issueKeyDataEntity(quiet, "e1", "violating")
	c.set(quietKey, "warning", "RowDataError", "another target's row")
	if _, ok := issueAt(c, quietKey); !ok {
		t.Fatalf("the cap is per target: one broken lens must not starve another target's diagnostics")
	}

	// The bounded families are never refused, whatever the per-row population is.
	configKey := issueKeyGapConfig(busy, "missing_x")
	c.set(configKey, "warning", "PlaybookConfigError", "the fault an operator needs to see")
	if _, ok := issueAt(c, configKey); !ok {
		t.Fatalf("a target-scoped config fault is bounded by construction and must always be admitted — " +
			"it is the entry that EXPLAINS the flood the cap is holding back")
	}
	if _, ok := issueAt(c, issueKeyConsumer(busy)); ok {
		t.Fatalf("setup: no consumer entry was raised")
	}
	c.set(issueKeyConsumer(busy), "error", "ConsumerReconcileError", "the lane is down")
	if _, ok := issueAt(c, issueKeyConsumer(busy)); !ok {
		t.Fatalf("a consumer entry is one per target and must always be admitted")
	}
}

// TestIssueCache_PrefixTeardownReleasesTheCap pins the cap's subject-gone
// lifetime: the prefix clears that retire a deleted entity's entries and a
// revoked target's whole issue set must take the cap's bookkeeping with them, or
// a target that has left would go on refusing raises for a target that no longer
// exists.
func TestIssueCache_PrefixTeardownReleasesTheCap(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const targetID = "capTeardown"

	fillRowIssues(c, targetID, maxPerTargetRowIssues)
	c.set(issueKeyDataEntity(targetID, "refused", "violating"), "warning", "RowDataError", "refused")
	if _, capped := cappedEntry(c.snapshot(), targetID); !capped {
		t.Fatalf("setup: the target must be standing at its cap")
	}

	for _, prefix := range issueKeyTargetPrefixes(targetID) {
		c.clearPrefix(prefix)
	}
	if got := len(c.issues); got != 0 {
		t.Fatalf("the target teardown left %d entries behind", got)
	}
	if _, capped := cappedEntry(c.snapshot(), targetID); capped {
		t.Fatalf("a target that has left must take its overflow record with it")
	}
	if got := c.rowIssues[targetID]; got != 0 {
		t.Fatalf("the per-target row count survived the teardown at %d", got)
	}
	c.set(issueKeyDataEntity(targetID, "fresh", "violating"), "warning", "RowDataError", "a re-registered target")
	if _, ok := issueAt(c, issueKeyDataEntity(targetID, "fresh", "violating")); !ok {
		t.Fatalf("a target coming back must start from an empty count, not from the one it left at")
	}
}

// TestIssueCache_CapDoesNotDisplaceTheDocumentBound pins the two bounds' division
// of labour. maxPerTargetRowIssues bounds the map; boundIssues bounds what one
// heartbeat lists, severity-first, with its OWN overflow entry. A snapshot at the
// cache cap must still be cut by the document cap, and the error that explains
// the flood must still be listed ahead of the warnings.
func TestIssueCache_CapDoesNotDisplaceTheDocumentBound(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const targetID = "capVsDoc"

	fillRowIssues(c, targetID, maxPerTargetRowIssues)
	c.set(issueKeyConsumer(targetID), "error", "ConsumerReconcileError", "the lane is down")

	snap := c.snapshot()
	if len(snap) <= maxHeartbeatIssues {
		t.Fatalf("setup: the cache snapshot (%d) must exceed the document cap (%d) for this to test "+
			"anything", len(snap), maxHeartbeatIssues)
	}
	doc := boundIssues(snap, maxHeartbeatIssues)
	if len(doc) != maxHeartbeatIssues+1 {
		t.Fatalf("the document cap must still cut the listing: %d entries listed", len(doc))
	}
	if doc[0].Code != "ConsumerReconcileError" {
		t.Fatalf("the document cut is severity-first, so the error that explains the flood leads: got %q",
			doc[0].Code)
	}
	if !hasIssueCode(doc, issuesTruncatedCode) {
		t.Fatalf("the document's own truncation entry must name what it did not list")
	}
}
