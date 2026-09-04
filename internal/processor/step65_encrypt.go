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
// conditions (it only defaults update/tombstone), so movedConditionedKeys can
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
			// A resolution that suffered a live-read fault is not a "not
			// sensitive" answer — it is no answer at all. Step 6 ran first
			// against this same shared live-read budget and may have resolved
			// this very class as sensitive before exhausting it; reading the
			// degraded result as a miss would commit the aspect as PLAINTEXT.
			// Reject the operation instead.
			//
			// The fault ALONE is the test, matching what step 6 does with one:
			// a fault ENDS the governing-DDL walk where it happened — both the
			// instanceOf read and the committed-class read stop it and resolve
			// empty — so no resolution can stand behind one, and a hop this
			// step could not read is a hop whose DDL may be the sensitive one.
			//
			// The rejection is TERMINAL, not a retry: handleStubFailure replies
			// and Terms the message, and the budget counter is monotone within
			// an execution, so the submitter resubmits rather than the platform
			// redelivering into the same exhausted budget.
			return nil, false, fmt.Errorf("step 6.5: resolve DDL for %s hit a live-read fault; refusing to treat the degraded resolution as non-sensitive: %w", m.Key, fault)
		}
		// Only an ASPECT can carry sensitivity — the install refuses `sensitive`
		// on every other DDL class (internal/pkgmgr/sensitivescope.go) — so a
		// cache that cannot adjudicate a vertex or link mutation is withholding
		// an answer that could not have changed this step's behavior anyway.
		// Scoping the refusal to aspects is therefore not leniency, it is the
		// blast radius the risk actually has: vertexRootForResolve returns no
		// root for a link, so EVERY link whose class is not itself a DDL
		// resolves empty, and rejecting those would take out ordinary link
		// writes across every package for no protection at all.
		if kind == substrate.KindAspect {
			if err := degradedCacheRefusal(resolver.DDLs, m.Key, class, ref, ok); err != nil {
				return nil, false, err
			}
		}
		if !ok || !ref.Sensitive {
			continue
		}
		// Sensitivity is an ASPECT-level property — the install refuses
		// Sensitive on any other DDL class — and step 6 gates its custody
		// check the same way. A vertex or link mutation whose class merely
		// collides with a sensitive aspect DDL's canonicalName is not a
		// sensitive aspect, so it passes through here exactly as step 6 lets
		// it pass. Keeping the two gates identical is the point: an asymmetry
		// is what lets one step act on a mutation the other never checked.
		if kind != substrate.KindAspect {
			continue
		}
		holderKey, ok := keyHolderFor(ref, kind, m.Key)
		if !ok {
			// The DDL says this data is sensitive and we could not determine a
			// key holder for it. Step 6 rejects every shape that reaches here,
			// but that is an invariant across two steps reading a cache a
			// concurrent meta-commit can invalidate BETWEEN them — not a
			// guarantee. Skipping would commit the plaintext of a declared
			// sensitive aspect, so this fails the operation rather than
			// trusting the earlier step to still agree.
			return nil, false, fmt.Errorf("step 6.5: sensitive class %q on %s resolves to no key holder (custody kind %q); refusing to commit it unencrypted", ref.CanonicalName, m.Key, ref.CustodyKind)
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

// degradedCacheRefusal reports the reason a DDL cache that has failed an
// invalidation cannot be trusted to answer THIS aspect mutation's sensitivity,
// or nil when it can. Called only for aspects, the sole kind an install lets
// declare `sensitive`.
//
// Three distinct failures of the same cause, each needing its own test because
// they present as different lookup results. A committed DDL reaches this process
// only through Invalidate:
//
//   - resolved=false: the class may be declared sensitive in Core KV and the
//     declaration simply never arrived. The missed root's canonicalName is
//     exactly what was never read, so nothing about THIS class can rule it out,
//     and Contract #1 §1.6's permissive default — sound over a cache that knows
//     itself complete — cannot be invoked here.
//   - resolved only by the instanceOf-chain FALLBACK: the exact lookup on the
//     mutation's own class missed, and the walk then terminated on an ancestor
//     type whose DDL is perfectly fresh. That answer is about the ANCESTOR, and
//     a coarse parent type is rarely itself sensitive, so a missed fine-grained
//     DDL is silently answered "not sensitive" by a name nothing flagged. The
//     exact-hit requirement is deliberately broader than the stale-name test: a
//     resolution that never consulted this class's own entry cannot be checked
//     against it, so while degraded the walk is not trusted at all.
//   - an EXACT hit on a possibly-stale name: a failing Invalidate returns before
//     it touches byRoot, so the PRIOR entry survives whole. A package upgrade
//     flipping `sensitive` false→true on an existing DDL is an update to a root
//     already indexed, and the cache keeps answering with the old `false` — a hit
//     that would commit the aspect as plaintext just as surely as a miss would.
//     Custody travels with the same entry, so a stale hit that still says
//     sensitive is no safer: it would encrypt under whatever holder the
//     pre-upgrade declaration named.
//
// All three are TERMINAL, like the live-read-fault refusal above: nothing in a
// redelivery re-reads Core KV for these classes, so it would land on the
// identical state and only an operator restart can change the answer.
func degradedCacheRefusal(cache *DDLCache, mutationKey, class string, ref MetaVertexRef, resolved bool) error {
	degraded, since, failedKey, cacheErr := cache.Degraded()
	if !degraded {
		return nil
	}
	if !resolved {
		return fmt.Errorf("step 6.5: resolve DDL for %s came back empty from a DDL cache degraded since %s (invalidate of %s failed: %v); refusing to treat it as non-sensitive",
			mutationKey, substrate.FormatTimestamp(since), failedKey, cacheErr)
	}
	if _, exact := cache.Lookup(class); !exact {
		return fmt.Errorf("step 6.5: class %q on %s resolved only through the instanceOf chain, from a DDL cache degraded since %s (invalidate of %s failed: %v); refusing to adjudicate its sensitivity from an ancestor's DDL",
			class, mutationKey, substrate.FormatTimestamp(since), failedKey, cacheErr)
	}
	if cache.PossiblyStale(ref.CanonicalName) {
		return fmt.Errorf("step 6.5: DDL %q governing %s may be stale — its invalidation failed and the cache has been degraded since %s (invalidate of %s failed: %v); refusing to adjudicate sensitivity from an entry that may predate a committed change",
			ref.CanonicalName, mutationKey, substrate.FormatTimestamp(since), failedKey, cacheErr)
	}
	return nil
}

// keyHolderFor resolves WHICH vertex custodies this mutation's DEK
// (retention-class-key-custody-design.md §4.2). Custody is a function of the
// resolved DDL and nothing else: a retentionClass DDL names its holder, and
// every other DDL derives the aspect's own anchoring identity. Returns false
// when neither yields a usable holder; the caller treats that as a hard error
// rather than as permission to commit plaintext.
//
// The kind gate is load-bearing and mirrors step 6's own (`ref.Sensitive &&
// kind == KindAspect`). A DDL cache lookup is keyed on canonicalName alone, so
// a VERTEX mutation carrying an aspect DDL's class resolves to that aspect
// DDL; step 6 skips its custody check for a non-aspect mutation, so without
// this gate step 6.5 would encrypt a vertex root under a holder nothing
// validated and no read path would ever decrypt it.
func keyHolderFor(ref MetaVertexRef, kind substrate.KeyKind, mutationKey string) (string, bool) {
	if kind != substrate.KindAspect {
		return "", false
	}
	switch ref.CustodyKind {
	case CustodyKindRetentionClass:
		if ref.CustodyHolderKey == "" {
			return "", false
		}
		return ref.CustodyHolderKey, true
	case "", CustodyKindIdentity:
		vertexKey, vertexType, _, _, ok := substrate.ParseAspectKey(mutationKey)
		if !ok || vertexType != "identity" {
			return "", false
		}
		return vertexKey, true
	default:
		// An unrecognized kind must NOT fall through to the identity
		// derivation. Doing so would custody a record on the data subject
		// whenever the declaration was garbled — silently choosing the one
		// custodian a retained record was declared to outlive.
		return "", false
	}
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
	// A PLATFORM-AUTHORED mutation, appended after step 6 and therefore exempt
	// from the permittedCommands gate — the standing exemption the task
	// auto-complete injection documents (autocomplete.go). Gating it would
	// refuse every operation that writes an identity's FIRST sensitive aspect,
	// since the `piiKey` DDL names the privacy operations and not the business
	// one that triggered the mint. It is sound for the same reason: every field
	// below is fixed by the Processor — the class, the vertex it holds a key
	// for, its local name, and the Vault envelope this call just created — with
	// nothing submitter-supplied riding along for a gate to adjudicate.
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
