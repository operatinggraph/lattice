// Package capabilityenv builds the on-wire Contract #6 §6.2 envelope for
// Capability Lens projections. Story 3.2a Phase C (Decision #3): the
// envelope shape is target-specific; we wrap at the pipeline layer so
// the generic adapter.Adapter interface stays unchanged.
package capabilityenv

import (
	"fmt"
	"strings"

	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/substrate"
)

// IdentityType is the Contract #1 vertex-type segment the Capability
// Lens projects against. Events on non-identity vertices either don't
// match the cypher's anchor or arrive via cross-vertex fan-out which
// Story 3.2a defers to 3.2b (Decision #4). The envelope wrapper drops
// rows whose anchor isn't an identity, returning ErrSkipProjection.
const IdentityType = "identity"

// Version is the Capability KV envelope schema version per Contract #6
// §6.3 — pinned to "1.0" for Phase 1.
const Version = "1.0"

// DefaultLanes is the Phase 1 default value for the envelope's `lanes`
// field. Multi-lane projection is out of scope for 3.2a — see the
// closing summary for the multi-lane carry.
var DefaultLanes = []string{"default"}

// NewWrapper returns a pipeline.EnvelopeFn that wraps the executor's
// RETURN-row output into the Contract #6 §6.2 Capability KV envelope.
//
// lensDefKey is the meta-lens vertex key (e.g.
// `vtx.meta.<lensNanoID>`); it appears in `projectedFromRevisions` so
// downstream readers can correlate the projection to the lens spec
// revision that produced it.
//
// projectionRevision should return the current Core KV revision of the
// anchor vertex (the actor) — Story 3.2b will extend coverage to all
// vertices referenced by the rule's traversal; for 3.2a we record only
// the anchor + lens-def revisions per Decision #7.
//
// Duplicate-identity review is handled out-of-band: the
// identity-hygiene package's `duplicateCandidates` Lens projects
// flagged pairs into its own KV bucket. The cap envelope carries no
// review-state field.
func NewWrapper(lensDefKey string, projectionRevision func(actorKey string) uint64) pipeline.EnvelopeFn {
	return func(row map[string]any, keys map[string]any, params map[string]any) (map[string]any, map[string]any, error) {
		// The cypher's RETURN produces a non-null `actorKey` only when
		// the anchor `MATCH (identity:identity {key: $actorKey})`
		// actually bound. If the row carries a null actorKey we are
		// looking at an aggregation row produced over zero MATCH
		// bindings (the event was on a non-identity vertex). Drop.
		rowActor, _ := row["actorKey"].(string)
		if rowActor == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}
		actorKey := rowActor
		// Sanity check the actor vertex type — 3.2a only projects
		// identity actors; future fan-out (3.2b) will broaden this.
		vtxType, _, ok := substrate.ParseVertexKey(actorKey)
		if !ok {
			return nil, nil, fmt.Errorf("capabilityenv: actorKey %q is not a Contract #1 vertex key", actorKey)
		}
		if vtxType != IdentityType {
			return nil, nil, pipeline.ErrSkipProjection
		}

		envelope := map[string]any{
			"key":                    capabilityKey(actorKey),
			"actor":                  actorKey,
			"version":                Version,
			"projectedAt":            params["projectedAt"],
			"projectedFromRevisions": projectedFromRevisions(actorKey, lensDefKey, projectionRevision),
			"lanes":                  DefaultLanes,
			"platformPermissions":    emptyArrayIfNil(row["platformPermissions"]),
			"serviceAccess":          emptyArrayIfNil(row["serviceAccess"]),
			"ephemeralGrants":        emptyArrayIfNil(row["ephemeralGrants"]),
			"roles":                  emptyArrayIfNil(row["roles"]),
		}

		newKeys := map[string]any{"key": envelope["key"]}
		return envelope, newKeys, nil
	}
}

