package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// DefaultAuditInterval and DefaultAuditBatch bound the plain-lens divergence
// audit (lens-projection-divergence-audit-design.md §6.3). The audit is a
// BACKGROUND truth check, not a latency detector — the liveness plane is the
// fast signal and keeps its seconds-scale cadence — so the clock is slow rather
// than the batch large: the anchor-key listing costs a full anchor-type
// enumeration per tick whatever the batch is (substrate's filtered listing pages
// client-side), while the batch bounds the seeded evaluations and target reads
// that follow it.
//
// AuditEnabledByDefault is the corpus-wide arming switch. Flipping it to false
// makes auditEnrolment refuse every lens with reason "disabled by deployment",
// which is a published refusal like any other — never a silent absence, and
// never a lens that reads as audited-clean because nothing audited it. It is
// deliberately NOT a zero batch: a zero AuditPlan.Batch resolves back to the
// default and would disable nothing.
const (
	DefaultAuditInterval  = 15 * time.Minute
	DefaultAuditBatch     = 10
	AuditEnabledByDefault = true
)

// auditArmed is the live reading of AuditEnabledByDefault. It is a var rather
// than the constant itself so the refusal it produces is exercisable: a
// published refusal nobody has ever seen produced is a claim, not a mechanism.
var auditArmed = AuditEnabledByDefault

// The divergence classes an audit pass can report, per §4.3 step 2. They are
// carried as a MAP keyed by these names rather than as three counters so a class
// that never fires reads as ABSENT rather than as zero — the difference between
// a direction with nothing to find and a direction that has silently stopped
// detecting.
const (
	// AuditClassMissing is a row the recomputed projection produces and the
	// target does not hold.
	AuditClassMissing = "missing"
	// AuditClassStale is a row the target holds whose content no longer equals
	// the recomputed one.
	AuditClassStale = "stale"
	// AuditClassRetained is a row the target still holds for an anchor that no
	// longer projects it — because its match stopped matching, or because the
	// anchor was tombstoned and the retraction was lost.
	AuditClassRetained = "retained"
)

// AuditCoverageBasisKeyType names how the audit enumerates a lens's anchors,
// and it is published on every pass because it is a real coverage boundary
// rather than an implementation note. The enumeration is a subject filter over
// `vtx.<label>.*`, i.e. by KEY TYPE — but the executor also admits a vertex
// whose BODY `class`/`label` equals the pattern label (executor.nodeMatches), and
// such an anchor is never enumerated here and never audited. The consequence is
// under-coverage, never a wrong verdict; publishing the basis is what keeps
// "audited clean" readable as the bounded claim it is.
const AuditCoverageBasisKeyType = "key-type"

// auditReasonUndrivableKey is the reason a tombstoned anchor contributes an
// unverified verdict: without a read-free derivation of the row key it owned,
// the audit cannot ask the target whether that row is still there.
const auditReasonUndrivableKey = "tombstoned anchor whose row key is not derivable"

// AuditPlan is the per-lens data the divergence audit needs. Installing a plan
// is what opts a pipeline into auditing, and only auditEnrolment's six
// fail-closed conjuncts produce one.
type AuditPlan struct {
	// AnchorLabel is the single vertex type the lens's seedable anchor pattern
	// binds — the key type whose Core KV population the audit walks. Singular
	// by enrolment: a taxonomy-expanded anchor resolves to a SET of concrete
	// subtypes, which one key-type listing cannot enumerate, and a multi-walk
	// lens has no single anchor at all.
	AnchorLabel string
	// Interval and Batch override the audit defaults; zero selects them.
	Interval time.Duration
	Batch    int
}

