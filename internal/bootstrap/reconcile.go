package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// ReconcileResult reports what a reconcile pass changed. A converged kernel
// yields Created == Updated == 0.
type ReconcileResult struct {
	Created   int
	Updated   int
	Unchanged int
	// Retained counts entries that differ from what this binary builds but are
	// deliberately not rewritten — a deleted entity, or topology owned by
	// operations rather than the seeder.
	Retained int
}

// Changed reports whether the pass wrote anything.
func (r ReconcileResult) Changed() bool { return r.Created > 0 || r.Updated > 0 }

// reconcileStep is one planned write: a kernel entry Core KV does not match.
// createOnly distinguishes an entry the bucket lacks from one it holds with a
// different body, which carries the revision the comparison read.
type reconcileStep struct {
	key        string
	value      []byte
	createOnly bool
	revision   uint64
}

// reconcilePlan is the difference between the kernel this binary builds and the
// kernel Core KV holds. An empty plan means there is nothing to write.
//
// `retained` names entries that differ but are deliberately left alone: a
// deleted entity, or topology whose ongoing lifecycle belongs to operations
// rather than to the seeder. They are reported, never rewritten.
//
// `orphanedEntities` and `orphanedAspects` name vtx.meta.* keys this binary
// no longer builds at all — retired kernel, not merely stale kernel. Neither
// ever reaches steps(): reporting a candidate for retirement and actually
// retiring it are different acts, and this plan only ever proves out the
// former.
//
// `orphanScanErr` carries a failure of the orphan LISTING itself
// (kernel-orphan-retirement-design.md §3.2's partial-result guard) separately
// from planReconcile's own error return. A reader that never asked about
// orphans — KernelDrift, or ReconcilePrimordial's kernel-repair path — must
// not be denied its own answer because the unrelated orphan scan could not
// run; only a caller whose contract IS the orphan report (KernelOrphans)
// surfaces it.
//
// `strandedEpochs` names prior-bootstrap-epoch operator roles that are still
// live and reachable from no current-epoch identity
// (primordial-epoch-stranded-authority-design.md §3) —
// an orphan class over a different key family than the vtx.meta.> census, and
// likewise reported here rather than acted on. `strandedScanErr` carries that
// scan's own failure for exactly the reason orphanScanErr does: the scan is
// advisory, so its inability to run must never deny an unrelated reader its own
// answer, and must never turn a boot into a failed boot.
type reconcilePlan struct {
	missing          []reconcileStep
	stale            []reconcileStep
	retained         []string
	unchanged        int
	orphanedEntities []string
	orphanedAspects  []string
	orphanScanErr    error
	strandedEpochs   []StrandedOperatorEpoch
	strandedScanErr  error
}

func (p reconcilePlan) steps() []reconcileStep {
	return append(append([]reconcileStep{}, p.missing...), p.stale...)
}

// isKernelDefinition reports whether a key holds kernel *definition* content —
// a DDL or lens meta-vertex and its aspects, where the scripts, schemas and
// specs this binary compiles live. These are the entries a stale bucket must
// not keep, and the only ones reconcile rewrites.
func isKernelDefinition(key string) bool { return strings.HasPrefix(key, "vtx.meta.") }

// storedIsDeleted reports whether a stored document carries a Lattice soft
// tombstone. A tombstone leaves the key present with isDeleted set, so it is
// indistinguishable from staleness by content comparison alone — and it is the
// exact opposite: a deliberate act, recorded by an operation.
func storedIsDeleted(raw []byte) bool {
	var env struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	return env.IsDeleted
}

