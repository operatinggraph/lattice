package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// auditorStaleCycles is how many audit intervals may elapse with no verdict
// before Auditor.Stale calls the audit stale — 10, so ~2.5 hours at the 15
// minute default. The window is scaled off the audit's OWN cadence rather than
// being a second independently-tuned duration, and it is generous on purpose: an
// audit that has skipped a tick or two is a detector hiccup, not a lens whose
// rows nothing is re-testing.
//
// It is 10 because the heartbeat's own audit-stall window is
// DefaultCapabilitySweepStallCycles = 10 (internal/refractor/health), and an
// operator who has reasoned about how late an audit may run before the platform
// says so expects the two signals to agree about the same lens. It is
// deliberately a SEPARATE constant rather than a shared one: the heartbeat's
// window governs when to ALARM and so is tuned to stay quiet through an ordinary
// pause or rebuild, while this one governs whether a write may be narrowed and
// so must err toward refusing. Two dispositions that happen to want the same
// number today must be able to move apart without one silently dragging the
// other with it.
const auditorStaleCycles = 10

// auditArmed is the live reading of AuditEnabledByDefault, moved by
// SetAuditEnabled. It is a var rather than the constant itself for two reasons:
// the refusal it produces has to be exercisable (a published refusal nobody has
// ever seen produced is a claim, not a mechanism), and a deployment needs the
// kill switch without a recompile.
var auditArmed = AuditEnabledByDefault

// SetAuditEnabled arms or disarms the divergence audit corpus-wide. Disarming
// makes auditEnrolment refuse every lens with reason "disabled by deployment",
// which is a published refusal like any other — the disabled state stays
// visible PER LENS rather than looking like a clean audit. Call before any
// pipeline activates; the process's own boot is the only sanctioned caller,
// mirroring SetDefaultPlainDerivedAnchorCap.
func SetAuditEnabled(enabled bool) { auditArmed = enabled }

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

// AuditOptions are the deployment-supplied inputs to InstallAudit: the lens's
// plane, and the interval/batch overrides. Zero Interval/Batch select the
// defaults, exactly as SweepPlan's do.
//
// AuthPlane is passed IN rather than read off the pipeline so enrolment cannot
// depend on whether an earlier activation stage happened to record the plane —
// a conjunct that reads as "not auth-plane" because the field was not written
// yet is the fail-open direction, and this conjunct exists to catch exactly the
// lens kind (plain, into the capability bucket) no actor-aggregate installer
// speaks for. The caller supplies projection.IsAuthPlane(rule), the one
// canonical derivation (nats_kv into the capability bucket, or a Postgres grant
// table), rather than a second reading of it here.
type AuditOptions struct {
	AuthPlane bool
	Interval  time.Duration
	Batch     int
}

