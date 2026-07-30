package main

import (
	"context"
	"log/slog"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// reactivateRemedy names what an operator must actually do when a spec change
// cannot be applied to a running pipeline. Deleting and re-creating the lens
// definition is one way; for a package-installed lens, whose definition is
// re-authored by an upgrade rather than by hand, re-activating Refractor is the
// one that applies — activation reads the current spec and installs all of it.
const reactivateRemedy = "the lens must be re-activated (restart Refractor, or delete and re-create the lens definition)"

// targetBuilder opens a rule's write target. The concrete implementation needs
// the process's substrate connection and pool manager; taking it as a function
// is what lets the guard-binding step above it be exercised without either.
type targetBuilder func(*lens.Rule) (adapter.Adapter, error)

// buildAdapter opens the rule's target and binds the rule's §6.2 guard
// requirement to it. The guard is a property of the RULE, so it belongs to
// every adapter built for that rule, whichever target the rule names and
// whichever path is building it — activation, or the replacement an INTO-only
// hot reload swaps in. A rule that requires the guard on a target that cannot
// enforce it fails to build, which keeps the lens off rather than running it
// open.
func buildAdapter(r *lens.Rule, buildTarget targetBuilder) (adapter.Adapter, error) {
	adpt, err := buildTarget(r)
	if err != nil {
		return nil, err
	}
	if err := projection.ApplyGuard(adpt, r); err != nil {
		return nil, err
	}
	// Which keys the lens owns is a rule property too, and a replacement that
	// lost it would purge a shared bucket whole on its next rebuild.
	projection.ApplyTruncateScope(adpt, r)
	return adpt, nil
}

// adapterIsGuarded reports whether a built adapter enforces the §6.2
// projection-write ordering token — the same question HotReloadInto asks of a
// replacement. An adapter is the only honest source for it: the rule-level
// predicate (projection.RequiresGuard) answers false for any lens that is not
// an actor-aggregate, which excludes the whole grant-lens family even though
// the grant writer's SQL carries the guard unconditionally.
func adapterIsGuarded(adpt adapter.Adapter) bool {
	guard, reports := adpt.(adapter.SeqGuarded)
	return reports && guard.Guarded()
}

// hotReloadRefusal reports why an update cannot be applied to the running
// pipeline, or "" when it can be. The reason it returns is what the operator is
// told, in the log and in health alike.
//
// Every comparison is against the RUNNING pipeline's activated values
// (entry.*) — the spec the source last SAW is not the spec the pipeline last
// APPLIED, because the source records a new revision whether or not this
// refuses it. Comparing against that would let a refused edit become the
// baseline for the next one: edit A is refused, edit B changes something else,
// and A rides in unexamined.
//
// A hot reload swaps the adapter (INTO-only) or the compiled rule (MATCH), and
// nothing else. So the refusals are exactly the changes neither swap can carry:
// a change to what the pipeline decrypts, a change to the envelope and fan-out
// wiring installed once at activation, and a change of the surface — or the
// identity — a lens has already written keys to and can no longer retract.
// Both kinds of update are held to all of them: a lens whose Output and cypher
// change together reaches the MATCH path, where the envelope is re-installed
// exactly as little as it is on the INTO path.
func hotReloadRefusal(entry *pipelineEntry, newLens *lens.Rule) string {
	// A live pipeline's secure decryptor is fixed at activation (installing one
	// mid-run would race the handler), so changing secureColumns needs a lens
	// re-create rather than a swap that leaves the decrypt set stale. A MATCH
	// edit reaches here too: ClassifyUpdate keys on the Match string alone, so
	// an update changing the cypher AND secureColumns is still a MATCH change.
	if !secureColumnsEqual(entry.secureColumns, newLens.Into.SecureColumns) {
		return "lens update changes secureColumns — not hot-reloadable; " + reactivateRemedy
	}
	// A secure lens also refuses table/DSN swaps: hot-reload has no
	// verify-and-pause, so the new target's RLS posture would be unprobed while
	// the rows carry decrypted PII.
	if len(entry.secureColumns) > 0 &&
		(entry.table != newLens.Into.Table || entry.dsn != newLens.Into.DSN) {
		return "secure lens update changes table/dsn — not hot-reloadable (no RLS re-verify on swap); " + reactivateRemedy
	}
	// The §6.13 Output descriptor shapes the on-wire envelope, the delete-key
	// derivation, the sweep plan and the guard predicate — all installed once,
	// at activation, by InstallActorAggregate. An INTO-only update rebuilds the
	// adapter and re-runs none of it, so accepting an Output edit would leave
	// the live envelope emitting the activated empty-behavior into an adapter
	// built for the new one. `output` is a separate aspect from `into`, so
	// editing it alone reaches this path with the Match clause untouched — and
	// editing it TOGETHER with the cypher reaches the MATCH path, which
	// re-installs the envelope no more than the INTO path does.
	if !outputDescriptorsEqual(entry.output, newLens.Output) {
		return "lens Output descriptor changed — not hot-reloadable (the envelope, delete key and sweep plan are installed at activation); " + reactivateRemedy
	}
	// Whether a lens projects the shared actor_read_grants table is its
	// identity, not its INTO config. Flipping it off strands every grant row
	// the lens wrote — no producer addresses that grant_source afterwards, so
	// diff retraction can never revoke them, and the rows stay live in the
	// table every protected read consults. Flipping it on is the same move in
	// reverse. Neither is a continuation of the running lens.
	if entry.grantTable != newLens.Into.GrantTable {
		return "lens update changes grantTable — not hot-reloadable (the rows the lens already wrote become unaddressable); " + reactivateRemedy
	}
	// `protected` is the fourth thing that decides whether the built adapter
	// carries the §6.2 guard: NewProtectedAdapter forces it on the inner
	// adapter, and a bare PostgresAdapter has it off. The other three — the
	// auth-plane bucket, the Output descriptor's tombstone empty-behavior, and
	// grantTable — are each pinned above, so pinning this one closes the set,
	// and a guarded lens cannot be edited into an unguarded one.
	//
	// This is the pin that has no backstop underneath it. HotReloadInto refuses
	// an unguarded replacement only once RequireGuardedAdapter has armed the
	// pipeline, and its sole caller is InstallActorAggregate — which never runs
	// for a protected postgres lens (it cannot: the guard-enabler requires a
	// NATS-KV adapter). So for exactly the family whose whole purpose is
	// read-path authorization, dropping `protected` would silently retire the
	// monotonic write guard on a live read model.
	if entry.protected != newLens.Into.Protected {
		return "lens update changes protected — not hot-reloadable (it would retire the §6.2 write guard on a live read model); " + reactivateRemedy
	}
	// A guarded lens is pinned to the surface it was activated against. The
	// §6.2 guard orders each write against what is already stored at the
	// target, so moving the target strands every key the lens wrote there —
	// unretractable, since the lens no longer addresses them — and a
	// replacement target's own ordering is a different mechanism, not a
	// continuation of this one. grant_source is part of that surface: it is
	// what scopes a grant lens's writes and its retraction enumeration to its
	// own rows in a table it shares.
	if entry.guarded && (entry.target != newLens.Into.Target ||
		entry.bucket != newLens.Into.Bucket ||
		entry.table != newLens.Into.Table ||
		entry.dsn != newLens.Into.DSN ||
		entry.grantSource != newLens.Into.GrantSource) {
		return "guarded lens update changes its write surface — not hot-reloadable (the guard binds to the surface the lens already wrote); " + reactivateRemedy
	}
	return ""
}

// outputDescriptorsEqual reports whether two §6.13 Output descriptors mean the
// same thing. It compares each slice field the way its consumer reads it:
// BodyColumns and StaticEmptyColumns name keys the envelope writes into a map,
// so their order carries nothing and a reorder is not a change; Lanes is
// emitted verbatim as the document's `lanes` array, so its order is content.
func outputDescriptorsEqual(a, b *lens.OutputDescriptorSpec) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.AnchorType == b.AnchorType &&
		a.OutputKeyPattern == b.OutputKeyPattern &&
		a.EmptyBehavior == b.EmptyBehavior &&
		a.RealnessFilter == b.RealnessFilter &&
		a.Freshness == b.Freshness &&
		a.KeyColumn == b.KeyColumn &&
		a.EntryKeyColumn == b.EntryKeyColumn &&
		actorFieldOf(a) == actorFieldOf(b) &&
		sameColumnSet(a.BodyColumns, b.BodyColumns) &&
		stringsEqual(a.Lanes, b.Lanes) &&
		sameColumnSet(a.StaticEmptyColumns, b.StaticEmptyColumns)
}

