package projection

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/personalinterest"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
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

// InstallPersonalLens wires the Fire 2 personal pipeline
// (personal-secure-lens-design.md §3.3): the existing ActorEnumerator
// (actorType "identity") drives per-recipient re-execution of the lens
// cypher, and a personal envelope injects the enumerated recipient into the
// adapter's reserved "__actor" key field — the cypher itself declares only
// its business key columns (Into.Key minus "__actor").
//
// interestKV is the personal-lens-interest bucket handle; nil disables the
// Interest Set relevance filter (every delta streams — the fail-open default
// the design specifies for "no registration yet"). Returns false when the
// lens must not be registered (a fail-closed descriptor/engine error).
func InstallPersonalLens(p *pipeline.Pipeline, r *lens.Rule, adjKV, coreKV, interestKV *substrate.KV, logger *slog.Logger) bool {
	cr, ok := r.CompiledRule.(*full.CompiledRule)
	if !ok {
		logger.Error("personal lens requires the full engine", "lensId", r.ID)
		return false
	}

	businessKeys := make([]string, 0, len(r.Into.Key))
	for _, k := range r.Into.Key {
		if k == adapter.PersonalActorKeyField {
			continue
		}
		businessKeys = append(businessKeys, k)
	}
	cr.KeyColumns = businessKeys
	if err := cr.ValidateKeyColumns(); err != nil {
		logger.Error("personal lens key-column validation", "lensId", r.ID, "err", err)
		return false
	}

	p.SetEnvelopeFn(personalEnvelopeFn(interestKV, logger))
	p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, PersonalActorType))

	logger.Info("personal lens fan-out + envelope installed",
		"lensId", r.ID, "businessKeys", businessKeys, "interestSetFilter", interestKV != nil)
	return true
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
func personalEnvelopeFn(interestKV *substrate.KV, logger *slog.Logger) pipeline.EnvelopeFn {
	return func(row map[string]any, keys map[string]any, params map[string]any) (map[string]any, map[string]any, error) {
		actorKey, _ := params["actorKey"].(string)
		if actorKey == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}
		_, actorID, ok := substrate.ParseVertexKey(actorKey)
		if !ok {
			return nil, nil, fmt.Errorf("projection: personal lens actorKey %q is not a Contract #1 vertex key", actorKey)
		}
		anchorID, _ := row["anchor"].(string)
		if anchorID == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}

		if interestKV != nil {
			anchorType, _ := row["kind"].(string)
			relevant, err := personalinterest.IsRelevant(context.Background(), interestKV, actorID, anchorType, anchorID)
			if err != nil {
				return nil, nil, fmt.Errorf("projection: personal lens interest-set check for %q: %w", actorID, err)
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
