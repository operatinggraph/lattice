package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// encryptSensitiveMutations implements commit-path step 6.5 (Contract #3
// §3.10, vault-crypto-shredding-design.md §2.2): every mutation whose DDL
// declares sensitive:true is encrypted under its identity's DEK before the
// atomic batch — Core KV never sees plaintext for a sensitive aspect. Lazily
// mints the identity's piiKey aspect (Vault.CreateIdentityKey) on the first
// sensitive write of a batch, appending that create to the SAME batch so the
// key and its first protected value commit atomically. Non-sensitive
// mutations and tombstones (no data to encrypt) pass through unchanged. Runs
// AFTER step 6 (Validate), which checked the plaintext shape/anchoring — the
// stored bytes are opaque ciphertext, deliberately unchecked by DDL schema.
//
// Sensitivity resolution reuses step 6's own ddlResolver (exact class→DDL
// lookup first, then the bounded instanceOf-chain walk over the SAME batch +
// hydrated working set step 6 validated against), so a fine-grained
// discriminator class resolvable only via the chain is encrypted exactly as
// consistently as step 6 scope-checked it — no separate, narrower resolution
// path to drift out of sync.
//
// Returns mintedPiiKey=true when this call minted a NEW piiKey (the identity
// had none yet) — a "create" mutation, which applyHydratedRevisions never
// conditions (it only defaults update/tombstone), so movedDefaultedKeys can
// never attribute a piiKey create-once collision to a benign race. The caller
// (commitPipeline's OCC retry) treats mintedPiiKey as an independent
// retry-eligible signal alongside `moved`, so two concurrent first-sensitive
// writes for the same identity get a transparent retry instead of a hard
// rejection.
func (cp *CommitPath) encryptSensitiveMutations(ctx context.Context, mutations []MutationOp, state HydratedState) ([]MutationOp, bool, error) {
	var extra []MutationOp
	envelopes := make(map[string]vault.Envelope) // vertexKey -> envelope, cached for this batch

	out := make([]MutationOp, len(mutations))
	copy(out, mutations)

	resolver := &ddlResolver{
		DDLs:        cp.deps.DDLs,
		linkReader:  &connInstanceOfReader{conn: cp.deps.Conn, coreBucket: cp.deps.CoreBucket},
		classReader: &connVertexClassReader{conn: cp.deps.Conn, coreBucket: cp.deps.CoreBucket},
		Logger:      cp.deps.Logger,
	}
	result := ScriptResult{Mutations: mutations}

	for i := range out {
		m := &out[i]
		if m.Op == "tombstone" || m.Document == nil {
			continue
		}
		class, _ := m.Document["class"].(string)
		if class == "" {
			continue
		}
		kind := substrate.ClassifyKey(m.Key)
		ref, ok, fault := resolver.resolveGoverningDDLChecked(ctx, class, m.Key, kind, result, state)
		if fault != nil {
			// A DEGRADED resolution is not a "not sensitive" answer. Step 6
			// ran first against this same shared live-read budget and may have
			// resolved this very class as sensitive before exhausting it; were
			// we to treat the fault as a miss, the aspect would commit as
			// plaintext. Fail the operation instead — the caller's OCC/stub
			// handling replies a failure, and a retry gets a fresh budget.
			return nil, false, fmt.Errorf("step 6.5: resolve DDL for %s degraded by a live-read fault; refusing to treat it as non-sensitive: %w", m.Key, fault)
		}
		if !ok || !ref.Sensitive {
			continue
		}
		holderKey, ok := keyHolderFor(ref, m.Key)
		if !ok {
			// step 6 already rejected a sensitive aspect whose custody it
			// could not satisfy; a malformed key here would have failed
			// validation.
			continue
		}
		env, ok := envelopes[holderKey]
		if !ok {
			var err error
			env, err = cp.ensureKeyHolderKey(ctx, holderKey, &extra)
			if err != nil {
				return nil, false, fmt.Errorf("step 6.5: ensure piiKey for %s: %w", holderKey, err)
			}
			envelopes[holderKey] = env
		}
		plaintext, err := json.Marshal(m.Document["data"])
		if err != nil {
			return nil, false, fmt.Errorf("step 6.5: marshal plaintext for %s: %w", m.Key, err)
		}
		ct, err := cp.deps.Vault.Encrypt(ctx, holderKey, env, plaintext)
		if err != nil {
			return nil, false, fmt.Errorf("step 6.5: encrypt %s: %w", m.Key, err)
		}
		// A fresh Document map, not the caller's shared one: m.Document still
		// points at the same map result.Mutations[i].Document does (out is a
		// shallow copy — struct fields only), so writing through m would
		// mutate the pre-step-6.5 mutation set any other holder of that slice
		// observes (e.g. a future audit/logging seam capturing "what the
		// script proposed" before encryption).
		doc := make(map[string]interface{}, len(m.Document))
		for k, v := range m.Document {
			doc[k] = v
		}
		doc["data"] = ct
		m.Document = doc
	}
	return append(out, extra...), len(extra) > 0, nil
}