// actorFieldOf resolves the descriptor's top-level actor field to the value the
// envelope will actually carry. ParseOutputDescriptor defaults a blank one to
// "actor", so spelling the default out is not an edit.
func actorFieldOf(d *lens.OutputDescriptorSpec) string {
	if d.ActorField == "" {
		return "actor"
	}
	return d.ActorField
}

// sameColumnSet compares two column lists as multisets — same names, same
// multiplicities, any order.
func sameColumnSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// newPipelineEntry snapshots what the lens was ACTIVATED with — the baseline
// every later hot-reload decision compares against.
//
// `guarded` is read from the adapter that was actually built, not from the
// rule-level predicate: projection.RequiresGuard answers false for any lens
// that is not an actor-aggregate — which is every grant lens, even though the
// grant writer's SQL carries the ordering token unconditionally. The adapter is
// therefore the only source that arms the surface pin for the one family whose
// lenses share a single table with five other producers, where a lens moved off
// its surface leaves rows no producer can address.
func newPipelineEntry(
	r *lens.Rule,
	adpt adapter.Adapter,
	p *pipeline.Pipeline,
	reporter *health.Reporter,
	cancel context.CancelFunc,
	done chan struct{},
) *pipelineEntry {
	return &pipelineEntry{
		cancel:        cancel,
		done:          done,
		pipeline:      p,
		reporter:      reporter,
		canonicalName: r.CanonicalName,
		authPlane:     projection.IsAuthPlane(r),
		guarded:       adapterIsGuarded(adpt),
		target:        r.Into.Target,
		bucket:        r.Into.Bucket,
		table:         r.Into.Table,
		dsn:           r.Into.DSN,
		protected:     r.Into.Protected,
		grantSource:   r.Into.GrantSource,
		grantTable:    r.Into.GrantTable,
		output:        r.Output,
		secureColumns: r.Into.SecureColumns,
	}
}

