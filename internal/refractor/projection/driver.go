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

// EntryEnvelopeFn builds the pipeline per-entry envelope wrapper for an
// actor-aggregate lens whose descriptor sets EntryKeyColumn (§3.3/§4.1 of
// cap-read-per-anchor-grant-keys-design.md): instead of one document per
// actor it emits one guarded key per REAL entry of the descriptor's single
// split list column. ParseOutputDescriptor already enforces exactly one
// BodyColumns entry when EntryKeyColumn is set, so d.BodyColumns[0] names
// that column.
//
// Behavior:
//   - The whole-row skip / anchor-type checks mirror EnvelopeFn exactly (an
//     empty or wrong-type actorKey declines the whole row via
//     pipeline.ErrSkipProjection).
//   - The list column is realness-filtered exactly as EnvelopeFn's roster
//     path (RealnessFiltered); entries the filter drops emit no key. Zero
//     surviving entries is not an error — it returns an empty entry set (no
//     write, no delete). Retracting any PREVIOUSLY-projected key this actor
//     no longer earns is §4.2's job (a later increment), not this one's.
//   - Each surviving entry must be a map (the shape every live roster column
//     — {anchorId, anchorType, via} and similar — produces) whose
//     EntryKeyColumn field is a non-empty, NanoID-alphabet string (a subject
//     metacharacter must never reach a key, Contract #1). Any other shape —
//     absent, empty, non-string, non-map entry, or a malformed token — errors
//     the WHOLE actor evaluation (fail-closed: never silently drops a grant,
//     never writes a malformed key), exactly the posture §3.3 specifies.
//   - The key is d.BuildKey(actorKey) + "." + the entry's key-field value.
//     The body is the entry's OTHER fields (the key field is not duplicated
//     into the body — §3.2) plus the envelope metadata (key, ActorField,
//     version, projectedAt). projectedFromRevisions is deliberately absent
//     (§3.2 — it was aggregate-level provenance; a per-key guard's
//     projectionSeq, stamped by the adapter write path, is the ordering
//     authority for a per-anchor key).
//   - Duplicate key-field values within one row's entries collapse to one
//     key, last entry wins (§3.3 — the cypher's own DISTINCT dedup already
//     collapses exact duplicates; a genuine collision across walk branches is
//     audit-only and benign).
//
// InstallActorAggregate calls this for any descriptor with entryKeyColumn
// set, alongside the full §4.2 retraction transport, §4.3's retry-path
// routing, and §4.4's sweep deltas — the bootstrap capabilityRead base lens
// (§6) is the first live one.
func (d OutputDescriptor) EntryEnvelopeFn() pipeline.MultiEnvelopeFn {
	if d.EntryKeyColumn == "" || len(d.BodyColumns) != 1 {
		// ParseOutputDescriptor already refuses this shape (§3.3), so a
		// caller reaching here went around it — a hand-built OutputDescriptor
		// literal, which the exported fields allow. Return a func that
		// errors on every call rather than let the len(d.BodyColumns) == 0
		// index below panic.
		return func(map[string]any, map[string]any, map[string]any) ([]pipeline.Envelope, error) {
			return nil, fmt.Errorf("projection: EntryEnvelopeFn requires entryKeyColumn set and exactly one bodyColumns entry (got entryKeyColumn=%q, bodyColumns=%v)", d.EntryKeyColumn, d.BodyColumns)
		}
	}
	listCol := d.BodyColumns[0]
	return func(row map[string]any, _ map[string]any, params map[string]any) ([]pipeline.Envelope, error) {
		actorKey, _ := row["actorKey"].(string)
		if actorKey == "" {
			actorKey, _ = params["actorKey"].(string)
		}
		if actorKey == "" {
			return nil, pipeline.ErrSkipProjection
		}
		vtxType, _, ok := substrate.ParseVertexKey(actorKey)
		if !ok {
			return nil, fmt.Errorf("projection: actorKey %q is not a Contract #1 vertex key", actorKey)
		}
		if vtxType != d.AnchorType {
			return nil, pipeline.ErrSkipProjection
		}

		list, _ := row[listCol].([]any)
		real := d.RealnessFiltered(list)

		byID := make(map[string]pipeline.Envelope, len(real))
		order := make([]string, 0, len(real))
		for _, e := range real {
			entry, isMap := e.(map[string]any)
			if !isMap {
				return nil, fmt.Errorf("projection: entryKeyColumn %q: entry is not a map (%T)", d.EntryKeyColumn, e)
			}
			idVal, isStr := entry[d.EntryKeyColumn].(string)
			if !isStr || idVal == "" {
				return nil, fmt.Errorf("projection: entryKeyColumn %q: entry carries no usable key field", d.EntryKeyColumn)
			}
			if !substrate.IsValidNanoID(idVal) {
				return nil, fmt.Errorf("projection: entryKeyColumn %q value %q is not a valid NanoID key token", d.EntryKeyColumn, idVal)
			}

			outKey := d.BuildKey(actorKey) + "." + idVal
			body := make(map[string]any, len(entry)+3)
			for k, v := range entry {
				if k == d.EntryKeyColumn {
					continue
				}
				if _, reserved := entryReservedFields[k]; reserved || k == d.ActorField {
					// The entry's own fields land at the body's top level
					// (unlike doc-mode, which nests body columns under their
					// own name — driver.go's EnvelopeFn — an entry field can
					// never collide with the envelope namespace there). A
					// roster whose collected map happens to carry a field
					// named "isDeleted" or "projectionSeq" (e.g. copied
					// straight off a Core KV vertex body, which carries
					// isDeleted) must never be let overwrite the guard's own
					// tombstone/watermark fields — that would silently
					// deny-by-tombstone or poison the §6.2 seq watermark on
					// write. Fail closed rather than let the last writer
					// (entry vs. metadata) win by accident.
					return nil, fmt.Errorf("projection: entryKeyColumn %q: entry carries reserved field %q, which would collide with the envelope metadata", d.EntryKeyColumn, k)
				}
				body[k] = v
			}
			body["key"] = outKey
			body[d.ActorField] = actorKey
			body["version"] = Version
			body["projectedAt"] = params["projectedAt"]

			if _, seen := byID[idVal]; !seen {
				order = append(order, idVal)
			}
			byID[idVal] = pipeline.Envelope{Keys: map[string]any{"key": outKey}, Row: body}
		}

		entries := make([]pipeline.Envelope, 0, len(order))
		for _, id := range order {
			entries = append(entries, byID[id])
		}
		return entries, nil
	}
}

