package projection

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/capabilityread"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// PersonalActorType is the recipient vertex type the Personal Lens fan-out
// enumerates — always "identity" (personal-secure-lens-design.md §3.3: "same
// enumerator, configured actorType: identity").
const PersonalActorType = "identity"

// IsPersonalLens reports whether a lens rule opts a "nats_subject" target
// into the Fire 2 cross-vertex fan-out. Routing keys only off this
// lens-definition aspect, never off the canonical name.
func IsPersonalLens(r *lens.Rule) bool {
	return r != nil && r.Into.Target == "nats_subject" && r.Into.Personal
}

// PersonalBusinessKeys returns the key columns a Personal Lens's cypher
// actually RETURNs: Into.Key minus the reserved "__actor" field, which the
// envelope injects at write time and which is therefore never a RETURN alias.
//
// Every path that threads key columns onto a Personal Lens's compiled rule
// must use this, not Into.Key. Threading the full Into.Key fails
// ValidateKeyColumns on "__actor"; threading nothing leaves KeyColumns empty,
// which drops the executor to its single-key fallback (the first RETURN item)
// and produces a keys map missing every business key — the adapter then
// rejects every write with "key field %q absent from keys map", and a
// multi-key Personal Lens retries that failure for as long as it runs.
func PersonalBusinessKeys(r *lens.Rule) []string {
	if r == nil {
		return nil
	}
	businessKeys := make([]string, 0, len(r.Into.Key))
	for _, k := range r.Into.Key {
		if k == adapter.PersonalActorKeyField {
			continue
		}
		businessKeys = append(businessKeys, k)
	}
	return businessKeys
}

// ThreadKeyColumns sets keyCols as cr's projection output key and, for a
// multi-branch Personal lens, every branch in branches — not just cr.
// refractor-shared-keyspace-arbitration-design.md §13.2 compiles each
// pkgmgr Walks entry as an independently-allocated *full.CompiledRule;
// branches[0] shares cr's pointer (setting it again is a harmless no-op),
// but branches 1..N are distinct objects. Threading KeyColumns onto cr alone
// leaves those distinct objects nil, which drops their executor to the
// first-RETURN-item fallback key — every row a non-zero branch produces then
// carries a keys map missing whichever business key isn't that first alias,
// and the adapter rejects the write with "key field %q absent from keys map"
// (live 2026-07-30: edgeCatalog/edgeTasks/edgeEntitySessions's role-walk
// branch dropping "ns"). Returns the first validation failure, if any.
func ThreadKeyColumns(cr *full.CompiledRule, branches []ruleengine.CompiledRule, keyCols []string) error {
	cr.KeyColumns = keyCols
	if err := cr.ValidateKeyColumns(); err != nil {
		return err
	}
	for _, b := range branches {
		bcr, ok := b.(*full.CompiledRule)
		if !ok {
			continue
		}
		bcr.KeyColumns = keyCols
		if err := bcr.ValidateKeyColumns(); err != nil {
			return err
		}
	}
	return nil
}

// InstallPersonalLens wires the Fire 2 personal pipeline
// (personal-secure-lens-design.md §3.3): the existing ActorEnumerator
// (actorType "identity") drives per-recipient re-execution of the lens
// cypher, and a personal envelope injects the enumerated recipient into the
// adapter's reserved "__actor" key field — the cypher itself declares only
// its business key columns (Into.Key minus "__actor").
//
// interestKV is the personal-lens-interest bucket handle; nil disables the
// Interest Set relevance filter (every delta streams — the fail-open default
// the design specifies for "no registration yet"). capKV is the Capability KV
// bucket handle (Contract #6 §6.14); nil disables the D1 read-grant security
// gate — the design's Fires 1-2 trusted-single-identity posture, so tests
// exercising only fan-out/relevance may still pass nil, but a production
// caller MUST thread a real handle (personal-secure-lens-design.md §3.4, Fire
// PL.3: "the security door is the only thing that needs D1; it is the
// explicit gate, not a silent default"). requireReadGate makes that
// requirement fail-closed: when true, a nil capKV REFUSES registration rather
// than installing the lens open (edge-lattice-full-design.md §8.1 RR-3) — the
// production wiring (cmd/refractor) passes true, the trusted/test posture
// passes false. Returns false when the lens must not be registered (a
// fail-closed descriptor/engine/posture error).
func InstallPersonalLens(p *pipeline.Pipeline, r *lens.Rule, adjKV, coreKV, interestKV, capKV *substrate.KV, requireReadGate bool, logger *slog.Logger) bool {
	cr, ok := r.CompiledRule.(*full.CompiledRule)
	if !ok {
		logger.Error("personal lens requires the full engine", "lensId", r.ID)
		return false
	}

	if capKV == nil {
		if requireReadGate {
			logger.Error("personal lens registration REFUSED: the D1 read-grant security gate (capKV) is required in this posture but was not threaded — a personal lens must never run open in production",
				"lensId", r.ID)
			return false
		}
		logger.Warn("personal lens installed WITHOUT the D1 read-grant security gate — trusted/test-only posture, never production",
			"lensId", r.ID)
	}

	businessKeys := PersonalBusinessKeys(r)
	if err := ThreadKeyColumns(cr, r.CompiledBranches, businessKeys); err != nil {
		logger.Error("personal lens key-column validation", "lensId", r.ID, "err", err)
		return false
	}

	p.SetEnvelopeFn(personalEnvelopeFn(interestKV, capKV, logger))
	p.SetEnvelopeScope(personalEnvelopeScope(interestKV, capKV))
	p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, PersonalActorType))

	logger.Info("personal lens fan-out + envelope installed",
		"lensId", r.ID, "businessKeys", businessKeys, "interestSetFilter", interestKV != nil, "readGrantGate", capKV != nil)
	return true
}