// reloader applies a lens-definition update to the running pipeline registry.
// Its dependencies are injected rather than captured so the decisions it makes
// — every one of which is a refusal that keeps a lens correct — are reachable
// from a test without a substrate.
type reloader struct {
	ctx          context.Context
	logger       *slog.Logger
	lookup       func(lensID string) (*pipelineEntry, bool)
	buildAdapter func(*lens.Rule) (adapter.Adapter, error)
	fullEngine   *full.Engine
}

// refuse tells the operator, in both places they look, that their edit did not
// land. The lens keeps running its activated spec, which is correct — so this
// is not a pause; pausing would take down a projection that is doing the right
// thing. It is an error on the lens's health record, which is what makes an
// edit that did not land distinguishable from one that applied.
func (rl *reloader) refuse(entry *pipelineEntry, lensID, reason string, attrs ...any) {
	rl.logger.Error(reason, append([]any{"lensId", lensID}, attrs...)...)
	if err := entry.reporter.RecordError(rl.ctx, reason); err != nil {
		rl.logger.Error("record hot-reload refusal in health", "lensId", lensID, "err", err)
	}
}

// update applies a lens-definition change to the running pipeline: an
// INTO-only update swaps the adapter, a MATCH change swaps the compiled rule.
func (rl *reloader) update(_, newLens *lens.Rule, kind lens.UpdateKind) {
	entry, ok := rl.lookup(newLens.ID)
	if !ok {
		rl.logger.Warn("update on unknown lens", "lensId", newLens.ID, "kind", kind)
		return
	}
	// Both kinds face the same question — can a swap carry this edit? — so both
	// ask it, and neither can be used to smuggle in what the other refuses.
	// Deciding first also means a refused update never opens a target, which
	// would leave an auto-created bucket behind once per redelivery.
	if reason := hotReloadRefusal(entry, newLens); reason != "" {
		rl.refuse(entry, newLens.ID, reason,
			"target", entry.target, "bucket", entry.bucket, "table", entry.table)
		return
	}
	switch kind {
	case lens.IntoOnly:
		newAdpt, err := rl.buildAdapter(newLens)
		if err != nil {
			rl.refuse(entry, newLens.ID, "build new adapter", "err", err)
			return
		}
		// The pipeline's own guard requirement, checked against the adapter
		// actually built. The refusals above cover every rule edit that can
		// drop the guard, so reaching this is a backstop, not a live path.
		if err := entry.pipeline.HotReloadInto(newAdpt); err != nil {
			rl.refuse(entry, newLens.ID, "hot-reload adapter", "err", err)
			return
		}
		entry.reporter.SetRuleSequence(newLens.Sequence)
		entry.reporter.SetRuleEngine(newLens.ResolvedEngine)
		rl.logger.Info("lens INTO hot-reloaded", "lensId", newLens.ID)
	case lens.MatchChange:
		// CoreKVSource has already compiled the new rule; reuse it.
		if newLens.CompiledRule == nil {
			rl.refuse(entry, newLens.ID, "MATCH update missing CompiledRule")
			return
		}
		if cr, ok := newLens.CompiledRule.(*full.CompiledRule); ok {
			// A Personal Lens is exempt from threading Into.Key verbatim
			// ("__actor" is envelope-injected and never a RETURN alias), but it
			// still needs its BUSINESS key columns — the activation path sets
			// exactly these in InstallPersonalLens. Leaving them unset drops
			// the executor to its first-RETURN-item fallback, so a multi-key
			// lens emits a keys map without its business keys and the adapter
			// rejects every write, forever.
			keyCols, threaded := hotReloadKeyColumns(newLens)
			if threaded {
				cr.KeyColumns = keyCols
				if err := cr.ValidateKeyColumns(); err != nil {
					rl.refuse(entry, newLens.ID, "full engine key-column validation (MATCH update)", "err", err)
					return
				}
				// The new cypher must still RETURN every alias the running
				// decryptor consumes.
				if err := cr.ValidateReturnAliases(secureAliasNames(entry.secureColumns)...); err != nil {
					rl.refuse(entry, newLens.ID, "secure-column RETURN-alias validation (MATCH update)", "err", err)
					return
				}
			}
		}
		entry.pipeline.UseFullEngineBranches(rl.fullEngine, newLens.CompiledRule, newLens.CompiledBranches)
		rl.logger.Info("lens MATCH hot-reloaded", "lensId", newLens.ID)
		entry.reporter.SetRuleSequence(newLens.Sequence)
		entry.reporter.SetRuleEngine(newLens.ResolvedEngine)
		// The swap above only changes which rule FUTURE events evaluate against.
		// A MATCH edit can change which already-stored Core KV entries should be
		// projected, so the existing corpus needs the same rescan an operator's
		// "rebuild" control op performs — otherwise a package-driven MATCH change
		// silently "succeeds" while already-projected rows drift from the new
		// rule until someone notices and restarts Refractor. Async, mirroring
		// control.Service.rebuildRule: Reset() is a network round trip, and this
		// runs on CoreKVSource's single dispatch goroutine, which every other
		// lens's spec reload also shares.
		go func() {
			if err := entry.pipeline.Rebuild(rl.ctx, false); err != nil {
				rl.logger.Error("MATCH hot-reload: trigger rebuild", "lensId", newLens.ID, "err", err)
			}
		}()
	}
}
