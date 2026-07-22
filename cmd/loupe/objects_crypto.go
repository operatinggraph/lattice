package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// fetchPiiKeyEnvelope reads identityKey's wrapped-DEK Envelope off its
// piiKey aspect via a direct Core-KV read — the P5 inspector exception only
// Loupe (and the platform binaries) get. A vertical app cannot do this read;
// its P5-compliant path is the privacy-base piiKeyEnvelope lens instead
// (object-store-crypto-shred-design.md §9 Fire 4 grounding finding).
func fetchPiiKeyEnvelope(ctx context.Context, conn *substrate.Conn, identityKey string) (vault.Envelope, error) {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, identityKey+".piiKey")
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return vault.Envelope{}, fmt.Errorf("%s has no piiKey — Vault.CreateIdentityKey must run first (e.g. via a sensitive aspect write)", identityKey)
		}
		return vault.Envelope{}, fmt.Errorf("get %s.piiKey: %w", identityKey, err)
	}
	var doc struct {
		Data vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return vault.Envelope{}, fmt.Errorf("parse %s.piiKey: %w", identityKey, err)
	}
	return doc.Data, nil
}
