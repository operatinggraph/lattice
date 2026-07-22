// Package credentialbinding is the credential→identity resolution seam the
// claim-flow shared-seam amendment requires
// (gateway-claim-flow-identity-provisioning-design.md §11.0/§11.5 R1).
//
// A caller authenticates as a raw credential identity (A, the JWT `sub`).
// Once A claims a pre-existing business identity (U) via ClaimIdentity,
// every subsequent request should act AS U — U carries the business links
// (lease, patient, tasks) capability grants project off. The
// credential-bindings KV bucket is the local, materialized lookup that
// answers "which U, if any, has A claimed?" — folded from the
// identity.claimed event by internal/gateway's own materializer (mirrors the
// token-revocation kill-switch's bucket + materializer shape). Both the
// Gateway's write path and each vertical app's read boundary consult it
// (they already share internal/gateway/auth) — one shared seam, relocated
// from "Gateway-private" once the browser-direct topology sends reads to the
// app and writes to the Gateway.
package credentialbinding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// BucketName is the canonical credential-bindings bucket
// (bootstrap.GatewayCredentialBindingsBucket).
const BucketName = "credential-bindings"

// kvGetter is the minimal read surface Resolve needs — *substrate.KV
// satisfies it; the interface keeps the package test-fakeable without a live
// NATS connection.
type kvGetter interface {
	Get(ctx context.Context, key string) (*substrate.KVEntry, error)
}

// binding is the materialized document shape, folded from the
// identity.claimed{identityKey, actorKey} event.
type binding struct {
	IdentityKey string `json:"identityKey"`
}

// Resolver reads the credential-bindings bucket to answer the shared-seam
// resolution question. Safe for concurrent use (it holds only a read
// handle).
type Resolver struct {
	kv kvGetter
}

// New builds a Resolver over an already-opened credential-bindings bucket
// handle (obtain via substrate.Conn.OpenKV(ctx, credentialbinding.BucketName)).
func New(kv kvGetter) *Resolver {
	return &Resolver{kv: kv}
}

// Resolve looks up actorID's (the raw credential identity, A) claimed
// business identity. bound=false (no error) means A has not claimed
// anything yet — the caller acts as A itself, the documented deny-safe
// fallback (also covers the CDC-lag window between a live claim and this
// bucket observing it). A malformed stored document or a transport/KV error
// is returned so the caller decides its own fallback posture — unlike the
// revocation kill-switch, an unresolved binding denies nothing on its own:
// A simply lacks U's business-scoped grants.
func (r *Resolver) Resolve(ctx context.Context, actorID string) (identityKey string, bound bool, err error) {
	entry, err := r.kv.Get(ctx, actorID)
	switch {
	case err == nil:
		var b binding
		if uerr := json.Unmarshal(entry.Value, &b); uerr != nil {
			return "", false, fmt.Errorf("credentialbinding: malformed binding for %q: %w", actorID, uerr)
		}
		if b.IdentityKey == "" {
			return "", false, fmt.Errorf("credentialbinding: binding for %q missing identityKey", actorID)
		}
		return b.IdentityKey, true, nil
	case errors.Is(err, substrate.ErrKeyNotFound):
		return "", false, nil
	default:
		return "", false, fmt.Errorf("credentialbinding: lookup %q: %w", actorID, err)
	}
}
