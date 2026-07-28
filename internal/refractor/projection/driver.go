package projection

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// EnvelopeFn builds the pipeline envelope wrapper that turns each per-actor
// RETURN row into the on-wire document the descriptor describes. It is the
// single data-driven replacement for the per-canonical-name capability
// envelope wrappers: one path, parameterized by the compiled OutputDescriptor.
//
// lensDefKey is the meta-lens vertex key (vtx.meta.<id>); revisionOf returns the
// current Core KV revision of a key (0 = unknown/absent). Both feed
// projectedFromRevisions via ContributingSources (§6.3, freshness: auto).
//
// Behavior, reproducing the built-in wrappers exactly:
//   - A row whose anchor actorKey is empty is declined (ErrSkipProjection) — it
//     is the degenerate aggregation row a cypher produces over zero anchor
//     bindings. The my-tasks wrapper additionally falls back to the bound
//     params["actorKey"] before declining, so a last-task-closed actor deletes
//     its key rather than leaving it stale; this driver does the same.
//   - A row whose anchor is not the descriptor's AnchorType is declined.
//   - Each body column projects by the SHAPE of its RETURN value: a list
//     (collect) value is realness-filtered (the roster path — drop degenerate
//     null-key collect entries); a scalar value (bool, string, number, or nil)
//     projects VERBATIM (the convergence path — a scalar Weaver reads as a bool
//     or a string param). A nil scalar projects as a genuine null so a downstream
//     bool reads false and a string param reads absent, never as `[]`.
//   - When the empty behavior is delete/softDelete and the realness check finds
//     no real value, the row is declined with ErrDeleteProjection keyed at
//     BuildKey(actorKey). Realness for a list column is "any real entry after the
//     filter"; for a designated scalar realness column it is "the scalar is
//     present and real" (a convergence lens marks the anchor alive that way).
//   - Otherwise the envelope is {key, <actorField>: actorKey, version,
//     projectedAt, projectedFromRevisions, [lanes], <bodyColumns...>,
//     <staticEmptyColumns...: []>}.
func (d OutputDescriptor) EnvelopeFn(lensDefKey string, revisionOf func(string) uint64) pipeline.EnvelopeFn {
	return func(row map[string]any, keys map[string]any, params map[string]any) (map[string]any, map[string]any, error) {
		actorKey, _ := row["actorKey"].(string)
		if actorKey == "" {
			actorKey, _ = params["actorKey"].(string)
		}
		if actorKey == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}
		vtxType, _, ok := substrate.ParseVertexKey(actorKey)
		if !ok {
			return nil, nil, fmt.Errorf("projection: actorKey %q is not a Contract #1 vertex key", actorKey)
		}
		if vtxType != d.AnchorType {
			return nil, nil, pipeline.ErrSkipProjection
		}

		outKey := d.BuildKey(actorKey)

		// Project each body column by the SHAPE of its RETURN value and decide
		// the empty-result action. A list column is realness-filtered (the roster
		// path); a scalar column projects verbatim (the convergence path) — a
		// scalar RETURN value (bool, string, number, nil) is never coerced to []
		// so Weaver's boolColumn / string-param resolution reads it directly.
		projected := make(map[string]any, len(d.BodyColumns))
		anyReal := false
		for _, col := range d.BodyColumns {
			if list, isList := row[col].([]any); isList {
				vals := d.RealnessFiltered(list)
				if vals == nil {
					vals = []any{}
				}
				projected[col] = vals
				if len(vals) > 0 {
					anyReal = true
				}
				continue
			}
			// Scalar passthrough: the raw value as-is (a nil scalar stays nil, so
			// the envelope carries a genuine null, not an empty list).
			projected[col] = row[col]
		}

		// A designated scalar realness column (e.g. a convergence lens's
		// entityKey) marks the anchor alive when present and real. This is
		// distinct from the roster realness (a field inside each list entry); a
		// roster lens names a field that lives inside its collect entries, never a
		// top-level scalar column, so this check is dormant for the roster lenses.
		if d.RealnessFilter != "" {
			if v, isCol := row[d.RealnessFilter]; isCol {
				if _, isList := v.([]any); !isList && isRealField(v) {
					anyReal = true
				}
			}
		}

		if !anyReal && d.RealnessFilter != "" {
			switch d.EmptyAction() {
			case ActionDelete, ActionSoftDelete:
				return nil, map[string]any{"key": outKey}, pipeline.ErrDeleteProjection
			case ActionSkip:
				return nil, nil, pipeline.ErrSkipProjection
			case ActionWriteEmptyDoc:
				// Fall through to build the envelope with every body column
				// already empty-after-realness — the key stays present with an
				// empty body, which is exactly the empty-doc behavior.
			}
		}

		envelope := map[string]any{
			"key":                    outKey,
			d.ActorField:             actorKey,
			"version":                Version,
			"projectedAt":            params["projectedAt"],
			"projectedFromRevisions": ContributingSources(actorKey, lensDefKey, []map[string]any{row}, revisionOf),
		}
		if len(d.Lanes) > 0 {
			envelope["lanes"] = append([]string(nil), d.Lanes...)
		}
		for _, col := range d.BodyColumns {
			envelope[col] = projected[col]
		}
		for _, col := range d.StaticEmptyColumns {
			envelope[col] = []any{}
		}

		return envelope, map[string]any{"key": outKey}, nil
	}
}