// planReconcile compares every kernel entry this binary builds against what
// Core KV stores. It performs no writes, and is the single comparison path
// behind both ReconcilePrimordial and KernelDrift so the repair and the
// verification gate can never disagree about what "stale" means.
//
// What may be rewritten is deliberately narrow, because "the stored body
// differs from the built one" does NOT imply "an older binary wrote it":
//
//   - The bootstrap op tracker is excluded outright. It is the sentinel
//     PrimordialSeeded and DecideReseed probe and the two-phase-commit marker
//     (Contract #7 §7.4) — seeding-state machinery, not kernel content.
//   - A stored document carrying a soft tombstone is never rewritten. The
//     primordial links are outside the Processor's protected-key guard by
//     design (protectedRootKey returns "" for anything not vertex-rooted), and
//     rbac-domain's RevokeRole/RevokePermission tombstone exactly those key
//     shapes. Rewriting one would silently restore a revoked grant — turning a
//     boot into an unlogged privilege escalation.
//   - Only kernel definitions (vtx.meta.*) are rewritten at all. Identities,
//     roles, permissions and links are topology whose ongoing lifecycle belongs
//     to operations, not to the seeder; their divergence is reported, never
//     reverted. Scripts, schemas and lens specs are what a stale bucket must
//     not keep, and they all live under vtx.meta.*.
//
// Every key is still created when absent: a missing entry is an incomplete
// kernel, and a soft tombstone leaves its key present, so a revoked grant is
// never resurrected by the create path either.
func planReconcile(ctx context.Context, kv jetstream.KeyValue) (reconcilePlan, error) {
	var plan reconcilePlan

	entries, err := buildPrimordialEntries()
	if err != nil {
		return plan, fmt.Errorf("build primordial entries: %w", err)
	}

	built := make(map[string]bool, len(entries))
	for _, e := range entries {
		built[e.key] = true
	}

	for _, e := range entries {
		if e.key == BootstrapOpKey {
			continue
		}
		stored, getErr := kv.Get(ctx, e.key)
		if getErr != nil {
			if !errors.Is(getErr, jetstream.ErrKeyNotFound) {
				return plan, fmt.Errorf("read kernel key %q: %w", e.key, getErr)
			}
			plan.missing = append(plan.missing, reconcileStep{key: e.key, value: e.value, createOnly: true})
			continue
		}
		same, cmpErr := sameDocument(stored.Value(), e.value)
		if cmpErr != nil {
			return plan, fmt.Errorf("compare kernel key %q: %w", e.key, cmpErr)
		}
		if same {
			plan.unchanged++
			continue
		}
		if storedIsDeleted(stored.Value()) || !isKernelDefinition(e.key) {
			plan.retained = append(plan.retained, e.key)
			continue
		}
		plan.stale = append(plan.stale, reconcileStep{key: e.key, value: e.value, revision: stored.Revision()})
	}

	orphanedEntities, orphanedAspects, orphanErr := scanKernelOrphans(ctx, kv, built)
	plan.orphanedEntities = orphanedEntities
	plan.orphanedAspects = orphanedAspects
	plan.orphanScanErr = orphanErr

	strandedEpochs, strandedErr := StrandedOperatorEpochs(ctx, kv)
	plan.strandedEpochs = strandedEpochs
	plan.strandedScanErr = strandedErr

	return plan, nil
}