// capabilityKey converts an actor vertex key (vtx.identity.<NanoID>)
// into the Capability KV target key (cap.identity.<NanoID>) per
// Contract #6 §6.13.
func capabilityKey(actorKey string) string {
	if rest, ok := strings.CutPrefix(actorKey, substrate.VertexPrefix+"."); ok {
		return "cap." + rest
	}
	// Defensive — caller already validated; keep behaviour explicit.
	return "cap." + actorKey
}

func projectedFromRevisions(actorKey, lensDefKey string, fn func(string) uint64) map[string]any {
	out := map[string]any{}
	if fn != nil {
		if rev := fn(actorKey); rev != 0 {
			out[actorKey] = rev
		}
		if rev := fn(lensDefKey); rev != 0 {
			out[lensDefKey] = rev
		}
	}
	return out
}

func emptyArrayIfNil(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}

// NewEphemeralWrapper returns the EnvelopeFn for the orchestration-base
// `capabilityEphemeral` lens (Contract #6 §6.6 Phase-2 amendment / Contract
// #10 §10.7).
//
// It wraps the lens's RETURN row into the disjoint-key ephemeral-grant
// document and targets `cap.ephemeral.<actor-suffix>` — a DIFFERENT key
// space from the primary `cap.<actor>` doc (capabilityKey), in the SAME
// shared capability-kv bucket (the disjoint-prefix contribution pattern,
// Contract #6 §6.1).
//
// Input row (produced by the lens cypher RETURN):
//
//	{actorKey: "vtx.identity.<id>", ephemeralGrants: [{source,taskKey,operationType,target,expiresAt}, ...]}
//
// Output (Contract #6 §6.6 amendment shape):
//
//	{key: "cap.ephemeral.identity.<id>", actor: "vtx.identity.<id>",
//	 version: "1.0", projectedAt: "...", ephemeralGrants: [...]}
//
// Rows whose anchor isn't a bound identity are dropped (ErrSkipProjection),
// identical to the primary wrapper.
//
// The lens cypher anchors on a non-optional `MATCH (identity {key})`, so a
// live actor always yields exactly one row whose `ephemeralGrants` collect
// may contain degenerate `{taskKey:null}` artifacts when the actor has no
// (live) task. The wrapper filters those out: a grant counts only when its
// `taskKey` is a non-empty string. When zero real grants remain it returns
// ErrDeleteProjection keyed at the actor's ephemeral key — the pipeline
// emits a Delete and the default-hard adapter removes the key, so step-3
// reads absent → AuthContextMismatch (absence = denial, Contract #6 §6.8).
func NewEphemeralWrapper(lensDefKey string, projectionRevision func(actorKey string) uint64) pipeline.EnvelopeFn {
	return func(row map[string]any, keys map[string]any, params map[string]any) (map[string]any, map[string]any, error) {
		rowActor, _ := row["actorKey"].(string)
		if rowActor == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}
		actorKey := rowActor
		vtxType, _, ok := substrate.ParseVertexKey(actorKey)
		if !ok {
			return nil, nil, fmt.Errorf("capabilityenv: actorKey %q is not a Contract #1 vertex key", actorKey)
		}
		if vtxType != IdentityType {
			return nil, nil, pipeline.ErrSkipProjection
		}

		envKey := EphemeralKey(actorKey)
		grants := realEphemeralGrants(row["ephemeralGrants"])
		if len(grants) == 0 {
			// No live grants for this actor → delete the ephemeral key.
			return nil, map[string]any{"key": envKey}, pipeline.ErrDeleteProjection
		}
		envelope := map[string]any{
			"key":                    envKey,
			"actor":                  actorKey,
			"version":                Version,
			"projectedAt":            params["projectedAt"],
			"projectedFromRevisions": projectedFromRevisions(actorKey, lensDefKey, projectionRevision),
			"ephemeralGrants":        grants,
		}
		return envelope, map[string]any{"key": envKey}, nil
	}
}

