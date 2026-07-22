// Package projection compiles an actor-aggregate lens definition into a
// ProjectionPlan{Execution, Output} and drives the live pipeline from it. The
// plan turns per-actor projection behavior into data (lens-definition aspects)
// rather than core Go keyed on a lens canonical name: the Output descriptor's
// EnvelopeFn, BuildKey, and guard predicate replace the per-CanonicalName
// wrappers, so a brand-new package lens projects with no core edit. Fan-out
// uses the broad adjacency BFS (the sound superset that can never miss an
// affected anchor).
package projection

import (
	"context"
	"fmt"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// ActorAggregateKind is the projectionKind aspect value that opts a lens into
// the actor-aggregate projection plan compiler (Contract #6 §6.13).
const ActorAggregateKind = "actorAggregate"

// AuthPlaneBucket is the target bucket that classifies a lens as auth-plane: a
// lens projecting into capability-kv writes an authorization surface (cap.*,
// including the decomposed cap.roles.* / cap.svc.*), so it is projection-write
// guarded (§6.2 monotonic-seq tombstone) and alerts at the auth-plane heartbeat
// severity. Fan-out is the unconditional broad adjacency BFS for every lens (a
// sound superset); see IsAuthPlane, which also covers the Postgres grant table.
const AuthPlaneBucket = "capability-kv"

// ExecutionPlan is the Execution half of a ProjectionPlan: the per-actor full-
// engine evaluation of the lens for a bound $actorKey. It references the
// existing executor path; the compiler does not change how a row is produced.
type ExecutionPlan struct {
	// Engine is the resolved rule engine name (always "full" for an actor-
	// aggregate lens — the simple engine cannot express the delegation pattern).
	Engine string
	// CompiledRule is the engine-specific compiled artifact the executor
	// consumes via ExecuteWith.
	CompiledRule ruleengine.CompiledRule
	// AnchorType is the actor vertex type the lens projects against.
	AnchorType string
}

// Execute evaluates the lens for one bound actor against the live KV, returning
// the projected RETURN rows. It is the same per-actor eval path the live
// pipeline uses; the projection plan only references it.
func (e *ExecutionPlan) Execute(ctx context.Context, params map[string]any, adjKV, coreKV *substrate.KV) ([]ruleengine.ProjectionResult, error) {
	eng := full.New()
	return eng.ExecuteWith(ctx, e.CompiledRule,
		ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
}

// ProjectionPlan is the compiled, data-driven representation of an actor-
// aggregate lens: how to evaluate it for one actor (Execution) and how to
// shape and key the output document (Output).
type ProjectionPlan struct {
	CanonicalName string
	Execution     ExecutionPlan
	Output        OutputDescriptor
	// AuthPlane reports whether the lens projects into the capability-kv bucket
	// (an authorization surface).
	AuthPlane bool
}

// IsActorAggregate reports whether a lens rule opts into the actor-aggregate
// projection plan via projectionKind. Routing keys only off this aspect, never
// off the canonical name.
func IsActorAggregate(r *lens.Rule) bool {
	return r != nil && r.ProjectionKind == ActorAggregateKind
}

// RequiresGuard reports whether this plan's writes must run under the §6.2
// monotonic projection-write guard. It is true when the lens projects an
// authorization surface (AuthPlane, target bucket capability-kv) OR its empty
// behavior produces a §6.2 soft tombstone (emptyBehavior ∈ {delete, softDelete}).
// This is the sole gate on enabling the guard — derived from the compiled plan,
// never from a canonical-name list.
func (p *ProjectionPlan) RequiresGuard() bool {
	return p.AuthPlane || p.Output.RequiresGuardedTombstone()
}

// IsAuthPlane classifies a lens as auth-plane iff it either writes the
// capability-kv bucket (the write-authorization surface) or is a grant-table
// lens projecting to actor_read_grants (Contract #6 §6.14) — the read-auth
// source of truth every protected table's RLS policy consults. A paused
// grant-table lens freezes read authorization the same way a paused
// capability-kv lens freezes write authorization, so both alert at the
// heartbeater's auth-plane (error) severity rather than the generic business-
// lens (warning) tier. An ordinary protected business lens is NOT auth-plane
// by this test — RLS enforcement is Postgres-native and independent of that
// lens's own freshness; only the grant table it reads from is auth-critical.
// Derived from the bucket/target, never from a canonical-name list and never
// from an extra aspect.
func IsAuthPlane(r *lens.Rule) bool {
	if r.Into.Target == "nats_kv" && r.Into.Bucket == AuthPlaneBucket {
		return true
	}
	return r.Into.Target == "postgres" && r.Into.GrantTable
}

// Compile turns an actor-aggregate lens rule into a ProjectionPlan: it
// validates and reads the Output descriptor (§6.13), classifies auth-plane,
// and builds the per-actor execution path. Compile must only be called for a
// lens where IsActorAggregate(r) is true.
func Compile(r *lens.Rule) (*ProjectionPlan, error) {
	if !IsActorAggregate(r) {
		return nil, fmt.Errorf("projection: lens %q is not an actorAggregate (projectionKind=%q)", r.CanonicalName, r.ProjectionKind)
	}

	desc, err := ParseOutputDescriptor(r.Output)
	if err != nil {
		return nil, fmt.Errorf("projection: lens %q: %w", r.CanonicalName, err)
	}

	return &ProjectionPlan{
		CanonicalName: r.CanonicalName,
		Output:        desc,
		AuthPlane:     IsAuthPlane(r),
		Execution: ExecutionPlan{
			Engine:       r.ResolvedEngine,
			CompiledRule: r.CompiledRule,
			AnchorType:   desc.AnchorType,
		},
	}, nil
}