// scanKernelOrphans enumerates every vtx.meta.* key Core KV holds that this
// binary does not build, and classifies each against the
// kernel-orphan-retirement-design.md §3.1 discriminator: a retired-kernel
// ENTITY (its meta-vertex root, absent from built and from nothing else), an
// orphaned ASPECT of an entity that is still built
// (kernel-orphan-retirement-design.md §3.4 — reported, never retired on its
// own), or a candidate this pass must leave entirely alone.
//
// The listing itself must be trusted or refused outright: a silently short
// listing would report fewer orphans than exist, which is safe (a missed
// retirement, never an invented one — kernel-orphan-retirement-design.md
// §3.2), but a listing that goes
// silently PARTIAL under context expiry is indistinguishable from a complete
// one unless checked, and acting on it as "no orphans" would be the exact
// wrong direction. So the listing error is returned to the caller — see
// reconcilePlan's orphanScanErr doc for how far that propagates.
//
// A failure reading any ONE candidate — a KVGet error, an unparseable
// envelope, a missing or foreign createdByOp — does not propagate: that
// candidate is silently kept. Nothing here is written, so there is nothing an
// unreadable candidate could corrupt; propagating its error instead would let
// one unreadable package meta-vertex take every boot down with it, which is
// strictly worse than leaving it unreported.
//
// Root presence — the row-4/5 discriminator between "an aspect whose entity
// row already adjudicates it" and "a parentless aspect" — is answered from
// the listing itself (presentRoots), never a second per-candidate KVGet: the
// listing already names every root the bucket holds. A candidate's own body
// is read at most once, and only when its classification actually needs it —
// an unbuilt root, an aspect of a still-built root, or a parentless aspect.
// An aspect whose unbuilt root is present in the listing is never read at
// all, which is what keeps this pass proportional to the ORPHAN count rather
// than to the platform's whole package-meta population.
func scanKernelOrphans(ctx context.Context, kv jetstream.KeyValue, built map[string]bool) (entities, aspects []string, err error) {
	lister, err := kv.ListKeysFiltered(ctx, "vtx.meta.>")
	if err != nil {
		return nil, nil, fmt.Errorf("list kernel meta-vertices: %w", err)
	}
	defer lister.Stop()
	var candidates []string
	for k := range lister.Keys() {
		candidates = append(candidates, k)
	}
	// The lister's feed goroutine exits (closing the channel) on ctx expiry
	// exactly as it does on completion, so a timed-out listing is otherwise
	// indistinguishable from a complete one — mirrors the guard
	// substrate/kv.go's KVListKeysPrefix applies to the same hazard.
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("list kernel meta-vertices: interrupted (partial result discarded): %w", err)
	}

	presentRoots := make(map[string]bool, len(candidates))
	for _, key := range candidates {
		if _, _, ok := substrate.ParseVertexKey(key); ok {
			presentRoots[key] = true
		}
	}

	for _, key := range candidates {
		if built[key] {
			continue
		}
		root, ok := metaVertexRoot(key)
		if !ok {
			continue
		}
		switch {
		case root == key:
			// Vertex-shape candidate: it IS the root. Its own filters decide
			// row 1 (report as entity) vs row 2 (keep).
			if kernelCandidatePasses(ctx, kv, key) {
				entities = append(entities, key)
			}
		case built[root]:
			// Aspect of a still-built root. Its own filters decide row 3
			// (report as aspect) vs row 6 (keep).
			if kernelCandidatePasses(ctx, kv, key) {
				aspects = append(aspects, key)
			}
		case presentRoots[root]:
			// Row 4 — the root is itself an unbuilt candidate in this same
			// listing; row 1/2 over the root key is what adjudicates the
			// entity. This aspect is never read and never reported on its
			// own, or one orphaned entity would surface once as an entity
			// and again, separately, as every one of its aspects.
		default:
			// Row 5 — parentless: no entity row will ever adjudicate it, so
			// this aspect's own filters decide row 5 (report) vs row 6 (keep).
			if kernelCandidatePasses(ctx, kv, key) {
				aspects = append(aspects, key)
			}
		}
	}

	sort.Strings(entities)
	sort.Strings(aspects)
	return entities, aspects, nil
}

// metaVertexRoot returns a vtx.meta.* candidate's root vertex key: itself,
// for a vertex-shape key, or its parent, for an aspect-shape one. ok is
// false for a listed key that is neither — something the vtx.meta.> filter
// should never surface, but a corrupt or foreign write might.
func metaVertexRoot(key string) (root string, ok bool) {
	if _, _, vertexOK := substrate.ParseVertexKey(key); vertexOK {
		return key, true
	}
	if vertexKey, _, _, _, aspectOK := substrate.ParseAspectKey(key); aspectOK {
		return vertexKey, true
	}
	return "", false
}

// kernelCandidatePasses reports whether a candidate key's stored envelope
// clears every kernel-orphan-retirement-design.md §3.1 filter this pass is
// licensed to act on: it reads, it parses, its createdByOp is this
// deployment's bootstrap op tracker (kernel-orphan-retirement-design.md §4 —
// the sound discriminator no other writer can produce), and it is not
// already tombstoned. Any failure here
// means "unknown provenance" or "already retired", and both keep — the
// default is keep, and nothing an author omits can flip it.
func kernelCandidatePasses(ctx context.Context, kv jetstream.KeyValue, key string) bool {
	stored, err := kv.Get(ctx, key)
	if err != nil {
		return false
	}
	var env struct {
		CreatedByOp string `json:"createdByOp"`
		IsDeleted   bool   `json:"isDeleted"`
	}
	if err := json.Unmarshal(stored.Value(), &env); err != nil {
		return false
	}
	if env.CreatedByOp != BootstrapOpKey {
		return false
	}
	return !env.IsDeleted
}