// realEphemeralGrants returns the subset of a cypher `ephemeralGrants`
// collect whose entries carry a non-empty string `taskKey`. The lens's
// non-optional actor anchor plus OPTIONAL task matches mean a grant-less
// actor still produces a degenerate `{taskKey:null,...}` collect artifact;
// those are dropped so absence is real.
func realEphemeralGrants(v any) []any {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(list))
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		tk, ok := m["taskKey"].(string)
		if !ok || tk == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// EphemeralKey converts an actor vertex key (vtx.identity.<NanoID>) into the
// disjoint Capability KV ephemeral key (cap.ephemeral.identity.<NanoID>)
// per Contract #6 §6.6 amendment. It is the single source of truth for the
// ephemeral key shape: the envelope projects to it, the pipeline deletes it on
// actor disappearance, and the consumer (processor.ephemeralKeyFromActor)
// reads it.
func EphemeralKey(actorKey string) string {
	if rest, ok := strings.CutPrefix(actorKey, substrate.VertexPrefix+"."); ok {
		return "cap.ephemeral." + rest
	}
	return "cap.ephemeral." + actorKey
}

// NewMyTasksWrapper returns the EnvelopeFn for the orchestration-base
// `myTasks` lens (Contract #10 §10.1). It projects, per identity, that
// identity's OPEN tasks into the package-owned my-tasks bucket keyed
// my-tasks.identity.<id>.
//
// Input row (produced by the lens cypher RETURN):
//
//	{actorKey: "vtx.identity.<id>", openTasks: [{taskKey,assignee,forOperation,scopedTo,expiresAt}, ...]}
//
// Output:
//
//	{key: "my-tasks.identity.<id>", assignee: "vtx.identity.<id>",
//	 projectedAt: "...", openTasks: [...]}
//
// Like the ephemeral wrapper, the lens cypher anchors on a non-optional
// identity, so a live identity always yields one row whose `openTasks` collect
// may carry a degenerate {taskKey:null} artifact when the identity has no open
// task. The wrapper keeps only entries with a non-empty taskKey; when zero
// remain it returns ErrDeleteProjection keyed at the identity's my-tasks key —
// the pipeline emits a Delete and the default-hard adapter removes the key, so
// a closed/cancelled/reassigned-away task drops out of my-tasks (the 7.1 FIX-1
// genuine-absence mechanism). A reassign reprojects BOTH endpoints via the
// actor fan-out: the old assignee's doc loses the task, the new assignee's
// gains it.
func NewMyTasksWrapper(lensDefKey string, projectionRevision func(actorKey string) uint64) pipeline.EnvelopeFn {
	return func(row map[string]any, keys map[string]any, params map[string]any) (map[string]any, map[string]any, error) {
		// When the anchored identity has zero open tasks the cypher collapses the
		// OPTIONAL task chain and `identity.key` projects as null; the per-actor
		// `params["actorKey"]` the pipeline bound is the authoritative anchor, so
		// fall back to it to key the deletion. Without this an identity whose last
		// open task just closed would skip (key lingers) instead of deleting.
		actorKey, _ := row["actorKey"].(string)
		if actorKey == "" {
			actorKey, _ = params["actorKey"].(string)
		}
		if actorKey == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}
		vtxType, _, ok := substrate.ParseVertexKey(actorKey)
		if !ok {
			return nil, nil, fmt.Errorf("capabilityenv: actorKey %q is not a Contract #1 vertex key", actorKey)
		}
		if vtxType != IdentityType {
			return nil, nil, pipeline.ErrSkipProjection
		}

		envKey := MyTasksKey(actorKey)
		tasks := realOpenTasks(row["openTasks"])
		if len(tasks) == 0 {
			return nil, map[string]any{"key": envKey}, pipeline.ErrDeleteProjection
		}
		envelope := map[string]any{
			"key":                    envKey,
			"assignee":               actorKey,
			"version":                Version,
			"projectedAt":            params["projectedAt"],
			"projectedFromRevisions": projectedFromRevisions(actorKey, lensDefKey, projectionRevision),
			"openTasks":              tasks,
		}
		return envelope, map[string]any{"key": envKey}, nil
	}
}

