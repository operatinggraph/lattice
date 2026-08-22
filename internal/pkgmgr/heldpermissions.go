package pkgmgr

import (
	"context"
	"fmt"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/capabilitykv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// HeldPermissionReader is the minimal Capability-KV surface
// ReadHeldPermissions needs — the same shape as capabilitykv.KVGetter.
// *substrate.Conn satisfies it; a test satisfies it with a fake that returns
// canned bytes for a fixed key.
type HeldPermissionReader interface {
	KVGet(ctx context.Context, bucket, key string) (*substrate.KVEntry, error)
}

// ReadHeldPermissions answers "what does this actor hold, as step 3 would see
// it" — the one implementation of that question, for every caller that needs
// the HeldPermission bound on what a `grant` proposal may confer
// (ValidateCapabilityArtifact's §5 scope check, capabilitymaterializer.go's
// requesterHolds).
//
// The routing is capabilitykv.ReadPlatformDoc's, not this package's: a system
// actor unions its cap.<rest> anchor with cap.roles.<rest>, every other actor
// reads cap.roles.<rest> alone. Deriving those keys by string concatenation
// instead answers a different question from the one the Processor answers in
// an rbac-active deployment — an ordinary actor's anchor doc, if the
// projection left one behind, would count as held and every permission in it
// would become grantable.
//
// systemActorKeys is the live set from bootstrap.SystemActorKeys. It is
// graph-derived, so a caller resolves it as the platform binaries do — once
// per process, held for the process lifetime (cmd/processor/main.go:142) —
// rather than per call.
//
// An absent key contributes no permissions (deny-closed union, not an error);
// a real read error propagates rather than silently degrading the grant set
// into a smaller — and therefore falsely permissive-looking — bound.
func ReadHeldPermissions(ctx context.Context, reader HeldPermissionReader, systemActorKeys []string, actor string) ([]HeldPermission, error) {
	doc, _, err := capabilitykv.ReadPlatformDoc(ctx, reader, bootstrap.CapabilityKVBucket, systemActorKeys, actor)
	if err != nil {
		return nil, fmt.Errorf("read held permissions for %s: %w", actor, err)
	}
	if doc == nil {
		return nil, nil
	}
	held := make([]HeldPermission, 0, len(doc.PlatformPermissions))
	for _, p := range doc.PlatformPermissions {
		// Origin travels with the entry: a reserved op held at runtime origin
		// is refused at step 3, so it must not count as held when proposing a
		// grant of that same op (HeldPermission.covers).
		held = append(held, HeldPermission{
			OperationType: p.OperationType, Scope: p.Scope, Origin: p.Origin,
		})
	}
	return held, nil
}