// personalScopeGrantsParam and personalScopeInterestParam are the evaluation-
// parameter entries personalEnvelopeScope publishes and personalEnvelopeFn
// answers its two gates from. They are unexported and dotted: a cypher
// parameter is an identifier, so neither spelling can name one, and the
// pipeline hands these to the envelope through a copy of the parameters the
// engine never sees.
//
// The values are one evaluation's security-plane state — a *AnchorSet and the
// identity's registrations — and their lifetime is that evaluation. Nothing
// caches them; a set outliving the evaluation that read it would keep
// honouring a grant after its revocation landed.
const (
	personalScopeGrantsParam   = "projection.personal.readableAnchors"
	personalScopeInterestParam = "projection.personal.registrations"
)

// personalScopeReadTimeout bounds the whole per-evaluation gate read.
//
// A wide actor's grant set is past the multi-get's 1,024-subject fast path, so
// the read runs as a consumer drain, and that drain is bounded by the caller's
// deadline or — with none — by the primitive's own 80-second ceiling
// (substrate.Conn.KVGetMultiNoSnapshot). An evaluation is on the consumer's hot
// path: 80 seconds of a starved drain is the whole lens stalled with nothing
// said about it. Exceeding this bound is a loud evaluation error instead, which
// the pipeline already classifies and Naks for redelivery. The widest live
// actor's drain (3,644 keys) completes in well under a second, so the bound is
// two orders of magnitude of headroom, not a tuning parameter.
const personalScopeReadTimeout = 15 * time.Second

// personalEnvelopeScope builds the EnvelopeScopeFn that answers a whole
// actor's evaluation from ONE read per gate instead of one per row: the
// actor's readable-anchor set (D1) and the identity's Interest Set
// registrations. A personal lens re-projects every row an actor holds on each
// event it binds, and the widest actors hold thousands, so the per-row reads
// personalEnvelopeFn falls back to cost thousands of serial round trips for
// one event.
//
// Both entries are computed from the same actorKey the envelope parses, and a
// gate whose KV handle is nil contributes nothing — the envelope's own nil
// checks are what decide whether a gate runs at all, and this must not turn a
// disabled gate into an enabled one or the reverse. A read failure fails the
// evaluation, so a gate that could not be read never reads as a gate that
// admitted; both reads run under personalScopeReadTimeout, so a starved one
// fails loudly rather than holding the consumer.
func personalEnvelopeScope(interestKV, capKV *substrate.KV) pipeline.EnvelopeScopeFn {
	return func(ctx context.Context, params map[string]any) (map[string]any, error) {
		actorKey, _ := params["actorKey"].(string)
		if actorKey == "" {
			return nil, nil
		}
		actorType, actorID, ok := substrate.ParseVertexKey(actorKey)
		if !ok {
			return nil, fmt.Errorf("projection: personal lens actorKey %q is not a Contract #1 vertex key", actorKey)
		}

		ctx, cancel := context.WithTimeout(ctx, personalScopeReadTimeout)
		defer cancel()

		scope := make(map[string]any, 2)
		if capKV != nil {
			// grant-change-posture: (subscribed) the cap-read producers carry
			// the grant-change edge (IsReadGrantProducer wires the sink in
			// InstallActorAggregate), so a grant landing or being withdrawn
			// re-drives this actor's personal pipelines through
			// Pipeline.ReprojectPersonalActor rather than waiting for an
			// unrelated Core KV event to happen to re-ask this gate.
			readable, err := capabilityread.ReadableAnchors(ctx, capKV, actorType, actorID)
			if err != nil {
				return nil, fmt.Errorf("projection: personal lens read-grant scope for %q: %w", actorID, err)
			}
			scope[personalScopeGrantsParam] = readable
		}
		if interestKV != nil {
			// interest-change-posture: (subscribed) every writer of the
			// Interest Set announces on the change edge — the control plane's
			// register and deregister ops, and the health InterestReconciler's
			// orphan reap — so a device that narrows or widens what it wants
			// has this actor's personal pipelines re-driven through
			// Pipeline.ReprojectPersonalActor rather than waiting for the
			// convergence sweep to come round.
			regs, err := personalinterest.Registrations(ctx, interestKV, actorID)
			if err != nil {
				return nil, fmt.Errorf("projection: personal lens interest-set scope for %q: %w", actorID, err)
			}
			scope[personalScopeInterestParam] = regs
		}
		return scope, nil
	}
}