// AuditPlan is the per-lens data the divergence audit needs. Installing a plan
// is what opts a pipeline into auditing, and only auditEnrolment's fail-closed
// conjuncts produce one.
type AuditPlan struct {
	// AnchorLabel is the single vertex type the lens's seedable anchor pattern
	// binds — the key type whose Core KV population the audit walks. Singular
	// by enrolment: a taxonomy-expanded anchor resolves to a SET of concrete
	// subtypes, which one key-type listing cannot enumerate, and a multi-walk
	// lens has no single anchor at all.
	AnchorLabel string
	// MaskedColumns are the columns the comparison excludes for a Secure Lens
	// — its declared secure columns' RETURN aliases (SecureDecryptor.Columns),
	// §4.1. Empty for a lens with no secure decryptor installed: comparison is
	// over every projected column.
	MaskedColumns []string
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
	// MaskedColumns are the columns this lens's comparison excludes — a
	// Secure Lens's declared secure columns, unverified because the audit's
	// recompute never decrypts them (§4.1). Empty, never nil, for an enrolled
	// lens with no secure decryptor: the container travels for every enrolled
	// lens, following DivergentRows' rule that absence means "not enrolled",
	// never "nothing masked".
	MaskedColumns []string
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
	// The last COMPLETED cycle's totals, stamped together with
	// CycleCompletedAt and describing the same walk. They are what makes that
	// timestamp mean something: a cycle is only recorded as completed when it
	// actually compared anchors, so a fresh CycleCompletedAt beside
	// CycleAudited > 0 says "this many anchors were compared", never "a tick
	// happened".
	//
	// CycleDivergentTotal also fixes the per-pass counters' blind spot: a pass
	// examines at most Batch anchors, so DivergentTotal reads zero on every
	// pass that did not re-examine a divergent one. Only a NEW cycle over the
	// same anchors may supersede a cycle's finding.
	CycleAudited        int
	CycleDivergentTotal int
	CycleUnverified     int
	// ListingSize is how many anchor keys the type filter matched this pass, so
	// a pathologically large anchor type is visible rather than merely
	// expensive. CoverageBasis is always AuditCoverageBasisKeyType.
	ListingSize   int
	CoverageBasis string
	// LastPassAt is when a pass last reached a verdict — never a suppressed
	// one, and never one that compared no anchor. It is the audit's liveness
	// clock: every counter above describes the last pass that ran, so an audit
	// that stops running keeps publishing its final verdict forever, and only
	// this timestamp says the verdict is old.
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
// The comparison is rowsComparableMasked — canonical JSON with the volatile
// envelope fields stripped, the SAME definition of "same row" the sweep and
// Reproject's rowsEquivalent/rowsComparable use, with two classes of column
// additionally excluded per anchor (comparisonIgnore): the row's own key
// columns, always, and a Secure Lens's declared secure columns (§4.1).
// rowsComparable itself is untouched and stays the sweep's own comparator.
type Auditor struct {
	p    *Pipeline
	plan AuditPlan

	interval  time.Duration
	batch     int
	authPlane bool

	mu sync.Mutex
	// anchorLabel is the key type the walk currently enumerates. It lives here
	// rather than in plan because a MATCH hot-reload or a taxonomy reload can
	// move the lens's anchor under an installed plan, and InstallAudit runs
	// once at activation — so the auditor re-derives it every pass and ADOPTS a
	// changed one (resetting the cursor, which addressed the old keyspace)
	// rather than suppressing itself against a frozen copy forever.
	anchorLabel string
	// maskedColumns is the current comparison mask — a Secure Lens's declared
	// secure columns, plus (always, regardless of Secure status) each anchor's
	// own key columns, threaded into the comparison at auditAnchor. It lives
	// here rather than only in plan for the same hot-reload reason as
	// anchorLabel: a lens whose secure decryptor changes (or is installed for
	// the first time) after activation must compare under the new mask on its
	// very next pass, not carry the install-time one forever. Unlike
	// anchorLabel, adopting a new mask resets no cursor: the mask changes what
	// a comparison excludes, not which anchors are walked.
	maskedColumns []string
	status        AuditStatus
	// The in-progress cycle's running totals. They are separate from the
	// status counters, which describe the last PASS: a cycle spans
	// ceil(anchors/batch) passes, and it is the cycle — not the pass — whose
	// completion licenses a claim about the whole lens.
	cycleAudited    int
	cycleDivergent  int
	cycleUnverified int
}

// newAuditor builds the auditor for an installed plan, applying the defaults.
func newAuditor(p *Pipeline, plan AuditPlan, authPlane bool) *Auditor {
	iv := plan.Interval
	if iv <= 0 {
		iv = DefaultAuditInterval
	}
	batch := plan.Batch
	if batch <= 0 {
		batch = DefaultAuditBatch
	}
	return &Auditor{
		p:             p,
		plan:          plan,
		interval:      iv,
		batch:         batch,
		authPlane:     authPlane,
		anchorLabel:   plan.AnchorLabel,
		maskedColumns: plan.MaskedColumns,
		status: AuditStatus{
			Enrolled:      true,
			CoverageBasis: AuditCoverageBasisKeyType,
			MaskedColumns: emptyIfNil(plan.MaskedColumns),
		},
	}
}

// emptyIfNil turns a nil slice into an empty, non-nil one — the "container
// travels even when empty" rule DivergentRows already follows (record's own
// comment): a nil MaskedColumns would marshal as `null`, which in this
// document means "could not be read", never "nothing is masked".
func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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

// Stale reports whether this audit has gone auditorStaleCycles intervals without
// reaching a verdict, and how long it has been since the last one. It is the
// question "is anything actually re-testing this lens's rows RIGHT NOW", which
// AuditStatus.Enrolled and AuditStatus.Suppression together cannot answer: both
// are written by the tick loop, so a loop that has stopped running — crashed,
// wedged, blocked forever inside a pass — leaves Enrolled true and Suppression
// empty for as long as the process lives. LastPassAt is the only field that ages
// on its own, which is exactly what it is for (see AuditStatus.LastPassAt), and
// this is the reading of it.
//
// It is fail-closed in both directions a caller can be wrong about. A zero or
// absent cadence (a refused auditor holds none) reads as stale rather than as
// "no cadence to be late against", because a caller asking this question wants a
// standing re-test and an auditor with no clock has none. And a zero LastPassAt
// — an auditor that has never completed a pass — computes as an enormous elapsed
// and so reads as stale, which is the correct answer for a write licence: not
// yet proven is not licensed. That is the OPPOSITE disposition to the
// heartbeat's own stall detector, which must not alarm on a freshly activated
// lens and therefore rebases its clock at first sight; the two mechanisms are
// deliberately independent for that reason and share no state.
//
// now is supplied by the caller rather than read here so the window is testable
// without waiting for one.
func (a *Auditor) Stale(now time.Time) (stale bool, elapsed time.Duration) {
	interval := a.Interval()
	if interval <= 0 {
		return true, 0
	}
	elapsed = now.Sub(a.Status().LastPassAt)
	return elapsed > auditorStaleCycles*interval, elapsed
}

// AnchorLabel is the vertex type this audit currently walks, and "" for a
// refused lens. Read under the lock because a pass may adopt a new one after a
// hot-reload moved the lens's anchor.
func (a *Auditor) AnchorLabel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.anchorLabel
}

