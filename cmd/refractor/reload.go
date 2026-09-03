package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
)

// reactivateRemedy names what an operator must actually do about one of the
// pinned spec changes hotReloadRefusal still refuses. Deleting and re-creating
// the lens definition is one way; for a package-installed lens, whose definition
// is re-authored by an upgrade rather than by hand, re-activating Refractor is
// the one that applies — activation reads the current spec and installs all of
// it.
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
	// So is whether the lens may write the D1 read-grant namespace. A
	// replacement that lost THAT would have every cap-read key it renders
	// refused: its retractions would fail while the grants they meant to
	// withdraw stayed live, in the over-grant direction, on a lens that had
	// merely been reinstalled with an unchanged cypher.
	if err := projection.ApplyReadGrantLicence(adpt, r); err != nil {
		return nil, err
	}
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
// nothing else. What it refuses is the narrower set that an in-place swap cannot
// carry AND that this path does not re-activate for: secureColumns, a secure
// lens's table/dsn, grantTable, protected, a guarded lens's write surface, and
// the authorization plane.
//
// The line between the two is what the edit does to rows that are already
// written, or to read-path posture. Each pin above leaves something behind that
// restarting the lens does not put right — grant rows no producer addresses
// afterwards, an RLS posture no swap re-verified, a decrypt set that decides
// which ciphertexts open — and the call about how to reconcile that belongs to
// an OPERATOR, not to whichever package upgrade happened to commit the spec.
// That is why the reason each returns names an operator action
// (reactivateRemedy) rather than performing one.
//
// An Output-descriptor edit is deliberately NOT among them. It changes the
// envelope, the delete-key derivation, the sweep plan and the guard predicate,
// which are installed once at activation and which neither swap re-runs — but
// the rows it leaves behind ARE reconcilable without a human: the purge and the
// full re-projection reloader.reactivate performs are exactly the reconciliation,
// and they are decidable from the two descriptors alone.
//
// Both kinds of update are held to every refusal below: a lens whose pinned
// fields and cypher change together reaches the MATCH path, where none of that
// installation is re-run either.
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
	// adapter, and a bare PostgresAdapter has it off. Two of the other three —
	// the auth-plane bucket and grantTable — are pinned above; the fourth, the
	// Output descriptor's tombstone empty-behavior, is not pinned at all.
	//
	// What the set is closed against is a SWAP: no edit can turn a live guarded
	// pipeline into an unguarded one while it runs. The empty-behavior arm can
	// genuinely retire the guard — `delete` edited to `emptyDoc` produces a lens
	// that needs none — and that is correct, because it goes the long way round:
	// re-activation purges the guarded rows the old watermarks ordered and then
	// rebuilds the adapter from the new rule through buildAdapter, so the guard
	// is re-derived from the spec rather than dropped out from under rows still
	// carrying it.
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
	// A lens's PLANE — whether it projects an authorization surface
	// (projection.IsAuthPlane: nats_kv into the capability bucket, or a Postgres
	// grant table) — is decided once, at activation, and recorded in three
	// places a swap re-derives none of: pipeline.authPlane (installLensPlane,
	// main.go), this entry's own authPlane (which routes the heartbeat between
	// the Capability-Lens and the business-lens severity tier), and the
	// Auditor's captured copy (whose enrolment refuses the plane outright). An
	// INTO edit moving a lens ONTO the plane leaves all three reading "business
	// read model" for an authorization surface — a monitoring tier, a divergence
	// detector and a narrowing licence each judging it as something it no longer
	// is — and one moving it OFF strands every capability row it wrote, which no
	// producer addresses afterwards.
	//
	// The remedy is a refusal rather than a re-derivation, deliberately: those
	// three holders are read from the handler and audit goroutines while a
	// reload runs on the dispatch goroutine, so re-assigning them here would be
	// a data race — whereas a deactivate-and-reactivate re-derives all three
	// through the one path that owns them. grantTable and protected are pinned
	// above for their own reasons; this closes the arm neither reaches, an
	// UNGUARDED lens moving its nats_kv bucket, which the guarded-surface pin
	// above lets through by design.
	if entry.authPlane != projection.IsAuthPlane(newLens) {
		return "lens update changes its authorization plane — not hot-reloadable (the plane is recorded at activation by the pipeline, its health entry and its auditor alike); " + reactivateRemedy
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

// replayCannotOverwrite reports whether the re-projection a re-activation runs
// would leave rows of the OLD shape standing — the question that decides whether
// the target must be purged first.
//
// Two ways it can. The keys can move, so the replay writes somewhere else
// entirely and nothing it does reaches what is already stored. Or the new
// descriptor can stop writing for an actor at all: emptyBehavior `skip` produces
// no write for an empty result, where every other behavior writes a document or
// a tombstone — so an actor that empties out keeps whatever the old descriptor
// last left at its key, forever.
//
// nil is a shape of its own: a descriptor arriving or departing changes how
// every key is derived.
func replayCannotOverwrite(a, b *lens.OutputDescriptorSpec) bool {
	if a == nil || b == nil {
		return a != b
	}
	if outputKeyShapeChanged(a, b) {
		return true
	}
	return b.EmptyBehavior == string(projection.EmptySkip) &&
		a.EmptyBehavior != string(projection.EmptySkip)
}

// outputKeyShapeChanged reports whether two §6.13 Output descriptors ADDRESS
// different keys.
//
// Four fields decide that, through two mechanisms. BuildKey renders the key from
// OutputKeyPattern, substituting the actor suffix — which KeyColumn narrows from
// `<type>.<id>` to the bare id. AnchorType and EntryKeyColumn do not reach
// BuildKey at all: they decide which keys the lens CLAIMS, through AnchorFromKey
// (whose vertex-type check is AnchorType) and through the per-entry split, which
// appends one more trailing token to every key EntryEnvelopeFn writes.
//
// A lens activated on a moved key shape cannot name the rows it already wrote,
// so no later delivery, sweep or tombstone will ever retract them.
func outputKeyShapeChanged(a, b *lens.OutputDescriptorSpec) bool {
	if a == nil || b == nil {
		return a != b
	}
	return a.AnchorType != b.AnchorType ||
		a.OutputKeyPattern != b.OutputKeyPattern ||
		a.KeyColumn != b.KeyColumn ||
		a.EntryKeyColumn != b.EntryKeyColumn
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
//
// resolver, when non-nil and r carries expansion labels, is consulted here to
// seed taxExpansion/taxExpansionStatus from the SAME answer activation's own
// UseFullEngineBranches call just used moments earlier (resolver state cannot
// have changed between the two — both run synchronously on one goroutine) —
// not left at the conservative (nil, StatusUnknown) zero value. Without this,
// the first live taxonomy event after every single activation would
// unconditionally look like a change and force a redundant durable
// delete-recreate + DeliverLastPerSubject replay + rebuilding health status,
// for a set that never actually differs from what the pipeline was activated
// against. nil is safe (and used by tests that construct entries directly): it
// just leaves the baseline at zero value, at worst costing one such redundant
// re-derivation the first time a real taxonomy event arrives.
func newPipelineEntry(
	r *lens.Rule,
	adpt adapter.Adapter,
	p *pipeline.Pipeline,
	reporter *health.Reporter,
	cancel context.CancelFunc,
	done chan struct{},
	resolver *taxonomy.Resolver,
) *pipelineEntry {
	labels := unionExpansionLabels(r)
	var expansion map[string]map[string]struct{}
	status := taxonomy.StatusUnknown
	if resolver != nil && len(labels) > 0 {
		expansion, _, status, _ = resolver.Expand(labels)
	}
	return &pipelineEntry{
		cancel:             cancel,
		done:               done,
		pipeline:           p,
		reporter:           reporter,
		canonicalName:      r.CanonicalName,
		authPlane:          projection.IsAuthPlane(r),
		guarded:            adapterIsGuarded(adpt),
		target:             r.Into.Target,
		bucket:             r.Into.Bucket,
		table:              r.Into.Table,
		dsn:                r.Into.DSN,
		protected:          r.Into.Protected,
		grantSource:        r.Into.GrantSource,
		grantTable:         r.Into.GrantTable,
		output:             r.Output,
		projectionKind:     r.ProjectionKind,
		secureColumns:      r.Into.SecureColumns,
		rule:               r,
		taxExpansionLabels: labels,
		taxExpansion:       expansion,
		taxExpansionStatus: status,
		// The shrink baseline is seeded from the same answer, and only when
		// that answer is resolved — an activation that could not resolve the
		// expansion has no set the gate is matching against to shrink FROM.
		taxExpansionResolved: resolvedExpansion(expansion, status),
	}
}

// resolvedExpansion returns expansion when status is resolved, and nil
// otherwise — the seed for pipelineEntry.taxExpansionResolved, which records
// only answers a filter can actually be built from.
func resolvedExpansion(expansion map[string]map[string]struct{}, status taxonomy.Status) map[string]map[string]struct{} {
	if status == taxonomy.StatusUnknown {
		return nil
	}
	return expansion
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

	// resolver is the taxonomy resolver every full-engine pipeline's `*`
	// expansion reads (installed via pipeline.SetTaxonomyResolver at
	// activation, cmd/refractor/main.go). taxonomyChanged calls its own
	// Expand — not merely relying on what UseFullEngineBranches computes
	// internally — because comparing against the PREVIOUS Expand answer
	// (entry.taxExpansion) is what tells a real set change from a no-op;
	// UseFullEngineBranches only ever sees "the current answer", with
	// nothing to diff against.
	resolver *taxonomy.Resolver

	// liveEntries snapshots the running pipeline registry under its own
	// lock — reloader has no direct handle on cmd/refractor's registry
	// map/mutex, so main.go supplies this closure the same way it supplies
	// lookup.
	liveEntries func() []*pipelineEntry

	// activateForTaxonomy re-attempts activation of a lens previously
	// refused at UseFullEngineBranches for an unknown taxonomy expansion
	// (main.go's startPipeline). Supplied so retryRefused can drive
	// activation without reloader importing startPipeline's closure-local
	// state (the registry, the adapter builder, etc). Must be the SAME
	// existence-checked entry point src.SetLoadCallback uses for a first
	// load (main.go wires it to that closure, not to the bare startPipeline
	// func) — otherwise a retry could reactivate a lens a concurrent load
	// already registered, racing two pipelines for one lens ID.
	activateForTaxonomy func(*lens.Rule)

	// deactivate stops a running lens and takes it out of the registry through
	// the exact removal a tombstone takes — remove the durable, cancel, wait for
	// Run to return, forget the cap-read producer, delete the health entry,
	// unregister from the control plane. main.go wires it to remover.stop, the
	// same mechanism it hands CoreKVSource for a tombstone. reactivate composes
	// it with activateForTaxonomy to carry an Output edit.
	//
	// It reports whether the lens actually STOPPED, and reactivate treats a
	// non-nil error as an abort rather than a step to log past: the durable
	// removal fails before the run context is cancelled, so a failed teardown
	// leaves the old pump alive, and activating over it would put two pipelines,
	// two durables and two writers on one lens ID. A nil deactivate is refused
	// there for the same reason.
	deactivate func(old *lens.Rule) error

	// ruleKnown reports whether CoreKVSource still has ruleID loaded
	// (src.Get's second return, ignoring the rule itself) — retryRefused's
	// belt to remover.remove/pipelineDeleter.Delete's eviction braces: a
	// rule tombstoned while queued in refused is evicted here instead of
	// retried, on the offchance some deletion path reaches the registry
	// without going through either of those two. nil in tests that supply
	// no source; retryRefused then retries every queued rule unconditionally.
	ruleKnown func(ruleID string) bool

	// taxRebuild is the bounded, coalescing scheduler every owed Rebuild runs
	// on — the one thing standing between a taxonomy event over a
	// hundred-lens corpus and a hundred simultaneous durable delete-recreates
	// (see taxonomyRebuildConcurrency). Usable at its zero value: it starts its
	// workers on the first enqueue, so the many tests that build a bare
	// &reloader{} need no constructor.
	taxRebuild taxonomyRebuildScheduler

	// rebuildPipeline performs one entry's rebuild. nil in production, where
	// runRebuild calls entry.pipeline.Rebuild directly; supplied by tests that
	// need to observe or block the rebuild itself — the fan-out bound is a
	// property of the SCHEDULER, and pinning it against real pipelines would
	// mean a hundred live JetStream consumers per assertion.
	rebuildPipeline func(entry *pipelineEntry, truncate bool) error

	refusedMu sync.Mutex
	// refused holds every lens whose activation failed at
	// UseFullEngineBranches specifically because its taxonomy expansion was
	// unknown (dynamic-type-taxonomy-design.md §14 Fire A item 4, §17.6's
	// "a boot refusal is never retried" precondition) — never any OTHER
	// activation failure. taxonomyChanged retries each entry here on every
	// taxonomy event; an entry is removed the moment its retry activates.
	refused map[string]*lens.Rule
}

// refuse tells the operator, in both places they look, that their edit did not
// land. The lens keeps running its activated spec, which is correct — so this
// is not a pause; pausing would take down a projection that is doing the right
// thing. It is an error on the lens's health record, which is what makes an
// edit that did not land distinguishable from one that applied.
func (rl *reloader) refuse(entry *pipelineEntry, lensID, reason string, attrs ...any) {
	rl.logger.Error(reason, append([]any{"lensId", lensID}, attrs...)...)
	// A lens with no health reporter still gets the log line. The guard is for
	// the entry shapes a harness builds directly; production activation always
	// supplies a reporter, so this can never silently downgrade a real refusal to
	// a log-only one.
	if entry.reporter == nil {
		return
	}
	// Recorded under health.HotReloadRefusalPrefix, which the log line above does
	// not carry: the prefix is what tells the clean-registration clear that this
	// verdict is retired by the next activation (which reads the current spec and
	// so settles the edit the refusal was about), and an operator reading the log
	// wants the reason, not the class marker.
	if err := entry.reporter.RecordError(rl.ctx, health.HotReloadRefusalPrefix+reason); err != nil {
		rl.logger.Error("record hot-reload refusal in health", "lensId", lensID, "err", err)
	}
}

// update applies a lens-definition change to the running pipeline: an
// INTO-only update swaps the adapter, a MATCH change swaps the compiled rule,
// and an Output-descriptor change re-activates the lens in place.
func (rl *reloader) update(old, newLens *lens.Rule, kind lens.UpdateKind) {
	entry, ok := rl.lookup(newLens.ID)
	if !ok {
		// A lens can be "unknown" to the registry for two different reasons:
		// genuinely never activated (or already removed) — the Warn below —
		// or REFUSED and queued in rl.refused pending a taxonomy event, in
		// which case this edit is the operator's fix and must not be
		// silently discarded in favor of the stale pre-edit rule the next
		// retry would otherwise activate (this file's own header states the
		// principle for the opposite direction: "a refused edit must not
		// become the baseline for the next one" — replacing the queued rule
		// here is that same principle applied to the queue itself).
		if rl.updateRefusedForTaxonomy(newLens) {
			rl.logger.Info("lens update replaces the pending rule queued while refused for taxonomy",
				"lensId", newLens.ID, "kind", kind)
			return
		}
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
	// An Output edit is carried by re-activation, not by either swap. It is
	// decided AFTER the refusal check for the same reason both swaps are: a lens
	// moving its Output together with something genuinely unswappable must still
	// be refused, and tearing the pipeline down first would stop a lens the
	// refusal meant to keep running. Both kinds arrive here — `output` is its own
	// aspect, so editing it alone leaves the Match string untouched and
	// classifies IntoOnly, while editing it beside the cypher classifies
	// MatchChange — and both need the same treatment, since neither swap
	// re-installs the envelope.
	//
	// projectionKind rides the same arm because it decides whether the descriptor
	// is installed AT ALL: activation's install switch dispatches on
	// projection.IsActorAggregate, so a lens edited into or out of the
	// actor-aggregate kind with a byte-identical Output needs exactly the same
	// re-installation, and no swap performs it.
	if !outputDescriptorsEqual(entry.output, newLens.Output) ||
		entry.projectionKind != newLens.ProjectionKind {
		rl.reactivate(entry, old, newLens)
		return
	}
	switch kind {
	case lens.IntoOnly:
		// Same transient-NATS-blip retry as activation's buildRuleAdapter
		// call (main.go's startPipeline) — a hot-reload burst hits the
		// identical adapter-build RTT, and refusing on one blip leaves the
		// lens running its stale spec until another edit happens to arrive.
		var newAdpt adapter.Adapter
		err := retryTransientBoot(rl.ctx, func() error {
			var buildErr error
			newAdpt, buildErr = rl.buildAdapter(newLens)
			return buildErr
		})
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
				if err := projection.ThreadKeyColumns(cr, newLens.CompiledBranches, keyCols); err != nil {
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
		// The label set the OLD rule admits, read before the swap replaces it:
		// two different compiled rules share no other currency, and comparing
		// what each ADMITS is what tells a MATCH edit that narrowed the set
		// from one that widened it.
		prevLabels, prevNarrowed := entry.pipeline.ConsumerFilterLabels()
		if err := entry.pipeline.UseFullEngineBranches(rl.fullEngine, newLens.CompiledRule, newLens.CompiledBranches); err != nil {
			rl.refuse(entry, newLens.ID, "label expansion (MATCH update)", "err", err)
			return
		}
		// A narrowing MATCH edit orphans rows for exactly the reason a taxonomy
		// shrink does, and by exactly the same mechanism: the new filter admits
		// no event for the labels it dropped, so no delivery can ever retract
		// the rows the old rule already projected for them. The rebuild this
		// arm already owes therefore has to truncate — see
		// pipelineEntry.taxRebuildTruncate for which target shapes that
		// actually retracts on, and which retract through DiffRetraction
		// instead.
		nextLabels, nextNarrowed := entry.pipeline.ConsumerFilterLabels()
		matchShrank := consumerFilterShrank(prevLabels, prevNarrowed, nextLabels, nextNarrowed) &&
			rl.truncateIsSafe(entry, newLens.ID)
		// The RUNNING pipeline now evaluates newLens — entry.rule (and the
		// taxonomy re-derivation baseline it feeds) must track it, not the
		// rule this reload started from (dynamic-type-taxonomy-design.md §14
		// Fire A item 4). Re-deriving the (map, Status) baseline HERE, from
		// the same resolver call UseFullEngineBranches just made internally,
		// avoids a spurious redundant re-derivation on the next unrelated
		// taxonomy event; leaving it at the conservative (nil, StatusUnknown)
		// zero value when there is no resolver (e.g. a test reloader) or no
		// `*` in the new rule is still safe — rederiveEntry no-ops on an
		// empty taxExpansionLabels, and any wrong guess elsewhere would only
		// cost one extra, never a missed, re-derivation.
		entry.rule = newLens
		entry.taxExpansionLabels = unionExpansionLabels(newLens)
		var newExpansion map[string]map[string]struct{}
		newStatus := taxonomy.StatusUnknown
		if rl.resolver != nil && len(entry.taxExpansionLabels) > 0 {
			newExpansion, _, newStatus, _ = rl.resolver.Expand(entry.taxExpansionLabels)
		}
		// taxMu-guarded: a rebuild goroutine from an EARLIER taxonomy
		// re-derivation on this same entry may still be in flight and read
		// these fields concurrently (entry.taxMu's own doc) — this write,
		// though it runs on the single dispatch goroutine like everything
		// else in update(), touches fields a background goroutine also
		// touches, so it takes the same lock.
		//
		// The shrink baseline tracks the new rule's answer too, so the next
		// taxonomy event compares against the labels the RUNNING rule expands,
		// not the ones the previous one did. An unresolved answer leaves it
		// alone (entry.taxExpansionResolved's own doc): the worst that costs is
		// one redundant truncating rebuild, when a later resolved answer for a
		// different label set reads as a shrink of the old one.
		entry.taxMu.Lock()
		entry.taxExpansion, entry.taxExpansionStatus = newExpansion, newStatus
		if resolved := resolvedExpansion(newExpansion, newStatus); resolved != nil {
			entry.taxExpansionResolved = resolved
		}
		entry.taxMu.Unlock()
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
		// A failure here is RECORDED on the lens's health entry, not merely
		// logged. This rebuild is the only thing that re-derives the Core KV
		// consumer filter after the label set above changed, and a JetStream
		// filter update never rewinds a consumer's cursor
		// (Pipeline.ConsumerFilter's doc) — so a rebuild that fails after a MATCH
		// edit WIDENED the label set leaves the live consumer narrowed to the OLD
		// set permanently: every write on a newly-referenced type is denied
		// delivery while the already-swapped client gate would have kept it. On
		// the auth plane that runs both ways — a grant the graph no longer
		// supports, with no retraction able to reach it. Unlike every other
		// refusal on this path the new spec has already been accepted by the time
		// this runs, so a log line was the only account of a lens now serving a
		// rule it cannot see all the events for.
		//
		// Driven through the same per-entry rebuild latch a taxonomy
		// re-derivation uses (taxonomy_reload.go): this arm publishes a client
		// gate and owes it a rebuild for exactly the same reason, and a MATCH
		// edit landing beside a taxonomy event would otherwise race two
		// rebuilds whose answers describe different gates.
		markTaxonomyRebuildPending(entry, matchShrank)
		rl.startTaxonomyRebuild(entry, newLens.ID,
			"MATCH hot-reload: rebuild failed — the Core KV consumer filter may still carry the pre-edit label set")
	}
}

// reactivate carries an Output-descriptor or projectionKind edit the one way a
// running lens can take one: it stops the lens and activates it again from the
// new spec.
//
// The envelope, the delete-key derivation, the sweep plan and the guard
// predicate the descriptor shapes are installed once, by
// projection.InstallActorAggregate at activation, and activation is the only
// path that owns them — the pipeline, this entry and the auditor each hold a
// copy, read from the handler and audit goroutines while a reload runs on the
// dispatch goroutine, so re-deriving them in place would be a data race rather
// than a repair.
//
// Composing the two halves here is safe because both already run on THIS
// goroutine: deactivate is the removal a tombstone drives and
// activateForTaxonomy the activation a first load drives. The registry is keyed
// by lens ID, so the old entry is gone before the new one can be inserted and
// the two can never coexist. The durable activation creates is fresh and starts
// at DeliverLastPerSubject, which is what makes this a full re-projection of the
// current corpus rather than a re-compile only future events would see.
func (rl *reloader) reactivate(entry *pipelineEntry, old, newLens *lens.Rule) {
	// Both halves, plus the rule the removal is driven from. Anything missing is a
	// REFUSAL rather than a skipped step: remover.stop reports "no rule to remove"
	// for a nil rule exactly as an unwired deactivate cannot remove anything, and
	// activating on the back of either would register a second pipeline for a lens
	// ID whose first one is still running — two durables and two writers on one
	// surface.
	if rl.deactivate == nil || rl.activateForTaxonomy == nil || old == nil {
		rl.refuse(entry, newLens.ID,
			"lens Output edit refused — re-activation cannot be driven (the running rule or a registry half is missing); the lens keeps its activated spec")
		return
	}
	if reason := reactivationPreflight(entry.target, newLens); reason != "" {
		rl.refuse(entry, newLens.ID, "lens Output edit refused — "+reason+"; the lens keeps its activated spec")
		return
	}

	// Read from the entry while it is still the live record of what the lens is
	// running; the teardown below retires it. A guarded target purges whatever
	// this answers (TruncateForReactivation's force rule), which is why the
	// pre-flight above — not a second conjunct here — is what keeps a purge off
	// a target that is not NATS-KV.
	requested := replayCannotOverwrite(entry.output, newLens.Output)

	if err := rl.deactivate(old); err != nil {
		// Nothing was running under this ID. Whoever removed it — a concurrent
		// operator delete, or an activation that never reached the registry —
		// decided the lens is going away, so this edit has nothing to re-activate
		// and no health entry to write to: update's own unknown-lens arm takes the
		// same view, and re-creating a record for a lens a delete has just taken
		// away would resurrect it in every operator view.
		if errors.Is(err, errLensNotRunning) {
			rl.logger.Warn("Output edit on a lens that is no longer running", "lensId", newLens.ID)
			return
		}
		rl.refuse(entry, newLens.ID,
			"lens Output edit refused — the running lens could not be stopped, so nothing may be started in its place; it keeps its activated spec",
			"err", err)
		return
	}
	// The teardown reported success, so Run has returned — pipelineDeleter.Delete
	// waits for exactly that. Asserting it rather than assuming it is what keeps
	// an old-shape write from landing after the purge, or a second pump from
	// writing beside the new one: an unclosed channel here means the deactivation
	// wired to this reloader is not the one that waits, which no amount of
	// activation would repair.
	select {
	case <-entry.done:
	default:
		rl.recordDark(entry, newLens.ID,
			"re-activation aborted — the lens was removed from the registry but its pipeline has not stopped, so no replacement was started; restart Refractor, or fix the spec and reinstall")
		return
	}
	rl.logger.Info("lens deactivated for an Output change", "lensId", newLens.ID)

	// Between Run returning and the replay starting — TruncateForReactivation's
	// own doc carries both halves of that ordering, and the guarded-force,
	// truncatability and ownership rules it applies. A failure is recorded and the
	// activation still runs: the replay re-derives whatever a partial purge
	// removed, while abandoning here would leave the lens dark AND its rows half
	// gone.
	// A descriptor ARRIVING is the one direction the purge cannot serve. The old
	// adapter carries no key prefix — ApplyTruncateScope derives one only for an
	// actor-aggregate lens — so the purge is declined as unconfined, and scoping
	// it off the NEW descriptor would be wrong twice over: the stranded rows are
	// the plain lens's, written under a key shape the new descriptor does not
	// claim. The re-activation still lands; what it cannot do is retract what the
	// plain lens left, which is the same thing a tombstone or a restart leaves
	// behind.
	if entry.output == nil && newLens.Output != nil {
		rl.logger.Warn("lens gained an Output descriptor: rows the plain lens wrote are not addressable by the new descriptor and are not retracted; remove them by hand if the old key shape must not linger",
			"lensId", newLens.ID, "outputKeyPattern", newLens.Output.OutputKeyPattern)
	}
	truncated, err := entry.pipeline.TruncateForReactivation(rl.ctx, requested)
	if err != nil {
		reason := "re-activation could not clear the target before the replay — rows the new Output no longer addresses may survive"
		rl.logger.Error(reason, "lensId", newLens.ID, "err", err)
		if recErr := entry.reporter.RecordError(rl.ctx, reason); recErr != nil {
			rl.logger.Error("record re-activation truncate failure in health", "lensId", newLens.ID, "err", recErr)
		}
	}
	rl.logger.Info("lens target cleared for re-activation", "lensId", newLens.ID, "truncated", truncated)

	rl.activateForTaxonomy(newLens)

	// A lens that is neither registered nor queued for a taxonomy retry is dark.
	// A lens queued for taxonomy is NOT dark in this sense — it is refused pending
	// an expansion the taxonomy has yet to supply, which retryRefused drives on
	// its own, exactly as a first load would.
	rl.refusedMu.Lock()
	_, queued := rl.refused[newLens.ID]
	rl.refusedMu.Unlock()
	if _, live := rl.lookup(newLens.ID); !live && !queued {
		rl.recordDark(entry, newLens.ID,
			"re-activation after an Output change failed — the lens is dark; restart Refractor, or fix the spec and reinstall (the entry resumes on its own once a pipeline is behind it)")
		return
	}
	rl.logger.Info("lens re-activated after an Output change", "lensId", newLens.ID)
}

// natsKVTarget is the Rule-side spelling of the NATS-KV target
// (lens.translateSpec's `nats_kv` arm — the package DSL's own `nats-kv` is
// normalized before a Rule ever exists).
const natsKVTarget = "nats_kv"

// reactivationPreflight runs the checks a re-activation must pass before
// anything is torn down, and returns the reason it would be refused, or "".
//
// It exists so a malformed edit leaves the OLD lens running rather than stopping
// it for an activation that was always going to refuse — and so that the purge
// between the two halves can never run against a target this path has no
// business clearing. Everything here is decidable from the running target and
// the new rule alone; everything startPipeline refuses on that is NOT here is
// about the substrate — a Vault backend, a target that will not open — which an
// Output edit does not move.
//
// BOTH targets must be NATS-KV, and that is the whole of the guard rather than a
// flag consulted later. The Output descriptor's only home is a NATS-KV bucket:
// the write guard (EnableProjectionGuard fails closed on any other adapter), the
// perEntry key listing (adapter.PrefixKeyLister), the row read-back
// (adapter.RowReader) and the retraction and sweep transports built on that pair
// are all NATS-KV capabilities. Checking only the NEW rule would leave the
// dangerous direction open, because the purge runs on the OLD adapter and a
// PROTECTED Postgres adapter answers yes to every question
// TruncateForReactivation asks — NewProtectedAdapter forces the §6.2 guard on
// it, so resolveTruncate FORCES the purge whatever the caller requested; it
// implements Truncater; and having no key prefix it counts as owning its target
// outright. A projectionKind flip OUT of actorAggregate on such a lens skips
// every new-rule descriptor check and would truncate a live RLS table.
func reactivationPreflight(oldTarget string, newLens *lens.Rule) string {
	if oldTarget != natsKVTarget || newLens.Into.Target != natsKVTarget {
		return "an Output descriptor lives only on a " + natsKVTarget +
			" target (the write guard, the per-entry key listing and the row read-back are all NATS-KV capabilities), and re-activating across any other target would purge rows the replay could never re-derive — this lens runs on " +
			targetName(oldTarget) + " and its edit declares " + targetName(newLens.Into.Target)
	}
	if projection.IsActorAggregate(newLens) {
		if _, err := projection.Compile(newLens); err != nil {
			return "the new descriptor does not compile: " + err.Error()
		}
	}
	if refusal := projection.CapReadWriterRefusal(newLens); refusal != "" {
		return refusal
	}
	return ""
}

// targetName renders a lens target for an operator-facing reason, naming an
// unset one rather than leaving a blank in the sentence.
func targetName(target string) string {
	if target == "" {
		return "(no target)"
	}
	return target
}

// recordDark reports a lens that is not running and has no pipeline behind it.
//
// It is two writes because the two say different things and both are read. The
// error is the diagnosis, on the counter and latch `lattice health summary` and
// Loupe's fault conjunct already consume. The pause is the STATE: without it
// RecordError re-creates the entry a teardown deleted at its `active` default,
// so a lens with nothing running would read healthy.
//
// The pause reason is INFRA, and the choice is about recovery rather than
// severity. A structural pause is served by the supervisor's waitWhilePaused —
// held until an operator issues `resume`, and restored across restarts by the
// health sink, so a restart would not bring the lens back. An infra pause runs
// the probe loop instead (substrate.ConsumerSupervisor's pump): the next
// activation restores it, probes the target, passes and resumes on its own, and
// the SetActive that follows clears the diagnosis with it. The remedy an
// operator is told about — restart Refractor, or fix the spec and reinstall — is
// then the remedy that actually works.
//
// entry is the RETIRED entry: its health record was deleted with the pipeline,
// and both writes re-create it. That is the only reporter that outlives the
// teardown.
func (rl *reloader) recordDark(entry *pipelineEntry, lensID, reason string) {
	rl.logger.Error(reason, "lensId", lensID)
	if entry.reporter == nil {
		return
	}
	if err := entry.reporter.RecordError(rl.ctx, reason); err != nil {
		rl.logger.Error("record re-activation failure in health", "lensId", lensID, "err", err)
	}
	if err := entry.reporter.SetPaused(rl.ctx, health.PauseReasonInfra, reason); err != nil {
		rl.logger.Error("record re-activation failure as a pause", "lensId", lensID, "err", err)
	}
}