// personalEnvelopeFn builds the EnvelopeFn that turns a fan-out re-execution's
// row into the delta the NatsSubjectAdapter publishes: it injects the
// enumerated recipient into the reserved "__actor" key field and applies the
// Interest Set relevance filter (skip, not error, when a device's declared
// filter excludes this anchor — personal-secure-lens-design.md §3.3 step 2).
// The row itself passes through unchanged; NatsSubjectAdapter.Upsert derives
// anchor/kind/class from the RETURN aliases the lens author supplies.
//
// A $actorKey-scoped traversal that matches no neighbor still yields one
// degenerate row with every traversal-side column null (the same delegation-
// pattern behavior actor-aggregate lenses guard against, driver.go's
// EnvelopeFn doc) — recognized here by an empty "anchor" alias and declined
// (ErrSkipProjection) rather than published as a hollow delta. A personal
// lens's cypher must therefore always alias its neighbor's key to "anchor".
//
// The D1 read-grant check (capKV) runs before the Interest Set relevance
// filter and wins over it — a delta an actor has no capability to read is
// denied even if some device's Interest Set declares it relevant
// (personal-secure-lens-design.md §3.4: "security filter wins over
// relevance").
//
// Both gates are answered from the per-evaluation scope
// (personalEnvelopeScope) when the pipeline hands one down: the same
// predicates over the same keys, read once for the whole actor rather than
// once per row. Absent a scope — a caller that installed the envelope alone —
// each gate reads live per row, which is the same answer at a higher cost.
// Which gates run at all is decided by capKV/interestKV here, never by what
// the scope happens to carry.
func personalEnvelopeFn(interestKV, capKV *substrate.KV, logger *slog.Logger) pipeline.EnvelopeFn {
	return func(row map[string]any, keys map[string]any, params map[string]any) (map[string]any, map[string]any, error) {
		actorKey, _ := params["actorKey"].(string)
		if actorKey == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}
		actorType, actorID, ok := substrate.ParseVertexKey(actorKey)
		if !ok {
			return nil, nil, fmt.Errorf("projection: personal lens actorKey %q is not a Contract #1 vertex key", actorKey)
		}
		anchorRaw, _ := row["anchor"].(string)
		if anchorRaw == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}

		if capKV != nil {
			_, anchorNanoID, ok := substrate.ParseVertexKey(anchorRaw)
			if !ok {
				return nil, nil, fmt.Errorf("projection: personal lens anchor %q is not a Contract #1 vertex key", anchorRaw)
			}
			var readable bool
			if grants, scoped := params[personalScopeGrantsParam].(*capabilityread.AnchorSet); scoped {
				readable = grants.Admits(anchorNanoID)
			} else {
				// grant-change-posture: (subscribed) the cap-read producers carry
				// the grant-change edge (IsReadGrantProducer wires the sink in
				// InstallActorAggregate), so a grant landing or being withdrawn
				// re-drives this actor's personal pipelines through
				// Pipeline.ReprojectPersonalActor rather than waiting for an
				// unrelated Core KV event to happen to re-ask this gate.
				perRow, err := capabilityread.IsReadable(context.Background(), capKV, actorType, actorID, anchorNanoID)
				if err != nil {
					return nil, nil, fmt.Errorf("projection: personal lens read-grant check for %q: %w", actorID, err)
				}
				readable = perRow
			}
			if !readable {
				return nil, nil, pipeline.ErrSkipProjection
			}
		}

		if interestKV != nil {
			anchorType, _ := row["kind"].(string)
			var relevant bool
			if regs, scoped := params[personalScopeInterestParam].([]personalinterest.Registration); scoped {
				relevant = personalinterest.RelevantIn(regs, anchorType, anchorRaw)
			} else {
				// interest-change-posture: (subscribed) every writer of the
				// Interest Set announces on the change edge — the control plane's
				// register and deregister ops, and the health InterestReconciler's
				// orphan reap — so a device that narrows or widens what it wants
				// has this actor's personal pipelines re-driven through
				// Pipeline.ReprojectPersonalActor rather than waiting for the
				// convergence sweep to come round.
				perRow, err := personalinterest.IsRelevant(context.Background(), interestKV, actorID, anchorType, anchorRaw)
				if err != nil {
					return nil, nil, fmt.Errorf("projection: personal lens interest-set check for %q: %w", actorID, err)
				}
				relevant = perRow
			}
			if !relevant {
				return nil, nil, pipeline.ErrSkipProjection
			}
		}

		newKeys := make(map[string]any, len(keys)+1)
		for k, v := range keys {
			newKeys[k] = v
		}
		newKeys[adapter.PersonalActorKeyField] = actorID
		return row, newKeys, nil
	}
}