// AuditStatus is the auditor's snapshot for the heartbeat and for cursor
// persistence.
//
// Every counter below describes the LAST PASS, and the pass covers at most
// Batch anchors of a cycle that takes ⌈anchors/Batch⌉ ticks to complete. That is
// why CycleCompletedAt and ListingSize travel beside them: a reader of
// DivergentTotal == 0 must be able to see whether that covers the whole lens or
// its last ten anchors. A verdict whose coverage is unstated is the same class
// of dishonesty the audit exists to remove.
type AuditStatus struct {
	// Enrolled is the install-time verdict, and Refusal the reason behind a
	// false one. A refused lens runs no pass, publishes no verdict, and can
	// never read as audit-stalled — "not audited" must be distinguishable from
	// "audited, clean" at every layer.
	Enrolled bool
	Refusal  string
	// Audited counts ANCHORS the last pass reached a conclusion about; the
	// Divergent map and DivergentTotal count ROWS. An anchor that reached no
	// conclusion is counted in Unverified and in neither of the other two.
	Audited int
	// Divergent holds only the classes that actually fired. DivergentTotal is
	// its sum — the single number the alert and the operator's first glance
	// both key on.
	Divergent      map[string]int
	DivergentTotal int
	// Unverified is how many anchors the last pass could conclude nothing
	// about, and LastUnverified the governing reason, so the issue an operator
	// reads names a cause rather than a count.
	Unverified     int
	LastUnverified string
	// Cursor is the round-robin position: the last anchor key the pass
	// examined, or "" once the cycle has been completed and reset.
	// CycleCompletedAt is when the walk last reached the end of the anchor
	// listing — the only field that says what a clean verdict is worth.
	Cursor           string
	CycleCompletedAt time.Time
	// ListingSize is how many anchor keys the type filter matched this pass, so
	// a pathologically large anchor type is visible rather than merely
	// expensive. CoverageBasis is always AuditCoverageBasisKeyType.
	ListingSize   int
	CoverageBasis string
	// LastPassAt is when a pass last reached a verdict — never a suppressed
	// one. It is the audit's liveness clock: every counter above describes the
	// last pass that ran, so an audit that stops running keeps publishing its
	// final verdict forever, and only this timestamp says the verdict is old.
	LastPassAt time.Time
	// Suppression names why the most recent tick verified nothing ("" when it
	// ran), and SuppressionAt when that was recorded. The timestamp is what
	// makes the reason trustworthy: it describes the LAST tick, so a tick that
	// never returns leaves the previous tick's reason standing, and a reader
	// with no clock would take a wedged audit for a merely-suppressed one.
	Suppression   string
	SuppressionAt time.Time
}

// Auditor is one plain lens's periodic correctness verdict: it re-runs the
// lens's own seeded evaluation against a bounded page of its anchors and
// compares the result with what is stored.
//
// It NEVER writes to the target. That is not an omission to be filled in later
// — it is the design (§8.1). A plain lens's target is unguarded, so a repair
// write would be unconditional last-writer-wins with no ordering token keeping
// it subordinate to a racing CDC event; the lenses share buckets, so a repair
// derived from a seeded evaluation would write into a keyspace the lens cannot
// prove it owns; and the failure this whole mechanism exists to end is a REPAIR
// path that concealed a detection gap. Detection must be able to stand alone or
// it is not detection. The remediation path is the operator's existing
// control-plane reproject RPC and Rebuild.
//
// The comparison is pipeline.rowsEquivalent — the same definition of "same row"
// the sweep and Reproject use, canonical JSON with the volatile envelope fields
// stripped — never a second one.
type Auditor struct {
	p    *Pipeline
	plan AuditPlan

	interval time.Duration
	batch    int

	mu     sync.Mutex
	status AuditStatus
}

// newAuditor builds the auditor for an installed plan, applying the defaults.
func newAuditor(p *Pipeline, plan AuditPlan) *Auditor {
	iv := plan.Interval
	if iv <= 0 {
		iv = DefaultAuditInterval
	}
	batch := plan.Batch
	if batch <= 0 {
		batch = DefaultAuditBatch
	}
	return &Auditor{
		p:        p,
		plan:     plan,
		interval: iv,
		batch:    batch,
		status:   AuditStatus{Enrolled: true, CoverageBasis: AuditCoverageBasisKeyType},
	}
}

