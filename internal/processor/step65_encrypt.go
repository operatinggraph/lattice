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
// Sensitivity resolution is exact-class-name only (DDLs.Lookup), unlike step
// 6's resolveGoverningDDL, which additionally walks a bounded instanceOf
// chain to a fine-grained discriminator class's type authority. No shipped
// sensitive DDL today (ssn/dob/name/email/phone/claimKey/credentialBinding)
// needs that walk — all register under their own exact canonical name — but a
// FUTURE sensitive DDL resolvable only via the chain would silently commit as
// plaintext here (Lookup miss) while step 6 still scope-checks it correctly.
// Known limitation, not exercised by any package today; fold in the shared
// resolution path if that pattern is ever used for a sensitive aspect.
//
// Returns mintedPiiKey=true when this call minted a NEW piiKey (the identity
// had none yet) — a "create" mutation, which applyHydratedRevisions never
// conditions (it only defaults update/tombstone), so movedDefaultedKeys can
// never attribute a piiKey create-once collision to a benign race. The caller
// (commitPipeline's OCC retry) treats mintedPiiKey as an independent
// retry-eligible signal alongside `moved`, so two concurrent first-sensitive
// writes for the same identity get a transparent retry instead of a hard
// rejection.
func (cp *CommitPath) encryptSensitiveMutations(ctx context.Context, mutations []MutationOp) ([]MutationOp, bool, error) {
	var extra []MutationOp
	envelopes := make(map[string]vault.Envelope) // vertexKey -> envelope, cached for this batch

	out := make([]MutationOp, len(mutations))
	copy(out, mutations)

	for i := range out {
		m := &out[i]
		if m.Op == "tombstone" || m.Document == nil {
			continue
		}
		class, _ := m.Document["class"].(string)
		if class == "" {
			continue
		}
		ref, ok := cp.deps.DDLs.Lookup(class)
		if !ok || !ref.Sensitive {
			continue
		}
		vertexKey, vertexType, _, _, ok := substrate.ParseAspectKey(m.Key)
		if !ok || vertexType != "identity" {
			// step 6 already rejected a non-identity-anchored sensitive
			// aspect; a malformed key here would have failed validation.
			continue
		}
		env, ok := envelopes[vertexKey]
		if !ok {
			var err error
			env, err = cp.ensureIdentityKey(ctx, vertexKey, &extra)
			if err != nil {
				return nil, false, fmt.Errorf("step 6.5: ensure piiKey for %s: %w", vertexKey, err)
			}
			envelopes[vertexKey] = env
		}
		plaintext, err := json.Marshal(m.Document["data"])
		if err != nil {
			return nil, false, fmt.Errorf("step 6.5: marshal plaintext for %s: %w", m.Key, err)
		}
		ct, err := cp.deps.Vault.Encrypt(ctx, vertexKey, env, plaintext)
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

// ensureIdentityKey returns vertexKey's existing piiKey envelope, or mints a
// fresh one and appends its create mutation to *extra when the identity has
// no piiKey yet (design §2.1 lazy creation — a non-sensitive identity never
// gets one). Called at most once per identity per batch — callers cache the
// result across the batch's mutations.
func (cp *CommitPath) ensureIdentityKey(ctx context.Context, vertexKey string, extra *[]MutationOp) (vault.Envelope, error) {
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