// Version is the Capability KV envelope schema version (Contract #6 §6.3),
// pinned to "1.0" for Phase 1. Every actor-aggregate document carries it.
const Version = "1.0"

// BusinessSweepInterval is how often a non-auth-plane actor-aggregate lens
// sweeps, against the auth plane's DefaultSweepInterval.
//
// The two clocks carry the same asymmetry the health path already states: a
// stale business read model is one vertical's outage, an unhealed capability
// document is an authorization failure. Sweeping a dozen business lenses on the
// auth plane's clock would also multiply the cell's steady-state reprojection
// load by the lens count, for rows whose staleness costs a view rather than a
// decision.
const BusinessSweepInterval = 5 * time.Minute

// sweepInterval selects the clock for a lens's plan; zero leaves the sweeper's
// own default, which is the auth plane's.
func sweepInterval(authPlane bool) time.Duration {
	if authPlane {
		return 0
	}
	return BusinessSweepInterval
}

// sweepEnrolment decides whether a lens may be swept, returning the prefix its
// target listing is scoped by, or a non-empty reason it may not be.
//
// A lens is enrolled only when it can prove it owns its rows, on all three
// counts a shared target demands. The gate is structural rather than a
// name list because the alternative is not a lens that sweeps badly — it is one
// whose sweep faults on every tick, which reports as a lens that cannot be
// repaired rather than one that is not being swept.
func sweepEnrolment(desc OutputDescriptor, adpt adapter.Adapter) (prefix, refusal string) {
	prefix, ok := desc.KeyPrefix()
	if !ok {
		return "", "its output key pattern yields no prefix to scope a target listing by"
	}
	if !desc.KeyOwnershipRoundTrips() {
		// Without a working inverse the orphan direction claims nothing, which
		// is indistinguishable from having nothing to claim — the detector
		// would be off and the lens would report healthy.
		return "", "its output key pattern does not round-trip through AnchorFromKey, so the orphan direction could never claim a row"
	}
	if _, ok := adpt.(adapter.PrefixKeyLister); !ok {
		return "", fmt.Sprintf("its target adapter %T cannot enumerate the keys under that prefix", adpt)
	}
	return prefix, ""
}