// realOpenTasks returns the subset of a cypher `openTasks` collect whose
// entries carry a non-empty string `taskKey`. The non-optional identity anchor
// plus OPTIONAL task matches mean a task-less identity still produces a
// degenerate {taskKey:null} artifact; those are dropped so absence is real.
func realOpenTasks(v any) []any {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(list))
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		tk, ok := m["taskKey"].(string)
		if !ok || tk == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// MyTasksKey converts an identity vertex key (vtx.identity.<NanoID>) into the
// my-tasks bucket key (my-tasks.identity.<NanoID>). Single source of truth for
// the shape: the envelope projects to it and the pipeline deletes it on actor
// disappearance.
func MyTasksKey(actorKey string) string {
	if rest, ok := strings.CutPrefix(actorKey, substrate.VertexPrefix+"."); ok {
		return "my-tasks." + rest
	}
	return "my-tasks." + actorKey
}

// NewRoleIndexWrapper returns the EnvelopeFn for the secondary
// capabilityRoleIndex lens (Contract #6 §6.1 / Story 3.2b §2).
//
// Input row (produced by the cypher RETURN):
//
//	{operationType: "read", roles: [...], projectedAt: "..."}
//
// Output (Contract #6 §6.1 secondary-key shape):
//
//	{key: "cap.role-by-operation.<operationType>",
//	 projectedAt: <projectedAt>,
//	 roles: [...]}
//
// Rows whose operationType is null/empty are dropped (ErrSkipProjection) —
// the executor's `collect` over zero MATCH bindings produces such rows
// when the CDC event doesn't touch a role/permission grant.
func NewRoleIndexWrapper() pipeline.EnvelopeFn {
	return func(row map[string]any, keys map[string]any, params map[string]any) (map[string]any, map[string]any, error) {
		op, _ := row["operationType"].(string)
		if op == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}
		projectedAt, _ := row["projectedAt"].(string)
		if projectedAt == "" {
			projectedAt, _ = params["projectedAt"].(string)
		}
		envKey := "cap.role-by-operation." + op
		envelope := map[string]any{
			"key":         envKey,
			"projectedAt": projectedAt,
			"roles":       emptyArrayIfNil(row["roles"]),
		}
		// The natskv adapter constructs the bucket key from the seeded
		// Into.Key list, which for capabilityRoleIndex is ["operationType"].
		// Set that field to the full Contract #6 §6.1 key so the bucket
		// entry lands at `cap.role-by-operation.<op>` (mirrors the
		// primary lens's `keys["key"] = "cap.identity.<id>"` convention).
		return envelope, map[string]any{"operationType": envKey}, nil
	}
}

// NewNullKeySkipper returns an EnvelopeFn that passes rows through
// verbatim but returns ErrSkipProjection when the configured key field
// resolves to nil/empty. Used for the secondary capabilityRoleIndex
// lens in Story 3.2a: that cypher's RETURN aggregates over zero
// MATCH-bindings producing a NULL operationType row when the CDC event
// is for a vertex that has no role-permission link — the pipeline must
// not write a row keyed by NULL. Story 3.2b lands the full
// contract-conformance test for capabilityRoleIndex.
func NewNullKeySkipper(keyField string) pipeline.EnvelopeFn {
	return func(row map[string]any, keys map[string]any, params map[string]any) (map[string]any, map[string]any, error) {
		val, ok := keys[keyField]
		if !ok || val == nil {
			return nil, nil, pipeline.ErrSkipProjection
		}
		if s, isStr := val.(string); isStr && s == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}
		return row, keys, nil
	}
}