// newRefusedAuditor builds the auditor a refused lens carries: it runs no pass
// and holds no cadence, and exists only so the refusal is PUBLISHED per lens
// instead of being a line in the activation log. A lens quietly running without
// its only correctness detector reads exactly like one whose audit keeps finding
// nothing.
func newRefusedAuditor(p *Pipeline, refusal string) *Auditor {
	return &Auditor{p: p, status: AuditStatus{Enrolled: false, Refusal: refusal}}
}

// Status returns the current audit snapshot. Thread-safe; read by the
// heartbeat's lens providers every beat.
//
// The Divergent map inside the snapshot is built fresh by each pass and
// replaced wholesale under the lock, never mutated after publication, so a
// caller holding an earlier snapshot keeps a consistent map rather than one
// changing underneath it.
func (a *Auditor) Status() AuditStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// Interval is the audit's tick period after defaults are applied, and zero for
// a refused lens. The heartbeat reads it to scale its staleness window off the
// audit's own cadence instead of a second, independently-tuned constant — and
// reads the zero as "no cadence to be late against", which is what stops a
// refused lens from ever reporting as audit-stalled.
func (a *Auditor) Interval() time.Duration { return a.interval }

// AnchorLabel is the vertex type this audit walks, and "" for a refused lens.
func (a *Auditor) AnchorLabel() string { return a.plan.AnchorLabel }

// SetAuditPlan installs the divergence-audit plan for this pipeline. Only
// auditEnrolment's conjuncts may produce a plan; InstallAudit is the production
// entry point that runs them. Must be called before RunAudit.
func (p *Pipeline) SetAuditPlan(plan AuditPlan) {
	p.auditor = newAuditor(p, plan)
}

// Auditor returns this pipeline's divergence auditor, or nil when InstallAudit
// has never run. A REFUSED lens returns a non-nil auditor whose status carries
// Enrolled=false and the reason — the refusal is a published verdict, not an
// absence, so it must survive as an object the provider can read.
func (p *Pipeline) Auditor() *Auditor { return p.auditor }

// InstallAudit evaluates the enrolment conjuncts against this pipeline as it
// now stands and installs an auditor either way: enrolled with a plan, or
// refused with the reason. It must be called after every component the
// conjuncts read is installed (the envelope/enumerator switch, SetDiffRetraction,
// SetSecureDecryptor, the compiled rule), because a conjunct evaluated against a
// half-built pipeline reads as satisfied for a lens that must never audit.
func (p *Pipeline) InstallAudit() (enrolled bool, refusal string) {
	plan, refusal := auditEnrolment(p)
	if refusal != "" {
		p.auditor = newRefusedAuditor(p, refusal)
		return false, refusal
	}
	p.SetAuditPlan(plan)
	return true, ""
}

// RunAudit runs the divergence audit until ctx is cancelled. It returns
// immediately for a pipeline with no auditor or a refused one, so the caller can
// start it unconditionally beside Run.
//
// The cursor and the last cycle completion are restored from the lens's existing
// Health KV entry before the first tick, so a redeploy resumes the walk instead
// of re-walking the head forever — which, on a cell that restarts more often
// than a cycle completes, would mean the tail of every lens is never audited at
// all while the verdict reads clean.
func (p *Pipeline) RunAudit(ctx context.Context) {
	a := p.auditor
	if a == nil || !a.status.Enrolled {
		return
	}
	a.restore(ctx)

	// Every lens's auditor starts inside the same activation loop, so without a
	// stagger they all tick together: one instant per interval in which the
	// whole enrolled set enumerates its anchors and evaluates a batch each. The
	// offset is derived from the lens ID rather than drawn randomly so a given
	// lens keeps its slot across restarts and a test can predict it.
	select {
	case <-ctx.Done():
		return
	case <-time.After(a.startOffset()):
	}

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pass(ctx)
		}
	}
}