// ReconcilePrimordial brings an already-seeded Core KV's kernel into agreement
// with the kernel this binary builds: it creates entries the bucket is missing
// and rewrites entries whose stored body differs from the built one.
//
// It exists because seeding is create-only and runs once. Without a reconcile
// the kernel in a long-lived bucket is frozen at whatever binary first seeded
// it, and every later fix to a kernel DDL — script, schema, lens spec — is
// invisible until the bucket is wiped and re-bootstrapped. A shared dev stack
// and a demo box cannot casually be wiped, so "wipe it" is not a remedy.
//
// The comparison is against built content rather than a stored version number
// deliberately. buildPrimordialEntries is deterministic — every envelope is
// stamped with the fixed BootstrapTime and every NanoID comes from
// lattice.bootstrap.json — so the built set answers "does this bucket match
// this binary?" directly. A version register would answer it only while every
// author of a kernel edit remembered to bump it, and a forgotten bump fails
// silently, which is the failure this reconcile exists to remove.
//
// The pass is a fixpoint: a converged bucket produces zero writes, which is
// what keeps boot idempotent (Contract #7 §7.4). Termination rests entirely on
// the built envelope carrying BootstrapTime rather than wall-clock provenance —
// a reconcile that stamped the current time would rewrite the whole kernel on
// every boot. Nothing here may introduce a time-varying field.
//
// Writes land in one atomic batch, for the same reason seeding does: a
// meta-vertex's aspects must not diverge from each other. A DDL left with a new
// .script beside an old .inputSchema would validate ops against one definition
// and execute another. Updates are revision-conditioned, so a concurrent
// bootstrapper cannot be clobbered.
//
// Writes go directly to Core KV, as primordial seeding already does (Contract
// #7 §7.1). The repair cannot route through the Processor: the Processor's own
// DDLs are what is being repaired, and protected kernel roots are rejected at
// commit time by design.
//
// A running Processor keeps serving the kernel it loaded at startup: DDLCache
// is filled by Refresh and thereafter only re-read per meta-root by the
// in-commit Invalidate, so a reconcile it did not commit goes unnoticed until
// it restarts. `make up` does not close that gap either — it short-circuits to
// "kernel already up … reusing" whenever the stack is healthy, and never runs
// this binary at all. Applying a kernel fix to a live stack is therefore
// `make reseed-kernel`, which reconciles and then cycles the Processor.
func (s *Seeder) ReconcilePrimordial(ctx context.Context) (ReconcileResult, error) {
	var res ReconcileResult

	kv, err := s.js.KeyValue(ctx, CoreKVBucket)
	if err != nil {
		return res, fmt.Errorf("open Core KV: %w", err)
	}

	plan, err := planReconcile(ctx, kv)
	if err != nil {
		return res, err
	}

	res.Created = len(plan.missing)
	res.Updated = len(plan.stale)
	res.Unchanged = plan.unchanged
	res.Retained = len(plan.retained)

	for _, key := range plan.retained {
		s.logger.Warn("kernel entry differs but is left as stored — deleted, or topology owned by operations",
			"key", key)
	}

	switch {
	case plan.orphanScanErr != nil:
		s.logger.Warn("cannot scan for kernel orphans — kernel repair proceeds without an orphan report",
			"error", plan.orphanScanErr)
	default:
		for _, key := range plan.orphanedEntities {
			s.logger.Warn("kernel entity no longer built by this binary — reported, not retired",
				"key", key)
		}
		for _, key := range plan.orphanedAspects {
			s.logger.Warn("kernel aspect orphaned but its entity is still built — reported, not retired",
				"key", key)
		}
	}

	// A stranded prior-epoch operator role is advisory at boot and never fatal:
	// boot cannot fix it (the remedy is a re-bootstrap from clean state, or the
	// reconciliation verb that does not exist yet), and a boot that refused to
	// come up over a condition it cannot repair would take the deployment down
	// without moving it any closer to repaired. verify-kernel is where this
	// moves an exit status.
	switch {
	case plan.strandedScanErr != nil:
		s.logger.Warn("cannot scan for stranded primordial-epoch roles — kernel repair proceeds without that report",
			"error", plan.strandedScanErr)
	default:
		for _, stranded := range plan.strandedEpochs {
			s.logger.Warn("operator role from a prior bootstrap epoch is live and reachable from no current-epoch identity — reported, not retired",
				"key", stranded.RoleKey, "liveGrants", len(stranded.GrantedBy),
				"priorEpochHolders", len(stranded.Holders), "protected", stranded.Protected)
		}
	}

	steps := plan.steps()
	if len(steps) == 0 {
		s.logger.Info("kernel definitions already match this binary — no writes",
			"unchanged", res.Unchanged, "retained", res.Retained)
		return res, nil
	}

	for _, st := range steps {
		s.logger.Info("kernel entry does not match this binary",
			"key", st.key, "action", map[bool]string{true: "create", false: "update"}[st.createOnly])
	}

	conn, err := substrate.Wrap(s.nc)
	if err != nil {
		return res, fmt.Errorf("substrate wrap: %w", err)
	}

	ops := make([]substrate.BatchOp, 0, len(steps))
	for _, st := range steps {
		// A rewrite is revision-conditioned on the body the plan compared, so a
		// concurrent writer is never clobbered. A restore is not: CreateOnly
		// asserts last-sequence zero, which a deleted key's own marker already
		// violates, so conditioning it would make a purged kernel entry
		// permanently unrestorable — the batch would be rejected on every boot
		// and the entry would stay missing. There is nothing to protect on a key
		// the plan observed as absent, and the value written is the same
		// deterministic kernel body any concurrent bootstrapper would write.
		ops = append(ops, substrate.BatchOp{
			Bucket:      CoreKVBucket,
			Key:         st.key,
			Value:       st.value,
			HasRevision: !st.createOnly,
			Revision:    st.revision,
		})
	}

	if _, batchErr := conn.AtomicBatch(ctx, ops); batchErr != nil {
		// A rejected batch means the bucket moved under the plan — most often a
		// concurrent bootstrapper converging the same keys to the same bytes.
		// The kernel is left exactly as it was (that is what atomicity buys),
		// and the next boot re-plans against the new state.
		if errors.Is(batchErr, substrate.ErrAtomicBatchRejected) && substrate.IsRevisionConflict(batchErr) {
			s.logger.Warn("kernel reconcile batch rejected — a concurrent writer moved these keys; re-planning next boot",
				"error", batchErr, "planned", len(ops))
			return ReconcileResult{Unchanged: plan.unchanged, Retained: len(plan.retained)}, nil
		}
		return res, fmt.Errorf("kernel reconcile batch: %w", batchErr)
	}

	s.logger.Info("kernel reconciled to this binary",
		"created", res.Created, "updated", res.Updated,
		"unchanged", res.Unchanged, "retained", res.Retained)
	return res, nil
}

