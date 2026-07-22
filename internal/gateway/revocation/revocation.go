// Package revocation is the read-path actor kill-switch (D1 increment 2).
//
// A JWT verifies on signature + expiry alone, so a compromised actor keeps read
// access until its (short) token expires. The token-revocation KV bucket is the
// out-of-band kill-switch (lattice-architecture.md "Token Revocation KV …
// Kill-switch for compromised actors (MVP auth mitigation)"; brainstorm #111).
// It is the synchronous, per-request gate the design mandates (§3.4 / M6) — not
// cached, checked on every read — with the short JWT TTL as the backstop for the
// CDC-lag window. The consistency-guaranteeing capability-vector-clock fence is
// the deferred v2 (design M3).
//
// Revocation is keyed by the full identity vertex key (`vtx.identity.<id>`),
// matching the actor id the auth Verifier surfaces — revoking an actor cuts off
// every token it holds at once (the documented "compromised actor" posture),
// which is coarser but stronger than per-token (`jti`) revocation.
package revocation

import (
	"context"
	"errors"
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// BucketName is the canonical token-revocation bucket
// (config.yaml gateway.tokenRevocationBucketName).
const BucketName = "token-revocation"

// kvGetter is the minimal read surface the kill-switch needs. *substrate.KV
// satisfies it; the interface keeps the package test-fakeable without a live
// NATS connection.
type kvGetter interface {
	Get(ctx context.Context, key string) (*substrate.KVEntry, error)
}

// Checker reads the token-revocation bucket to answer the per-request kill-switch
// question. It is safe for concurrent use (it holds only a read handle).
type Checker struct {
	kv kvGetter
}

// New builds a Checker over an already-opened token-revocation bucket handle
// (obtain via substrate.Conn.OpenKV(ctx, revocation.BucketName)).
func New(kv kvGetter) *Checker {
	return &Checker{kv: kv}
}

// IsRevoked reports whether actorID (a full identity vertex key) is on the
// revocation list. Presence of the key — under any value — means revoked; an
// absent key means live. A KV/transport error is wrapped and returned so the
// caller fails closed (a read boundary that cannot confirm the actor is live
// must deny).
func (c *Checker) IsRevoked(ctx context.Context, actorID string) (bool, error) {
	_, err := c.kv.Get(ctx, actorID)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, substrate.ErrKeyNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("revocation: check %q: %w", actorID, err)
	}
}
