package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
type reconcilePlan struct {
	missing   []reconcileStep
	stale     []reconcileStep
	retained  []string
	unchanged int
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
	return plan, nil
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
	plan, err := planReconcile(ctx, kv)
	if err != nil {
		return nil, nil, err
	}
	for _, st := range plan.missing {
		missing = append(missing, st.key)
	}
	for _, st := range plan.stale {
		stale = append(stale, st.key)
	}
	return missing, stale, nil
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