// keyHolderFor resolves WHICH vertex custodies this aspect's DEK
// (retention-class-key-custody-design.md §4.2). Custody is a function of the
// resolved DDL and nothing else: a retentionClass DDL names its holder, and
// every other DDL falls back to today's derivation — the aspect's own
// anchoring identity. Returns false when neither yields a usable holder,
// which step 6 has already rejected upstream.
func keyHolderFor(ref MetaVertexRef, mutationKey string) (string, bool) {
	if ref.CustodyKind == CustodyKindRetentionClass {
		if ref.CustodyHolderKey == "" {
			return "", false
		}
		return ref.CustodyHolderKey, true
	}
	vertexKey, vertexType, _, _, ok := substrate.ParseAspectKey(mutationKey)
	if !ok || vertexType != "identity" {
		return "", false
	}
	return vertexKey, true
}

// ensureKeyHolderKey returns the key holder's existing piiKey envelope, or
// mints a fresh one and appends its create mutation to *extra when the holder
// has none yet (design §2.1 lazy creation — a holder with no sensitive data
// never gets a key). Called at most once per holder per batch — callers cache
// the result across the batch's mutations, so a batch touching many records
// under one retention class mints once.
//
// The holder is not necessarily the aspect's parent: for a retention class it
// is a vertex the operation never named, so this appends a create on a key
// outside the operation's own footprint. No shipped permission check is keyed
// on the committed footprint, but anything that later audits one must expect a
// vtx.retentionclass.* key in a batch whose operation names only its records.
func (cp *CommitPath) ensureKeyHolderKey(ctx context.Context, vertexKey string, extra *[]MutationOp) (vault.Envelope, error) {
	piiKeyKey := vertexKey + ".piiKey"
	entry, err := cp.deps.Conn.KVGet(ctx, cp.deps.CoreBucket, piiKeyKey)
	if err == nil {
		var doc struct {
			Data vault.Envelope `json:"data"`
		}
		if uerr := json.Unmarshal(entry.Value, &doc); uerr != nil {
			return vault.Envelope{}, fmt.Errorf("parse piiKey %s: %w", piiKeyKey, uerr)
		}
		return doc.Data, nil
	}
	if !errors.Is(err, substrate.ErrKeyNotFound) {
		return vault.Envelope{}, fmt.Errorf("read piiKey %s: %w", piiKeyKey, err)
	}

	env, cerr := cp.deps.Vault.CreateIdentityKey(ctx, vertexKey)
	if cerr != nil {
		return vault.Envelope{}, fmt.Errorf("create identity key for %s: %w", vertexKey, cerr)
	}
	*extra = append(*extra, MutationOp{
		Op:  "create",
		Key: piiKeyKey,
		Document: map[string]interface{}{
			"class":     "piiKey",
			"vertexKey": vertexKey,
			"localName": "piiKey",
			"isDeleted": false,
			"data":      env,
		},
	})
	return env, nil
}