// startOffset spreads this lens's ticks across the interval, in [0, interval).
func (a *Auditor) startOffset() time.Duration {
	h := fnv.New64a()
	_, _ = h.Write([]byte(a.p.ruleID))
	return time.Duration(h.Sum64() % uint64(a.interval))
}

// restore seeds the in-memory cursor from Health KV so a restarted process
// resumes its round-robin position and keeps the coverage claim its last
// completed cycle earned.
func (a *Auditor) restore(ctx context.Context) {
	if a.p.reporter == nil {
		return
	}
	entry, err := a.p.reporter.GetStatus(ctx)
	if err != nil {
		slog.Warn("pipeline: audit: could not restore cursor; walking from the start",
			"ruleId", a.p.ruleID, "err", err)
		return
	}
	a.mu.Lock()
	a.status.Cursor = entry.AuditCursor
	if entry.AuditCycleCompletedAt != "" {
		if at, perr := time.Parse(time.RFC3339, entry.AuditCycleCompletedAt); perr == nil {
			a.status.CycleCompletedAt = at
		}
	}
	a.mu.Unlock()
}

// suppressed reports why this tick must be skipped, or "" to run it.
//
// The first check is the ENROLMENT RE-CHECK, and it is the reason this is not a
// copy of the sweep's suppression. Every conjunct auditEnrolment reads is a
// mutable pipeline field, and a lens hot-reload or an adapter swap can move any
// of them under an installed plan; an auditor that trusted its install-time plan
// would keep auditing under a shape the lens no longer has. This is the same
// posture RequireGuardedAdapter takes — the requirement outlives this adapter
// instance — and it costs a few field reads per tick.
//
// A hot-reload that moves the ANCHOR LABEL is included: the persisted cursor is
// a key inside the old label's keyspace and means nothing in the new one, so the
// audit holds (fail-closed, never a wrong verdict) rather than walking a
// keyspace its cursor does not address.
//
// The remaining two mirror the sweep verbatim: a rebuild is a superset of the
// audit (truncate + full rescan), and a paused pipeline is operator intent the
// audit must not quietly speak over. The reason is returned rather than a bare
// bool because every cause is held by something external to the audit, so
// "skipped" is not a transient the next tick clears and the operator needs the
// cause.
func (a *Auditor) suppressed(ctx context.Context) string {
	plan, refusal := auditEnrolment(a.p)
	switch {
	case refusal != "":
		return "enrolment no longer holds: " + refusal
	case plan.AnchorLabel != a.plan.AnchorLabel:
		return "anchor label changed from " + a.plan.AnchorLabel + " to " + plan.AnchorLabel
	}
	if a.p.RebuildInFlight() {
		return "rebuild in flight"
	}
	if a.p.reporter == nil {
		return ""
	}
	entry, err := a.p.reporter.GetStatus(ctx)
	if err != nil {
		// Fail closed: an unreadable health entry means the pause state is
		// unknown, and skipping one tick costs a slice of coverage where
		// auditing through an operator pause publishes a verdict over a read
		// model the operator deliberately froze.
		return "lens status unreadable: " + err.Error()
	}
	if entry.Status != "active" {
		return "lens status is " + entry.Status
	}
	return ""
}

