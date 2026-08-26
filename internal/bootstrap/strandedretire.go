package bootstrap

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// RevocationOp is one RevokeRole / RevokePermission call
// PlanStrandedEpochRetirement derives from a StrandedOperatorEpoch finding.
// OperationType and Payload are shaped to drop straight into a
// processor.OperationEnvelope (json.Marshal(Payload) as its Payload field) —
// this package stays free of any dependency on internal/processor or a NATS
// connection; submission is the caller's job (cmd/lattice/bootstrap).
type RevocationOp struct {
	// OperationType is "RevokeRole" or "RevokePermission" — the two
	// packages/rbac-domain ops that already tombstone a holdsRole /
	// grantedBy link by known key (ddls.go:387-400, :433-446). No new
	// Starlark verb is needed; this plan only decides WHICH known keys to
	// call them on.
	OperationType string
	// Payload is the op's payload object: {"actorKey", "roleKey"} for
	// RevokeRole, {"permKey", "roleKey"} for RevokePermission.
	Payload map[string]any
	// LinkKey is the exact lnk.* key the op's own script will look up in
	// its read set — the caller must declare it in the submitted
	// OperationEnvelope's ContextHint.Reads or the op is rejected outright
	// (Contract #2 §2.5's declared-read posture). Computed once, here, via
	// substrate.LinkKey so it cannot drift from the script's own
	// construction (ddls.go:392, :438).
	LinkKey string
}

// PlanStrandedEpochRetirement derives the exact RevokeRole / RevokePermission
// calls that neutralize one StrandedOperatorEpoch finding: one RevokeRole per
// entry in Holders, one RevokeRole per entry in ReachableVia, one
// RevokePermission per entry in GrantedBy. Pure — no I/O, no NATS — so a
// caller can preview the plan (a --dry-run) without submitting anything.
//
// Holders and ReachableVia both name holdsRole edges into epoch.RoleKey, but
// in different shapes: Holders carries the HOLDER's vertex key (the actor),
// while ReachableVia carries the LINK key itself (strandedepoch.go's own doc
// comments on both fields). The second half of this function recovers the
// actor key from the link key rather than accepting a second key shape into
// RevokeRole's payload — the op itself only ever wants {actorKey, roleKey}.
//
// A ReachableVia entry that is not in fact a holdsRole link into epoch.RoleKey
// is a hard error, never a silent skip: StrandedOperatorEpochs only ever
// appends entries it parsed as exactly that shape (strandedepoch.go's
// liveEdgesInto), but this function does not re-derive that guarantee from
// StrandedOperatorEpochs's own internals — it re-checks the shape itself
// rather than trusting an invariant silently across the same package's
// internal boundary (fire-brief-template.md's standing checklist #6:
// precedent may carry debt no test happens to pin).
func PlanStrandedEpochRetirement(epoch StrandedOperatorEpoch) ([]RevocationOp, error) {
	roleType, roleID, ok := substrate.ParseVertexKey(epoch.RoleKey)
	if !ok || roleType != "role" {
		return nil, fmt.Errorf("plan retirement: %q is not a vtx.role.<id> key", epoch.RoleKey)
	}

	var ops []RevocationOp
	for _, actorKey := range epoch.Holders {
		actorType, actorID, ok := substrate.ParseVertexKey(actorKey)
		if !ok {
			return nil, fmt.Errorf("plan retirement for %s: Holders entry %q is not a vtx.<type>.<id> key",
				epoch.RoleKey, actorKey)
		}
		ops = append(ops, revokeRoleOp(actorKey, actorType, actorID, epoch.RoleKey, roleID))
	}

	for _, linkKey := range epoch.ReachableVia {
		sourceType, sourceID, relation, targetType, targetID, ok := substrate.ParseLinkKey(linkKey)
		if !ok || relation != "holdsRole" || targetType != "role" || targetID != roleID {
			return nil, fmt.Errorf(
				"plan retirement for %s: ReachableVia entry %q is not a holdsRole link into it (invariant violation — StrandedOperatorEpochs must only ever report links of that exact shape)",
				epoch.RoleKey, linkKey)
		}
		actorKey := substrate.VertexKey(sourceType, sourceID)
		ops = append(ops, revokeRoleOp(actorKey, sourceType, sourceID, epoch.RoleKey, roleID))
	}

	for _, permKey := range epoch.GrantedBy {
		permType, permID, ok := substrate.ParseVertexKey(permKey)
		if !ok || permType != "permission" {
			return nil, fmt.Errorf("plan retirement for %s: GrantedBy entry %q is not a vtx.permission.<id> key",
				epoch.RoleKey, permKey)
		}
		linkKey := substrate.LinkKey("permission", permID, "grantedBy", "role", roleID)
		ops = append(ops, RevocationOp{
			OperationType: "RevokePermission",
			Payload:       map[string]any{"permKey": permKey, "roleKey": epoch.RoleKey},
			LinkKey:       linkKey,
		})
	}

	return ops, nil
}

func revokeRoleOp(actorKey, actorType, actorID, roleKey, roleID string) RevocationOp {
	return RevocationOp{
		OperationType: "RevokeRole",
		Payload:       map[string]any{"actorKey": actorKey, "roleKey": roleKey},
		LinkKey:       substrate.LinkKey(actorType, actorID, "holdsRole", "role", roleID),
	}
}

// PermissionDeclaredBy reads a live vtx.permission.<id> vertex's
// data.declaredBy field — the same field internal/pkgmgr/permissionreconcile.go's
// LivePermission.DeclaredBy classifies on for a package-origin permission.
// The retirement CLI uses it to name, for each grant PlanStrandedEpochRetirement
// revoked, the package whose re-apply re-mints it against the current role —
// the permission vertex already names its own declaring package, so no
// package-registry cross-reference is needed.
//
// Returns ok=false with no error when the vertex is absent, tombstoned, or
// carries no declaredBy field at all (kernel- and runtime-origin permissions
// have none, and neither has a package to reinstall) — all three mean
// "nothing to recommend", not a failure. A declaredBy field that IS present
// but is not a string is a malformed permission body, not an absent field,
// and is reported as an error rather than silently folded into the same
// "nothing to recommend" bucket — a cold review found the two were
// indistinguishable, which would have dropped a genuinely package-origin
// grant from the recommendations with no diagnostic at all.
func PermissionDeclaredBy(ctx context.Context, kv jetstream.KeyValue, permKey string) (declaredBy string, ok bool, err error) {
	doc, state, err := readDocument(ctx, kv, permKey)
	if err != nil {
		return "", false, err
	}
	if state != docLive {
		return "", false, nil
	}
	raw, present := doc.Data["declaredBy"]
	if !present {
		return "", false, nil
	}
	name, isString := raw.(string)
	if !isString {
		return "", false, fmt.Errorf("%s: declaredBy is present but not a string (%T)", permKey, raw)
	}
	if name == "" {
		return "", false, nil
	}
	return name, true, nil
}