// KernelReport is the combined read-only view over one planReconcile pass:
// drift (missing/stale) plus kernel orphans, so a caller wanting both — every
// verify-kernel consumer does — pays for a single plan instead of running the
// comparison twice. KernelDrift and KernelOrphans are thin wrappers over
// ReadKernelReport for callers that only want one half.
type KernelReport struct {
	Missing, Stale                    []string
	OrphanedEntities, OrphanedAspects []string
	// OrphanScanErr is the orphan LISTING's own failure (reconcilePlan's
	// orphanScanErr doc) — distinct from the error ReadKernelReport itself
	// returns, which reflects only a failure of the built-set comparison
	// every consumer depends on.
	OrphanScanErr error
	// StrandedOperatorEpochs names prior-bootstrap-epoch operator roles that
	// are still live and reachable from no current-epoch identity
	// (primordial-epoch-stranded-authority-design.md §3). The count of live
	// grants on each is what decides its severity at the gate: authority no
	// identity can reach is a failure, dead weight is a notice.
	StrandedOperatorEpochs []StrandedOperatorEpoch
	// StrandedScanErr is the stranded-epoch SCAN's own failure — narrower, the
	// same way OrphanScanErr is, than the error ReadKernelReport returns: the
	// scan alone could not be trusted, which says nothing about Missing/Stale
	// and must never reach a caller that never asked about stranded epochs.
	StrandedScanErr error
}