// pass runs one bounded audit tick: page the anchor listing from the persisted
// cursor, re-derive each anchor's rows, compare them against what is stored, and
// publish the verdict. Nothing in it writes to the target.
func (a *Auditor) pass(ctx context.Context) {
	if reason := a.suppressed(ctx); reason != "" {
		a.noteSuppressed(reason)
		return
	}
	a.noteSuppressed("")

	rs := a.p.ruleState()
	reader, canRead := a.p.currentAdapter().(adapter.RowReader)
	if rs.engine == nil || !canRead {
		// Unreachable: the enrolment re-check above proved both. Reported as a
		// suppression rather than assumed away, because the alternative is a
		// pass that publishes a clean verdict having compared nothing.
		a.noteSuppressed("pipeline cannot evaluate or read back its own rows")
		return
	}

	a.mu.Lock()
	cursor := a.status.Cursor
	a.mu.Unlock()

	page, listingSize, next, err := a.page(ctx, cursor)
	if err != nil {
		// A pass that could not enumerate its anchors verified nothing, so it
		// must not stamp the liveness clock or publish a verdict — the same
		// disposition a suppressed tick takes, for the same reason.
		slog.Warn("pipeline: audit: anchor listing failed; retrying next tick",
			"ruleId", a.p.ruleID, "anchorLabel", a.plan.AnchorLabel, "err", err)
		a.noteSuppressed("anchor listing failed: " + err.Error())
		return
	}

	var t auditTally
	for _, key := range page {
		a.auditAnchor(ctx, rs, reader, key, &t)
	}
	a.record(ctx, t, next, listingSize)
}

// page reads the anchor key listing and returns this tick's slice of it, the
// full matching count, and the cursor to resume from ("" once the walk has
// reached the end and the cycle is complete).
//
// The listing is taken WHOLE and paged here rather than through the substrate's
// own cursor argument, for two reasons that are the same reason: the substrate's
// filtered listing already collects the entire matching set from JetStream
// before it slices (KVListKeysFilter), so a server-side page would save nothing;
// and the matched set still has to be filtered down to well-formed roots of the
// anchor type, which a pre-sliced page cannot do without silently shrinking the
// batch. Paging here also yields the honest ListingSize the pass publishes —
// a server-side page reports only its own length.
func (a *Auditor) page(ctx context.Context, cursor string) (page []string, listingSize int, next string, err error) {
	filter := substrate.VertexPrefix + "." + a.plan.AnchorLabel + ".*"
	keys, _, err := a.p.coreKV.ListKeysFilter(ctx, filter, "", 0)
	if err != nil {
		return nil, 0, "", err
	}
	anchors := make([]string, 0, len(keys))
	for _, k := range keys {
		// The filter already excludes the four-segment aspect keys; this admits
		// only a well-formed root of the anchor type the plan names.
		vtxType, _, ok := substrate.ParseVertexKey(k)
		if !ok || vtxType != a.plan.AnchorLabel {
			continue
		}
		anchors = append(anchors, k)
	}
	sort.Strings(anchors)
	listingSize = len(anchors)

	start := 0
	if cursor != "" {
		start = sort.SearchStrings(anchors, cursor)
		for start < len(anchors) && anchors[start] <= cursor {
			start++
		}
	}
	if start >= len(anchors) {
		// The walk is past the end — either it finished, or anchors left the
		// corpus behind the cursor. Both mean this cycle is over.
		return nil, listingSize, "", nil
	}
	end := min(start+a.batch, len(anchors))
	page = anchors[start:end]
	if end < len(anchors) {
		next = page[len(page)-1]
	}
	return page, listingSize, next, nil
}

// auditTally accumulates one pass's verdicts.
type auditTally struct {
	audited        int
	unverified     int
	lastUnverified string
	divergent      map[string]int
}

// noteUnverified books an anchor the pass could conclude nothing about. It is
// counted as neither clean nor divergent: an audit that folded "I could not
// check this" into either would be the very collapse toward a confident verdict
// this mechanism exists to end.
func (t *auditTally) noteUnverified(reason string) {
	t.unverified++
	if t.lastUnverified == "" || reason < t.lastUnverified {
		// Lexicographically first, so the published text is stable across
		// passes rather than flapping with whichever anchor came last.
		t.lastUnverified = reason
	}
}

// noteDivergent books one divergent ROW under its class, creating the class's
// entry only when it fires.
func (t *auditTally) noteDivergent(class string) {
	if t.divergent == nil {
		t.divergent = map[string]int{}
	}
	t.divergent[class]++
}