// InstallActorAggregate wires an actor-aggregate lens through the compiled
// ProjectionPlan: the §6.13 Output descriptor drives the on-wire envelope, the
// per-actor cross-vertex fan-out, the empty/delete-key behavior, and the §6.2
// guard predicate — all from lens-definition data, with no canonical-name
// knowledge. Returns false when the lens must NOT be registered (a fail-closed
// descriptor error), true once the components are installed.
//
// Fan-out uses the broad adjacency ActorEnumerator — the sound superset that
// can never miss an affected anchor, so it over-reprojects rather than under-
// reprojecting a security-plane lens.
func InstallActorAggregate(
	p *pipeline.Pipeline,
	adpt adapter.Adapter,
	r *lens.Rule,
	projectionRevision func(string) uint64,
	adjKV, coreKV *substrate.KV,
	logger *slog.Logger,
) bool {
	desc, err := ParseOutputDescriptor(r.Output)
	if err != nil {
		logger.Error("actor-aggregate output descriptor invalid — refusing registration",
			"lensId", r.ID, "err", err)
		return false
	}
	if desc.EntryKeyColumn != "" {
		// EnvelopeFn below has no perEntry emission path yet (§4.1 of
		// cap-read-per-anchor-grant-keys-design.md ships it in a later
		// increment) — writing the one-document-per-actor shape for a lens
		// that opted into per-entry keys would silently keep the unbounded
		// document this design exists to retire. Refuse loudly (fail-closed,
		// under-grant) rather than degrade the write silently.
		logger.Error("actor-aggregate output descriptor sets entryKeyColumn but the driver has no perEntry emission yet — refusing registration",
			"lensId", r.ID)
		return false
	}

	plan, err := Compile(r)
	if err != nil {
		logger.Error("actor-aggregate plan compile failed — refusing registration",
			"lensId", r.ID, "err", err)
		return false
	}
	authPlane := plan.AuthPlane

	lensDefKey := "vtx.meta." + r.ID
	p.SetEnvelopeFn(desc.EnvelopeFn(lensDefKey, projectionRevision))
	p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, desc.AnchorType))
	p.SetActorDeleteKey(desc.BuildKey)
	p.SetLatencyBuffer(pipeline.NewLatencyRingBuffer(pipeline.DefaultLatencyBufferSize))

	// The convergence sweep (capability-projection-reconciliation-design.md
	// §3.2, generalized by lens-projection-liveness-design.md §15). Installing
	// a plan is what opts a lens in, and every actor-aggregate lens that can
	// name its own rows receives one — a plain lens retracts through
	// filter/diff retraction and the Personal Lens has its own Hydrate, so
	// neither is excluded by a name list; it simply never gets a plan.
	//
	// The sweep is the only healer for a row that is present but stale, which
	// is exactly what adding a walk to a lens leaves behind, so withholding it
	// from business lenses left them converging only when a CDC event next
	// happened to touch the actor.
	if prefix, refusal := sweepEnrolment(desc, adpt); refusal != "" {
		// Refusing is the same posture the grant adapter takes for a lens
		// declaring no grant_source: no scoping, no enumeration. A sweep the
		// lens cannot support is not a degraded sweep — it is one that faults
		// every tick, reporting a lens that is unrepairable rather than one
		// that is simply not swept.
		logger.Warn("actor-aggregate lens gets no convergence sweep",
			"lensId", r.ID, "reason", refusal, "outputKeyPattern", desc.OutputKeyPattern)
	} else {
		p.SetSweepPlan(pipeline.SweepPlan{
			AnchorType:    desc.AnchorType,
			BuildKey:      desc.BuildKey,
			AnchorFromKey: desc.AnchorFromKey,
			KeyPrefix:     prefix,
			Interval:      sweepInterval(authPlane),
		})
	}

	// Scope this lens's rebuild truncate to its own rows. Independent of the
	// guard: a lens sharing a bucket must not purge its siblings whether or not
	// its writes are ordered.
	ApplyTruncateScope(adpt, r)

	guarded := plan.RequiresGuard()
	if guarded {
		if gErr := EnableProjectionGuard(adpt, r.ID); gErr != nil {
			logger.Error("actor-aggregate guard", "lensId", r.ID, "err", gErr)
			return false
		}
		// The requirement outlives this adapter instance. A lens whose writes
		// need the §6.2 ordering token needs it for the lens's whole life, so
		// the pipeline — the object that survives an adapter swap — is what
		// carries it, and refuses any later adapter that cannot enforce it.
		p.RequireGuardedAdapter()
	}

	logger.Info("actor-aggregate envelope + fan-out + delete-key + latency installed",
		"lensId", r.ID, "lensDefKey", lensDefKey,
		"anchorType", desc.AnchorType, "guarded", guarded, "authPlane", authPlane)
	return true
}