// ReadKernelReport runs planReconcile once and returns everything a
// verification gate needs from it. The returned error is non-nil only when
// the built-set comparison itself failed — a plan no caller can trust at
// all. OrphanScanErr and StrandedScanErr are narrower: one advisory scan alone
// could not be trusted, which does not invalidate Missing/Stale.
func ReadKernelReport(ctx context.Context, kv jetstream.KeyValue) (KernelReport, error) {
	plan, err := planReconcile(ctx, kv)
	if err != nil {
		return KernelReport{}, err
	}
	return kernelReportFromPlan(plan), nil
}

// kernelReportFromPlan projects a completed plan into the report shape. It is
// the whole of ReadKernelReport that decides what an advisory scan's failure
// does, and it is separated from the I/O so that decision is assertable
// directly: an advisory scan's error reaches its own report field and nothing
// else, leaving the built-set comparison every consumer depends on intact
// beside it.
func kernelReportFromPlan(plan reconcilePlan) KernelReport {
	var report KernelReport
	for _, st := range plan.missing {
		report.Missing = append(report.Missing, st.key)
	}
	for _, st := range plan.stale {
		report.Stale = append(report.Stale, st.key)
	}
	report.OrphanedEntities = plan.orphanedEntities
	report.OrphanedAspects = plan.orphanedAspects
	report.OrphanScanErr = plan.orphanScanErr
	report.StrandedOperatorEpochs = plan.strandedEpochs
	report.StrandedScanErr = plan.strandedScanErr
	return report
}

// KernelDrift reports kernel entries this binary builds that Core KV does not
// match: `missing` are absent outright, `stale` are present with a different
// body. It is the read-only twin of ReconcilePrimordial — same plan, no writes —
// so a verification gate can assert what the reconcile guarantees.
//
// Presence checks alone cannot see a stale kernel: a bucket seeded by an older
// binary holds every expected key, with valid envelopes and correct provenance,
// while running superseded DDL scripts. Drift is only visible by comparing
// stored content against built content.
func KernelDrift(ctx context.Context, kv jetstream.KeyValue) (missing, stale []string, err error) {
	report, err := ReadKernelReport(ctx, kv)
	if err != nil {
		return nil, nil, err
	}
	return report.Missing, report.Stale, nil
}

// KernelOrphans reports vtx.meta.* keys this binary no longer builds at all:
// `entities` are meta-vertex roots retired kernel has shed entirely;
// `aspects` are orphaned aspects of an entity whose root is still built
// (kernel-orphan-retirement-design.md §3.4 — never retired on their own,
// whole-entity retirement only). It is the read-only twin of KernelDrift over
// the same shared plan, so a verification gate reports exactly what a future
// repair pass would act on and the two can never disagree about what counts
// as orphaned.
//
// Unlike KernelDrift, which has no interest in the orphan listing's own
// health, KernelOrphans's entire contract IS the orphan report — so a failed
// listing (ReadKernelReport's OrphanScanErr) is surfaced as this function's
// own error rather than answered as a false "no orphans".
func KernelOrphans(ctx context.Context, kv jetstream.KeyValue) (entities, aspects []string, err error) {
	report, err := ReadKernelReport(ctx, kv)
	if err != nil {
		return nil, nil, err
	}
	if report.OrphanScanErr != nil {
		return nil, nil, report.OrphanScanErr
	}
	return report.OrphanedEntities, report.OrphanedAspects, nil
}

// sameDocument reports whether two stored envelope bodies carry the same
// content. The comparison is on the decoded JSON rather than the raw bytes so
// that a key-ordering or whitespace difference left by an older marshaller is
// not mistaken for a stale kernel — only a value difference is.
func sameDocument(stored, built []byte) (bool, error) {
	var b any
	if err := json.Unmarshal(built, &b); err != nil {
		return false, fmt.Errorf("decode built envelope: %w", err)
	}
	var a any
	if !json.Valid(stored) || json.Unmarshal(stored, &a) != nil {
		// An unparseable stored body cannot be compared, and leaving it in
		// place would strand the key behind a corrupt document. Treat it as
		// stale so the built definition replaces it.
		return false, nil
	}
	return reflect.DeepEqual(a, b), nil
}