// auditAnchor reaches one anchor's verdict. Every path either books the anchor
// as audited (with zero or more divergent rows) or books it as unverified, and
// an anchor whose check faults partway discards the classes it had already found
// — a partially-checked anchor is not a divergent one, it is an unknown one.
func (a *Auditor) auditAnchor(ctx context.Context, rs ruleState, reader adapter.RowReader, key string, t *auditTally) {
	props, live, err := a.anchorBody(ctx, key)
	if err != nil {
		t.noteUnverified("anchor vertex read failed: " + err.Error())
		return
	}

	var classes []string
	if !live {
		// A tombstoned anchor projects nothing, so the only question is whether
		// the row it owned is still there — the retraction the tombstone should
		// have produced may have been lost. The key is derived read-free from
		// the stored body, exactly as the CDC anchor-tombstone shortcut derives
		// it; without a body (a hard-absent key, i.e. one that left the corpus
		// between the listing and this read) there is nothing to derive from.
		if props == nil {
			t.noteUnverified(auditReasonUndrivableKey)
			return
		}
		keys, ok := rs.engine.AnchorDeleteResult(rs.cr, key, a.plan.AnchorLabel, props)
		if !ok {
			t.noteUnverified(auditReasonUndrivableKey)
			return
		}
		_, present, rerr := reader.GetRow(ctx, keys)
		if rerr != nil {
			t.noteUnverified("target row read failed: " + rerr.Error())
			return
		}
		if present {
			classes = append(classes, AuditClassRetained)
		}
		a.commit(t, classes)
		return
	}

	// The seeded evaluation: the same call the CDC path makes once seedAnchorFor
	// has proved the event is a mutation of this anchor, with the anchor key
	// supplied as the seed. It is what makes a per-anchor audit cost one
	// anchor's walk instead of the corpus-wide scan a full re-execute would be.
	results, err := a.p.executeFullForActor(ctx, rs, key, props, key)
	if err != nil {
		t.noteUnverified("evaluation failed: " + err.Error())
		return
	}

	// Should-exist: every row the recomputation produces must be present and
	// equal.
	for _, res := range results {
		if res.Delete {
			continue
		}
		stored, present, rerr := reader.GetRow(ctx, res.Keys)
		if rerr != nil {
			t.noteUnverified("target row read failed: " + rerr.Error())
			return
		}
		switch {
		case !present:
			classes = append(classes, AuditClassMissing)
		case !rowsEquivalent(stored, res.Row):
			classes = append(classes, AuditClassStale)
		}
	}

	// Should-not-exist: the row this anchor OWNS, when the recomputation no
	// longer produces it. The derivation is the same read-free one the CDC
	// filter-retraction check uses, and when it declines (a neighbor-keyed or
	// multi-row lens) the anchor is simply not checked in this direction —
	// exactly as the CDC path is not — which is why the class counts travel
	// per-class rather than as one number.
	if owned, ok := rs.engine.AnchorProjectionKey(rs.cr, key, a.plan.AnchorLabel, props); ok &&
		!resultsContainKeys(results, owned) {
		_, present, rerr := reader.GetRow(ctx, owned)
		if rerr != nil {
			t.noteUnverified("target row read failed: " + rerr.Error())
			return
		}
		if present {
			classes = append(classes, AuditClassRetained)
		}
	}
	a.commit(t, classes)
}

// commit books a fully-checked anchor: one audited anchor, plus whatever
// divergent rows it turned up.
func (a *Auditor) commit(t *auditTally, classes []string) {
	t.audited++
	for _, c := range classes {
		t.noteDivergent(c)
	}
}