// entryReservedFields are the per-entry body field names EntryEnvelopeFn
// itself writes (key, version, projectedAt) plus the two fields the guarded
// NATS-KV write path stamps or reads out-of-band (natskv.go's guardedBody
// injects projectionSeq; a soft-tombstoned key carries isDeleted — Contract
// #6 §6.8). d.ActorField ("actor" by default) is checked separately since
// it is configurable, not a literal. An entry field sharing any of these
// names is refused rather than silently overwritten or overwriting.
var entryReservedFields = map[string]struct{}{
	"key":           {},
	"version":       {},
	"projectedAt":   {},
	"projectionSeq": {},
	"isDeleted":     {},
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
	plan, err := Compile(r)
	if err != nil {
		logger.Error("actor-aggregate plan compile failed — refusing registration",
			"lensId", r.ID, "err", err)
		return false
	}
	authPlane := plan.AuthPlane
	p.SetAuthPlane(authPlane)
	p.SetRequiresFootprintValidation(plan.RequiresFootprintValidation)

	// A perEntry descriptor (entryKeyColumn set) projects through
	// EntryEnvelopeFn's per-anchor keys instead of EnvelopeFn's one document
	// per actor (cap-read-per-anchor-grant-keys-design.md §4.1); every other
	// installation step below — fan-out, delete-key derivation, sweep
	// enrolment, truncate scoping, guard — already reads from desc/plan alone
	// and needs no perEntry branch of its own (§4.2's retraction, §4.3's
	// retry-path routing, and §4.4's sweep deltas all dispatch on whichever
	// envelope func is installed, not on how it got installed).
	lensDefKey := "vtx.meta." + r.ID
	if desc.EntryKeyColumn != "" {
		// The §4.2 retraction transport (multiEntryRetractions) that every
		// perEntry evaluation runs through requires the adapter to both list
		// its own keys by prefix and read a candidate back to decide
		// tombstone-skip — refusing here when the target can't is the same
		// posture EnableProjectionGuard and SetDiffRetraction already take
		// for their own adapter requirements: a lens installed without the
		// capability its mechanism depends on doesn't run degraded, it fails
		// every evaluation, which is a worse failure mode than never
		// installing.
		if _, ok := adpt.(adapter.PrefixKeyLister); !ok {
			logger.Error("actor-aggregate entryKeyColumn descriptor requires an adapter that can list keys by prefix — refusing registration",
				"lensId", r.ID, "adapter", fmt.Sprintf("%T", adpt))
			return false
		}
		if _, ok := adpt.(adapter.RowReader); !ok {
			logger.Error("actor-aggregate entryKeyColumn descriptor requires an adapter that can read a row back — refusing registration",
				"lensId", r.ID, "adapter", fmt.Sprintf("%T", adpt))
			return false
		}
		p.SetMultiEnvelopeFn(desc.EntryEnvelopeFn())
	} else {
		p.SetEnvelopeFn(desc.EnvelopeFn(lensDefKey, projectionRevision))
		// Zero-row retraction (evaluate.go's executeFullForActorOnce): a
		// doc-mode descriptor whose empty behavior tombstones needs it
		// because a filtering WHERE on the anchor match itself makes the
		// cypher return no row at all once the anchor stops matching — the
		// per-row envelope callback above only ever runs on a produced row,
		// so it never sees that case. A perEntry lens (the other branch of
		// this if) retracts through its own prefix-diff instead.
		if desc.RequiresGuardedTombstone() {
			p.SetZeroRowRetraction(true)
		}
	}
	p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, desc.AnchorType))
	// An actor-aggregate lens's row is a function of the subgraph its compiled
	// pattern binds and nothing else — the soundness precondition for the
	// fan-out arms' relevance gate (auth-plane-projection-latency-design.md
	// §4.1/§4.2). InstallPersonalLens deliberately does not assert this: a
	// Personal Lens also consults the D1 read gate (cap-read.<domain>.<actor>)
	// and the Interest Set, so an event outside its pattern can still change
	// what it projects.
	p.SetPatternClosedOutput(true)
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
		"anchorType", desc.AnchorType, "guarded", guarded, "authPlane", authPlane,
		"perEntry", desc.EntryKeyColumn != "", "footprintValidated", plan.RequiresFootprintValidation,
		"zeroRowRetraction", p.ZeroRowRetractionArmed())
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
// the WHOLE bucket. That is the right default for a lens owning its target
// outright — a dedicated target's rebuild has to clear everything to reach the
// empty high-water state §6.2 wants — but it is NOT evidence that the lens owns
// it. A lens can share a bucket and still reach this function's early returns:
// rbac-domain's capabilityRoleIndex is neither an actor-aggregate nor carries
// an output key pattern, and writes capability-kv beside the core cap.<actor>
// surface; one-bill's four plain lenses share one history bucket. So an
// unscoped adapter means "unconfined", never "unshared", and a caller deciding
// whether to ASK for a truncate must read it as unconfined —
// Pipeline.RebuildTruncateIsScoped is that test, and it refuses.
//
// What keeps the truncates that already happen safe is that they are FORCED
// only for a guarded adapter, and the guard has the same actor-aggregate driver
// this scoping does (projection.RequiresGuard): every lens truncating on a
// rebuild today is a lens this function has scoped.
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