// ApplyGuard binds a lens rule's §6.2 guard requirement to an adapter built for
// that rule. The requirement is a property of the RULE, not of one adapter
// instance, so EVERY adapter a lens ever writes through must carry it — the
// activation-path adapter and equally a replacement built to swap into a
// running pipeline on an INTO-only hot reload. That second caller is why this
// is separable from InstallActorAggregate, which runs only at activation.
//
// A rule needing no guard leaves the adapter untouched. A rule needing one on a
// target that cannot enforce it is an error, never a silent downgrade — the
// same fail-closed posture EnableProjectionGuard takes, so a caller that
// refuses to build or install the adapter keeps the guarded lens off rather
// than running it open.
func ApplyGuard(adpt adapter.Adapter, r *lens.Rule) error {
	required, err := RequiresGuard(r)
	if err != nil {
		return err
	}
	if !required {
		return nil
	}
	return EnableProjectionGuard(adpt, r.ID)
}

// ApplyTruncateScope binds a lens rule's key prefix to an adapter built for that
// rule, so a rebuild truncates only the rows this lens wrote.
//
// It is separable from InstallActorAggregate for the same reason ApplyGuard is:
// which keys a lens owns is a property of the RULE, so a replacement adapter
// swapped into a running pipeline on an INTO-only hot reload must carry it too.
// An adapter that lost the scoping on a reload would purge a shared bucket whole
// on its next rebuild — the wipe the scoping exists to prevent, reached through
// the swap rather than through activation.
//
// A rule with no derivable prefix leaves the adapter untouched, which truncates
// the whole bucket. That is not a downgrade: a lens with no output key pattern
// is not a shared-target lens, and a dedicated target's rebuild has to clear
// everything to reach the empty high-water state §6.2 wants.
func ApplyTruncateScope(adpt adapter.Adapter, r *lens.Rule) {
	nkv, ok := adpt.(*adapter.NatsKVAdapter)
	if !ok {
		return
	}
	if !IsActorAggregate(r) {
		return
	}
	desc, err := ParseOutputDescriptor(r.Output)
	if err != nil {
		return
	}
	if prefix, ok := desc.KeyPrefix(); ok {
		nkv.SetKeyPrefix(prefix)
	}
}

// RequiresGuard reports whether a lens rule's writes must run under the §6.2
// monotonic projection-write guard — true for an actor-aggregate lens that
// projects an authorization surface or whose empty behavior produces a soft
// tombstone. A lens that is not an actor-aggregate has no projection plan and
// no guard.
//
// It answers from the rule alone, so a caller holding nothing but a lens
// definition — the adapter builder, the hot-reload guard — decides the same way
// InstallActorAggregate does.
func RequiresGuard(r *lens.Rule) (bool, error) {
	if !IsActorAggregate(r) {
		return false, nil
	}
	plan, err := Compile(r)
	if err != nil {
		return false, fmt.Errorf("projection guard: lens %s: %w", r.ID, err)
	}
	return plan.RequiresGuard(), nil
}

// EnableProjectionGuard turns on the monotonic projection-write guard for a
// NATS-KV-backed lens. The caller decides which lenses are guarded from the
// compiled plan predicate (auth-plane or empty-delete tombstone) and flips the
// flag here. The guarded lenses are security/correctness-plane, so an adapter
// that cannot enforce the guard (e.g. a Postgres target) is a fail-closed error,
// not a silent downgrade: a guarded lens running unguarded re-opens the
// resurrection window the guard exists to close.
func EnableProjectionGuard(adpt adapter.Adapter, lensID string) error {
	nkv, ok := adpt.(*adapter.NatsKVAdapter)
	if !ok {
		return fmt.Errorf("projection-write guard required for lens %s but target adapter cannot enforce it (not NATS-KV)", lensID)
	}
	nkv.SetGuarded(true)
	return nil
}