// anchorBody point-reads an anchor vertex and reports its stored body together
// with whether it is live.
//
// It deliberately does NOT reuse fetchVertexProps, which collapses "absent" and
// "soft-deleted" into one nil answer. The audit needs them apart: a soft
// tombstone still carries the body the row-key derivation resolves against, and
// treating it as absent would make every tombstoned anchor unverified — turning
// the one direction that catches a LOST RETRACTION into noise.
func (a *Auditor) anchorBody(ctx context.Context, vtxKey string) (props map[string]any, live bool, err error) {
	entry, err := a.p.coreKV.Get(ctx, vtxKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if entry == nil || len(entry.Value) == 0 {
		return nil, false, nil
	}
	if jerr := json.Unmarshal(entry.Value, &props); jerr != nil {
		return nil, false, jerr
	}
	isDel, _ := props["isDeleted"].(bool)
	return props, !isDel, nil
}

// noteSuppressed records (or clears) the reason the last tick verified nothing.
// It deliberately does NOT touch LastPassAt: a suppressed tick reached no
// verdict, so the liveness clock must keep ageing — that ageing is the only
// thing that distinguishes an audit held indefinitely from one that is simply
// clean and quiet, since both publish identical counters.
func (a *Auditor) noteSuppressed(reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status.Suppression = truncateFailure(reason)
	if reason == "" {
		a.status.SuppressionAt = time.Time{}
		return
	}
	a.status.SuppressionAt = time.Now()
}

// record folds this pass's outcome into the status and persists the cursor +
// last cycle completion to the lens's Health KV entry — the audit's ONLY write
// anywhere, and it lands in the Health-KV plane the Refractor already owns
// (the architecture's operational-self-reporting exception), never in Core KV
// and never in the target.
func (a *Auditor) record(ctx context.Context, t auditTally, next string, listingSize int) {
	a.mu.Lock()
	a.status.Audited = t.audited
	a.status.Unverified = t.unverified
	a.status.LastUnverified = truncateFailure(t.lastUnverified)
	a.status.Divergent = t.divergent
	total := 0
	for _, n := range t.divergent {
		total += n
	}
	a.status.DivergentTotal = total
	a.status.ListingSize = listingSize
	a.status.CoverageBasis = AuditCoverageBasisKeyType
	a.status.Cursor = next
	if next == "" {
		// The walk reached the end of the listing: this cycle covered the whole
		// anchor type, which is the only thing that makes a clean verdict a
		// claim about the LENS rather than about ten of its anchors.
		a.status.CycleCompletedAt = time.Now()
	}
	a.status.LastPassAt = time.Now()
	snapshot := a.status
	a.mu.Unlock()

	if snapshot.DivergentTotal > 0 {
		slog.Warn("pipeline: audit: the projection disagrees with the graph",
			"ruleId", a.p.ruleID, "anchorLabel", a.plan.AnchorLabel,
			"divergentRows", snapshot.DivergentTotal, "classes", snapshot.Divergent,
			"audited", snapshot.Audited)
	}
	if a.p.reporter == nil {
		return
	}
	if err := a.p.reporter.SetAuditProgress(ctx, snapshot.Cursor, snapshot.CycleCompletedAt); err != nil {
		slog.Warn("pipeline: audit: could not persist cursor",
			"ruleId", a.p.ruleID, "err", err)
	}
}

// auditEnrolment decides whether a lens may be audited, returning its plan or a
// non-empty reason it may not be.
//
// Every conjunct is a correctness requirement read off an already-shipped
// predicate, not a heuristic, and every one is re-evaluated at the TOP OF EVERY
// PASS rather than only at install (see Auditor.suppressed) — they are all
// mutable pipeline state, and a plan cached at install would keep auditing under
// a shape the lens no longer has.
//
// The gate is fail-closed throughout: a refused lens publishes its refusal and
// raises no verdict, because "not audited" reading as "audited, clean" is the
// exact silence the audit exists to end.
func auditEnrolment(p *Pipeline) (AuditPlan, string) {
	if !auditArmed {
		return AuditPlan{}, "disabled by deployment"
	}

	rs := p.ruleState()
	if rs.engineKind != ruleengine.EngineFull || rs.engine == nil {
		return AuditPlan{}, "the audit's seeded evaluation is a full-engine mechanism and this lens does not run on it"
	}
	fullCR, isFull := rs.cr.(*full.CompiledRule)
	if !isFull || fullCR == nil {
		return AuditPlan{}, "its compiled rule is not a full-engine rule, so its anchor and parameters cannot be derived"
	}

	// A single-branch, derivable anchor pattern. seedAnchorLabels is EMPTY for a
	// multi-walk lens — branch merging evaluates N independent queries, each
	// with its own anchor, and one seed cannot speak for all of them — and holds
	// the resolved SUBTYPE SET for a `*` taxonomy anchor, which one key-type
	// listing cannot enumerate.
	switch len(rs.seedAnchorLabels) {
	case 1:
	case 0:
		return AuditPlan{}, "it has no single derivable anchor pattern to seed an evaluation from (multi-walk, unlabeled, or not full-engine)"
	default:
		return AuditPlan{}, "its anchor pattern expands to several concrete types, which one key-type anchor listing cannot enumerate"
	}
	label := ""
	for l := range rs.seedAnchorLabels {
		label = l
	}

	// An actor-aware or personal evaluation's "anchor" is the actor, not the
	// event vertex, so seeding it with an anchor key evaluates the wrong entity.
	// Actor-aggregate lenses are the convergence sweep's, not the audit's.
	if p.actorEnumerator != nil || p.envelopeFn != nil || p.multiEnvelopeFn != nil {
		return AuditPlan{}, "it is actor-aggregate or personal, so its rows are the convergence sweep's to verify, not the audit's"
	}
	// Diff retraction diffs the target's FULL live key set against an
	// evaluation's FULL row set, so a single-anchor row set reads as "every
	// other anchor's rows are gone". The audit never writes — but it must not
	// COMPUTE under a shape whose semantics it would misread.
	if p.diffRetraction {
		return AuditPlan{}, "it uses target-diff retraction, whose semantics a single-anchor evaluation would misread"
	}
	// Without read-back there is nothing to compare against, and an audit that
	// cannot compare would report clean.
	if _, ok := p.currentAdapter().(adapter.RowReader); !ok {
		return AuditPlan{}, "its target adapter cannot read a row back, so there is nothing to compare a recomputation against"
	}
	// A Secure Lens's declared columns are decrypted before the results reach
	// any write path (Contract #3 §3.10). A background job with no request
	// context must not re-derive plaintext merely to compare it. The check is
	// against the installed COMPONENT rather than the spec, because the
	// component is what actually decrypts.
	if p.secureDecryptor != nil {
		return AuditPlan{}, "it is a Secure Lens, and a background comparison must not re-derive plaintext outside a request context"
	}

	// The two evaluation parameters that make a recompute legitimately differ
	// from the stored row: $now is wall-clock, and $projectedAt derives from the
	// EVENT vertex's provenance — a neighbor of the anchor on the plain CDC
	// path — which a seeded recompute, supplying the anchor's own props, can
	// never reproduce. Either would read divergent forever.
	//
	// A non-exhaustive walk is a REFUSAL, never a pass: (referenced=false,
	// exhaustive=false) means the accessor could not rule the parameter out, and
	// reading that as absence is exactly the read-the-declaration-not-the-matcher
	// mistake the exhaustive flag exists to prevent.
	for _, param := range []string{"now", "projectedAt"} {
		referenced, exhaustive := fullCR.ReferencesParam(param)
		if !exhaustive {
			return AuditPlan{}, "its query shape could not be proven free of $" + param + ", which a recomputation cannot reproduce"
		}
		if referenced {
			return AuditPlan{}, "it returns $" + param + ", which a recomputation cannot reproduce, so every row would read divergent forever"
		}
	}

	return AuditPlan{AnchorLabel: label}, ""
}