// MaskedColumns is the set of columns the current pass excludes from its
// comparison — a Secure Lens's declared secure columns, and "" (an empty
// slice) for a refused lens or one with none. Read under the lock because a
// pass may adopt a changed set after a hot reload installs or replaces the
// lens's secure decryptor.
func (a *Auditor) MaskedColumns() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.maskedColumns
}

// SetAuditPlan installs the divergence-audit plan for this pipeline. Only
// auditEnrolment's conjuncts may produce a plan; InstallAudit is the production
// entry point that runs them. Must be called before RunAudit.
func (p *Pipeline) SetAuditPlan(plan AuditPlan) {
	p.auditor = newAuditor(p, plan, false)
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
func (p *Pipeline) InstallAudit(opts AuditOptions) (enrolled bool, refusal string) {
	plan, refusal := auditEnrolment(p, p.ruleState(), p.currentAdapter(), opts.AuthPlane)
	if refusal != "" {
		p.auditor = newRefusedAuditor(p, refusal)
		return false, refusal
	}
	plan.Interval, plan.Batch = opts.Interval, opts.Batch
	p.auditor = newAuditor(p, plan, opts.AuthPlane)
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

// suppressed reports why this tick must be skipped, or "" to run it, against
// the ONE rule snapshot and adapter the whole pass is judged under.
//
// Both are passed in rather than re-read here, because this package's own
// contract says so: a rule snapshot is taken once at the top of an operation
// and threaded down, since two snapshots in one operation can straddle a
// hot-reload (Pipeline.ruleState). Re-reading here would make the check
// answerable about a DIFFERENT rule than the one the evaluation runs — the
// enrolment could prove the old rule free of $now while the new one returns it,
// which is precisely the divergence-forever the conjunct exists to prevent.
//
// The first check is the ENROLMENT RE-CHECK. Every conjunct auditEnrolment
// reads is mutable pipeline state, and a lens hot-reload or an adapter swap can
// move any of them under an installed plan; an auditor that trusted its
// install-time plan would keep auditing under a shape the lens no longer has.
// This is the same posture RequireGuardedAdapter takes — the requirement
// outlives this adapter instance.
//
// A changed ANCHOR LABEL is not a suppression: the auditor adopts it (see
// adoptAnchorLabel). Suppressing on it instead would hold forever, because
// InstallAudit runs once at activation and nothing re-invokes it — the lens
// would publish a stale reason until the process restarted, and eventually
// raise an LensAuditStalled nobody could clear.
//
// The rest mirror the sweep verbatim: a rebuild is a superset of the audit
// (truncate + full rescan), a paused pipeline is operator intent the audit must
// not quietly speak over, and an unreadable health entry fails closed. The
// reason is returned rather than a bare bool because every cause is held by
// something external to the audit, so "skipped" is not a transient the next
// tick clears and the operator needs the cause.
func (a *Auditor) suppressed(ctx context.Context, rs ruleState, adpt adapter.Adapter) string {
	plan, refusal := auditEnrolment(a.p, rs, adpt, a.authPlane)
	if refusal != "" {
		return "enrolment no longer holds: " + refusal
	}
	a.adoptAnchorLabel(plan.AnchorLabel)
	a.adoptMaskedColumns(plan.MaskedColumns)
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

// adoptAnchorLabel moves the walk to a freshly-derived anchor label, resetting
// the cursor and the in-progress cycle. Both are scoped to the OLD keyspace: a
// cursor is a key inside it, and a cycle's coverage claim is about its anchors,
// so carrying either into a new label would let the audit resume mid-walk
// through a type it has never seen and stamp a completion over it.
func (a *Auditor) adoptAnchorLabel(label string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if label == a.anchorLabel {
		return
	}
	slog.Info("pipeline: audit: the lens's anchor moved; restarting the walk",
		"ruleId", a.p.ruleID, "from", a.anchorLabel, "to", label)
	a.anchorLabel = label
	a.status.Cursor = ""
	a.cycleAudited, a.cycleDivergent, a.cycleUnverified = 0, 0, 0
}

// adoptMaskedColumns updates the columns this pass's comparison excludes for a
// Secure Lens. Unlike adoptAnchorLabel, no cursor or cycle state depends on
// this value — only the comparison basis for anchors compared FROM HERE ON —
// so a hot reload that adds, removes or changes a secure decryptor's declared
// columns takes effect on the very next anchor this pass compares, with
// nothing to reset: an anchor already compared earlier this cycle is not
// retroactively re-verified, the same as every other counter a cycle never
// rewinds mid-walk.
func (a *Auditor) adoptMaskedColumns(mask []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.maskedColumns = mask
	a.status.MaskedColumns = emptyIfNil(mask)
}

// pass runs one bounded audit tick: page the anchor listing from the persisted
// cursor, re-derive each anchor's rows, compare them against what is stored, and
// publish the verdict. Nothing in it writes to the target.
func (a *Auditor) pass(ctx context.Context) {
	// One rule snapshot and one adapter for the whole pass — the enrolment
	// re-check, the evaluation and the read-back all answer about the same
	// rule, so a hot-reload landing mid-pass cannot desync them.
	rs := a.p.ruleState()
	adpt := a.p.currentAdapter()

	if reason := a.suppressed(ctx, rs, adpt); reason != "" {
		a.noteSuppressed(reason)
		return
	}
	a.noteSuppressed("")

	reader, canRead := adpt.(adapter.RowReader)
	if rs.engine == nil || !canRead {
		// Unreachable: the enrolment re-check above proved both. Reported as a
		// suppression rather than assumed away, because the alternative is a
		// pass that publishes a clean verdict having compared nothing.
		a.noteSuppressed("pipeline cannot evaluate or read back its own rows")
		return
	}

	a.mu.Lock()
	cursor, label := a.status.Cursor, a.anchorLabel
	a.mu.Unlock()

	page, listingSize, next, err := a.page(ctx, label, cursor)
	if err != nil {
		// A pass that could not enumerate its anchors verified nothing, so it
		// must not stamp the liveness clock or publish a verdict — the same
		// disposition a suppressed tick takes, for the same reason.
		slog.Warn("pipeline: audit: anchor listing failed; retrying next tick",
			"ruleId", a.p.ruleID, "anchorLabel", label, "err", err)
		a.noteSuppressed("anchor listing failed: " + err.Error())
		return
	}

	var t auditTally
	for _, key := range page {
		a.auditAnchor(ctx, rs, reader, label, key, &t)
	}

	// Re-derived after the batch, never before it. A pass takes real time —
	// one seeded evaluation and one or more target reads per anchor — and both
	// conditions below arise DURING it:
	//
	//   - A rule swap (a MATCH edit, an async taxonomy rebuild) makes every
	//     comparison in hand a comparison against a rule no longer in force.
	//     The sweep abandons its pass for this exact reason (ErrRuleSuperseded);
	//     the audit has no write to withhold, so what it withholds is the
	//     VERDICT.
	//   - A rebuild or a pause starting mid-batch means the later anchors were
	//     compared against a target being truncated underneath them, which
	//     reads as a run of `missing` divergences that were never divergent.
	//
	// Either way the pass reached no trustworthy verdict, so it publishes the
	// reason and leaves the liveness clock ageing rather than banking a finding.
	switch {
	case a.p.supersededRule(rs):
		slog.Info("pipeline: audit: pass discarded — rule swapped mid-pass", "ruleId", a.p.ruleID)
		a.noteSuppressed("rule swapped mid-pass")
		return
	default:
		if reason := a.suppressed(ctx, rs, adpt); reason != "" {
			a.noteSuppressed("started mid-pass: " + reason)
			return
		}
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
func (a *Auditor) page(ctx context.Context, label, cursor string) (page []string, listingSize int, next string, err error) {
	filter := substrate.VertexPrefix + "." + label + ".*"
	keys, _, err := a.p.coreKV.ListKeysFilter(ctx, filter, "", 0)
	if err != nil {
		return nil, 0, "", err
	}
	anchors := make([]string, 0, len(keys))
	for _, k := range keys {
		// The filter already excludes the four-segment aspect keys; this admits
		// only a well-formed root of the anchor type the plan names.
		vtxType, _, ok := substrate.ParseVertexKey(k)
		if !ok || vtxType != label {
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

// comparisonIgnore is the full set of columns one anchor's should-exist
// comparison excludes:
//
//   - keys' own column names, always — a freshly computed row carries every
//     RETURN alias, key columns included, while GetRow's contract excludes them
//     (rowsComparableMasked's doc says why);
//   - the columns the STORED row carries and the computed one does not
//     (storedOnlyColumns), which on a `SELECT *` reader is a column of the table
//     the lens does not project — a migration leftover reads `stale` on every
//     pass forever otherwise;
//   - for a Secure Lens, MaskedColumns (§4.1).
//
// Computed fresh per row rather than cached: the first two are a property of the
// row in hand, and building the slice here keeps the mask's own hot-reload
// adoption (adoptMaskedColumns) the only place that mutates shared state.
func (a *Auditor) comparisonIgnore(keys, stored, computed map[string]any) []string {
	ignore := append(keyColumnNames(keys), a.MaskedColumns()...)
	return append(ignore, storedOnlyColumns(stored, computed)...)
}

// auditAnchor reaches one anchor's verdict. Every path either books the anchor
// as audited (with zero or more divergent rows) or books it as unverified, and
// an anchor whose check faults partway discards the classes it had already found
// — a partially-checked anchor is not a divergent one, it is an unknown one.
func (a *Auditor) auditAnchor(ctx context.Context, rs ruleState, reader adapter.RowReader, label, key string, t *auditTally) {
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
		keys, ok := rs.engine.AnchorDeleteResult(rs.cr, key, label, props)
		if !ok {
			// A partition-armed lens has no read-free row key — its key carries
			// a neighbour-bound column — but it does have a partition, and the
			// tombstone's retraction was supposed to empty it. Listing it is
			// the same question asked of the scope the CDC path actually
			// retracts within.
			retained, listed, lerr := a.partitionHoldsAnyLiveKey(ctx, rs, reader, key, label)
			if lerr != nil {
				t.noteUnverified("partition listing failed: " + lerr.Error())
				return
			}
			if !listed {
				t.noteUnverified(auditReasonUndrivableKey)
				return
			}
			if retained {
				classes = append(classes, AuditClassRetained)
			}
			a.commit(t, classes)
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
	results, err := a.p.executeFullForAudit(ctx, rs, key, props, key)
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
		if !present {
			classes = append(classes, AuditClassMissing)
			continue
		}
		equal, comparable := rowsComparableMasked(stored, res.Row, a.comparisonIgnore(res.Keys, stored, res.Row))
		switch {
		case !comparable:
			// One side could not be rendered at all (a value JSON cannot
			// express). The audit has NOT proven a divergence — it has failed
			// to make the comparison — and reporting the two identically would
			// alarm on a fault in the observation.
			t.noteUnverified("row could not be rendered for comparison")
			return
		case !equal:
			classes = append(classes, AuditClassStale)
		}
	}

	// Should-not-exist: the rows this anchor OWNS, when the recomputation no
	// longer produces them. Two derivations, and which one applies is the same
	// question the CDC retraction path asks of the same lens.
	//
	// The read-free one (AnchorProjectionKey) names the single row a closed lens
	// owns. When it declines — a neighbour-keyed or multi-row lens — a
	// partition-armed lens has its PARTITION listed instead, and a key in it the
	// recompute did not produce books `retained`. That is the one direction a
	// per-anchor seeded evaluation can go wrong in: it can produce FEWER rows
	// than the whole one, and only this listing names the rows left behind. A
	// lens that is neither closed nor armed is not checked in this direction at
	// all, which is why the class counts travel per-class rather than as one
	// number: an absent `retained` on one of them reads as "not detected here",
	// never as "clean".
	if owned, ok := rs.engine.AnchorProjectionKey(rs.cr, key, label, props); ok {
		if !resultsContainKeys(results, owned) {
			_, present, rerr := reader.GetRow(ctx, owned)
			if rerr != nil {
				t.noteUnverified("target row read failed: " + rerr.Error())
				return
			}
			if present {
				classes = append(classes, AuditClassRetained)
			}
		}
	} else if extra, listed, lerr := a.partitionHoldsUnproducedKey(ctx, rs, reader, key, label, results); lerr != nil {
		t.noteUnverified("partition listing failed: " + lerr.Error())
		return
	} else if listed && extra {
		classes = append(classes, AuditClassRetained)
	}
	a.commit(t, classes)
}

// partitionHoldsAnyLiveKey reports whether a TOMBSTONED anchor's partition still
// holds a live key — the retraction its own tombstone should have produced,
// lost. listed is false when this lens is not partition-armed or the predicate
// declines, which leaves the anchor unchecked in this direction — the same
// disposition the CDC path takes for a key it cannot derive.
func (a *Auditor) partitionHoldsAnyLiveKey(ctx context.Context, rs ruleState, reader adapter.RowReader, key, label string) (retained, listed bool, err error) {
	existing, listed, err := a.listAnchorPartition(ctx, rs, key, label)
	if err != nil || !listed {
		return false, listed, err
	}
	for _, keys := range existing {
		live, rerr := rowIsLive(ctx, reader, keys)
		if rerr != nil {
			return false, false, rerr
		}
		if live {
			return true, true, nil
		}
	}
	return false, true, nil
}

// partitionHoldsUnproducedKey reports whether a LIVE anchor's partition holds a
// key the seeded recomputation did not produce — an under-produced evaluation's
// leftovers, which is the failure mode the partition-scoped seeding could
// introduce and which nothing else would name (§3.5).
func (a *Auditor) partitionHoldsUnproducedKey(ctx context.Context, rs ruleState, reader adapter.RowReader, key, label string, results []ruleengine.EvalResult) (extra, listed bool, err error) {
	existing, listed, err := a.listAnchorPartition(ctx, rs, key, label)
	if err != nil || !listed {
		return false, listed, err
	}
	for _, keys := range existing {
		if resultsContainKeys(results, keys) {
			continue
		}
		live, rerr := rowIsLive(ctx, reader, keys)
		if rerr != nil {
			return false, false, rerr
		}
		if live {
			return true, true, nil
		}
	}
	return false, true, nil
}

// rowIsLive reads one listed key back, so a key a listing still carries but the
// target has already tombstoned is not booked as a retained row. It is the same
// probe the partition retraction runs before it tombstones a listed key, asked
// here for the same reason: the listing says a key exists, only the read says it
// is live.
func rowIsLive(ctx context.Context, reader adapter.RowReader, keys map[string]any) (bool, error) {
	_, present, err := reader.GetRow(ctx, keys)
	if err != nil {
		return false, fmt.Errorf("target row read failed: %w", err)
	}
	return present, nil
}

// listAnchorPartition lists the live keys of one anchor's partition, through the
// SAME predicate and the SAME lister the CDC retraction scopes itself by — so
// the audit speaks about the scope the retraction acts on rather than a second
// derivation of it.
//
// listed is false, with no error, for every lens this direction does not apply
// to: one that is not partition-armed, a target that cannot list a partition, or
// an anchor whose predicate declines. Those are the "not checked in this
// direction" cases the caller books as undrivable rather than as clean.
func (a *Auditor) listAnchorPartition(ctx context.Context, rs ruleState, key, label string) (existing []map[string]any, listed bool, err error) {
	if !a.p.partitionArmed(rs) {
		return nil, false, nil
	}
	lister, ok := a.p.currentAdapter().(adapter.PartitionKeyLister)
	if !ok {
		return nil, false, nil
	}
	fixed, ok := rs.engine.PartitionPredicate(rs.cr, key, label)
	if !ok {
		return nil, false, nil
	}
	existing, err = lister.ListKeysWhere(ctx, fixed, a.p.diffRetractionPrefix)
	if err != nil {
		return nil, false, err
	}
	return existing, true, nil
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
	total := 0
	for _, n := range t.divergent {
		total += n
	}

	a.mu.Lock()
	a.status.Audited = t.audited
	a.status.Unverified = t.unverified
	a.status.LastUnverified = truncateFailure(t.lastUnverified)
	a.status.Divergent = t.divergent
	a.status.DivergentTotal = total
	a.status.ListingSize = listingSize
	a.status.CoverageBasis = AuditCoverageBasisKeyType
	a.status.Cursor = next

	a.cycleAudited += t.audited
	a.cycleDivergent += total
	a.cycleUnverified += t.unverified
	// A cycle is recorded as COMPLETED only when the walk reached the end of
	// the listing AND actually compared something. The guard is the whole point
	// of the field: a pass over an empty page — an emptied anchor type, or a
	// cursor left past every live key — reaches the end having verified
	// nothing, and stamping it would publish `divergentTotal: 0` beside a fresh
	// completion timestamp, which reads as "the whole lens was audited and is
	// clean". A lens with no enumerable anchors therefore never earns a
	// completion stamp, and its clean number stays visibly unsubstantiated.
	if next == "" && a.cycleAudited > 0 {
		a.status.CycleCompletedAt = time.Now()
		a.status.CycleAudited = a.cycleAudited
		a.status.CycleDivergentTotal = a.cycleDivergent
		a.status.CycleUnverified = a.cycleUnverified
		a.cycleAudited, a.cycleDivergent, a.cycleUnverified = 0, 0, 0
	}
	// The liveness clock takes the same guard one step earlier, and for a
	// stronger reason. LastPassAt means "a pass reached a verdict", and a pass
	// that compared no anchor reached none — whether the page was empty or every
	// anchor on it hit noteUnverified, which one target-read outage produces for
	// a whole page at once. Stamped unconditionally it would publish
	// `divergentTotal: 0` beside a fresh clock off a pass that verified literally
	// nothing, which is the same over-claim the cycle stamp above refuses.
	//
	// Downstream it is worse than a misleading number. plainDerivationLicence
	// (anchor_derivation_plain.go) reads this field through Auditor.Stale to
	// decide whether a plain lens's narrowing WRITE licence still holds, and that
	// conjunct exists precisely so an audit loop that is no longer reaching
	// verdicts cannot keep licensing narrowed writes. A blind loop stamping the
	// clock is that same fail-open reached by a loop that runs rather than one
	// that died — and during a target-read outage it is reached alongside the §6
	// presence probe dropping every derived retraction, so the one detector that
	// would catch the consequence is the detector reporting clean.
	if t.audited > 0 {
		a.status.LastPassAt = time.Now()
	}
	snapshot := a.status
	a.mu.Unlock()

	if snapshot.DivergentTotal > 0 {
		slog.Warn("pipeline: audit: the projection disagrees with the graph",
			"ruleId", a.p.ruleID, "coverageBasis", snapshot.CoverageBasis,
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
func auditEnrolment(p *Pipeline, rs ruleState, adpt adapter.Adapter, authPlane bool) (AuditPlan, string) {
	if !auditArmed {
		return AuditPlan{}, "disabled by deployment"
	}
	// The auth plane is the sweep's, and this refusal is the only thing that
	// keeps it so. The actor-aggregate conjunct below excludes every auth-plane
	// lens SHIPPED today, and the RowReader conjunct excludes the Postgres
	// grant tables — but a plain-kind lens declaring nats_kv into the
	// capability bucket is neither, and would enrol. A divergence found there
	// is an authorization finding, and it must be raised by a plane that has a
	// code, a severity ladder and an escalation for it rather than published as
	// a bare number by a detector built for business read models.
	if authPlane {
		return AuditPlan{}, "it projects onto the auth plane, whose per-row verdicts are the convergence sweep's"
	}

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
	// DiffRetraction declares a target-diff retraction transport, not an
	// evaluation shape: executeFullForAudit (auditAnchor's own call) never
	// calls applyDiffRetraction or applyPartitionDiffRetraction — their only
	// caller is evaluateForEntryRaw's plain arm, which the audit does not run —
	// so a DiffRetraction lens's seeded evaluation is read exactly like any
	// other plain lens's.
	//
	// What the declaration decides is which SHOULD-NOT-EXIST direction the
	// anchor is checked in, and there are three (auditAnchor, below).
	// AnchorProjectionKey — the read-free presence-check derivation — declines
	// for most of this corpus's DiffRetraction lenses, because their key carries
	// a neighbour-bound column. For a PARTITION-ARMED lens the anchor's own
	// partition is listed instead, so a tombstoned anchor with a live key in it,
	// or a live anchor whose partition holds a key the recompute did not
	// produce, books `retained` — which is the standing detector for an
	// under-producing seeded evaluation, shipped in the same increment as the
	// seeding that could cause one. For a DiffRetraction lens that is NOT armed
	// the anchor is still unchecked in this direction, and enrols with
	// `missing`/`stale` only: an absent `retained` there reads as "not detected
	// in this direction", never as "clean", the same as the CDC path it mirrors.
	//
	// Without read-back there is nothing to compare against, and an audit that
	// cannot compare would report clean.
	if _, ok := adpt.(adapter.RowReader); !ok {
		return AuditPlan{}, "its target adapter cannot read a row back, so there is nothing to compare a recomputation against"
	}
	// A Secure Lens's declared secure columns are decrypted only inside
	// evaluateForEntry (Pipeline.handle's own CDC path) and the two actor
	// fan-out handlers — never inside executeFullForAudit, so the audit's
	// recomputed row always carries the raw ciphertext envelope while the
	// stored row carries the decrypted plaintext (or null). That is not a
	// reason to refuse the lens; it is a reason to exclude those columns from
	// the comparison (§4.1): the mask below, threaded through
	// rowsComparableMasked at auditAnchor's one comparison site. A masked
	// `stale` verdict is exact over every OTHER column; a secure column is
	// simply unverified, never assumed equal or assumed diverged.
	var maskedColumns []string
	if p.secureDecryptor != nil {
		maskedColumns = p.secureDecryptor.Columns()
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

	return AuditPlan{AnchorLabel: label, MaskedColumns: maskedColumns}, ""
}
