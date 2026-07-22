package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// SecureColumn declares one decrypt-at-projection column of a Secure Lens
// (Contract #3 §3.10 "the read-path-authorized Secure Lens";
// vault-crypto-shredding-design.md §2.3 Phase B). Column names the RETURN
// alias whose evaluated value is a sensitive aspect's ciphertext envelope
// ({ct, nonce, keyId} — the aspect's `data` map, reached in cypher as
// `node.<aspect>.data`). IdentityKeyColumn names the RETURN alias carrying
// the OWNING identity's vertex key (vtx.identity.<id>) — the key custody
// anchor whose piiKey wraps the DEK. Field optionally selects one field of
// the decrypted plaintext object to project (e.g. "value"); empty projects
// the whole decrypted object.
type SecureColumn struct {
	Column            string `json:"column"`
	IdentityKeyColumn string `json:"identityKeyColumn"`
	Field             string `json:"field,omitempty"`
}

// coreKVGetter is the single point-read the decryptor needs from Core KV
// (the identity's piiKey aspect). *substrate.KV satisfies it; tests inject a
// fake.
type coreKVGetter interface {
	Get(ctx context.Context, key string) (*substrate.KVEntry, error)
}

// SecureDecryptor rewrites Secure-Lens projection rows in place: each
// declared SecureColumn's ciphertext envelope is decrypted under the owning
// identity's DEK before the row reaches the (RLS-protected) adapter. This is
// the ONLY place Refractor produces plaintext from a sensitive aspect — the
// default projection path copies ciphertext as-is (Contract #3 §3.10
// "Readers").
//
// Shred semantics (the crypto-shred guarantee at the projection surface): a
// column whose identity key has been shredded projects NULL — so any
// reprojection of a shredded identity's row self-nullifies its PII by
// construction. The pipeline guarantees that reprojection happens: a CDC
// event on the anchor's piiKey aspect (which every shred commits) triggers
// re-evaluation on a secure lens (Pipeline.handle's KindAspect branch), so
// the plaintext row is overwritten with null PII without waiting for an
// unrelated anchor event.
//
// The retry queue is NOT safe for a Secure Lens: it captures the decrypted
// row and re-upserts it verbatim, so a shred between capture and drain would
// resurrect pre-shred plaintext. No production wiring installs a retry queue
// today; keep it that way for any lens with a decryptor (transient write
// failures Nak → re-evaluate → fresh decrypt, which is the safe path).
type SecureDecryptor struct {
	vault   vault.Vault
	coreKV  coreKVGetter
	columns []SecureColumn
	// calls counts Vault.Decrypt invocations for the Contract #5 §5.4
	// vault_calls_total heartbeat metric. Shared across all Secure Lenses in
	// the process; nil disables counting.
	calls *atomic.Uint64
}

// NewSecureDecryptor builds a decryptor for one lens's declared secure
// columns. v and coreKV must be non-nil; calls may be nil.
func NewSecureDecryptor(v vault.Vault, coreKV coreKVGetter, columns []SecureColumn, calls *atomic.Uint64) (*SecureDecryptor, error) {
	if v == nil {
		return nil, errors.New("pipeline: secure decryptor: vault must not be nil")
	}
	if coreKV == nil {
		return nil, errors.New("pipeline: secure decryptor: core KV must not be nil")
	}
	if len(columns) == 0 {
		return nil, errors.New("pipeline: secure decryptor: at least one secure column required")
	}
	return &SecureDecryptor{vault: v, coreKV: coreKV, columns: columns, calls: calls}, nil
}

// Apply decrypts every declared secure column across results, mutating rows
// in place. Delete results and nil rows pass through untouched.
//
// Failure posture (fail closed, never fail open):
//   - absent aspect (column value null) → column stays null.
//   - shredded identity key → column projects null (right-to-erasure).
//   - malformed envelope, plaintext where ciphertext was declared, missing
//     identity-key column, ciphertext with no piiKey, or authenticated-decrypt
//     failure → a Terminal error: the row is never written (DLQ + health), so
//     a defect can suppress a projection but can never leak ciphertext into a
//     plaintext column or vice versa.
//   - piiKey read infra errors surface unwrapped for the standard
//     infra/transient classification (retry/pause).
func (d *SecureDecryptor) Apply(ctx context.Context, results []ruleengine.EvalResult) error {
	for i := range results {
		if results[i].Delete || results[i].Row == nil {
			continue
		}
		for _, col := range d.columns {
			if err := d.decryptColumn(ctx, results[i].Row, col); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *SecureDecryptor) decryptColumn(ctx context.Context, row map[string]any, col SecureColumn) error {
	raw, present := row[col.Column]
	if !present || raw == nil {
		// The aspect is absent on this vertex (e.g. an identity with no phone)
		// — a null column is the correct projection.
		return nil
	}
	ctMap, ok := raw.(map[string]any)
	if !ok {
		return failure.Terminal(fmt.Errorf(
			"pipeline: secure column %q: value is %T, not a ciphertext envelope map — the cypher must return the sensitive aspect's data (node.<aspect>.data)",
			col.Column, raw))
	}
	ct, err := ciphertextFromMap(ctMap)
	if err != nil {
		return failure.Terminal(fmt.Errorf("pipeline: secure column %q: %w", col.Column, err))
	}

	identityKey, _ := row[col.IdentityKeyColumn].(string)
	if identityKey == "" {
		return failure.Terminal(fmt.Errorf(
			"pipeline: secure column %q: identity-key column %q is empty for a non-null ciphertext — cannot resolve key custody",
			col.Column, col.IdentityKeyColumn))
	}

	envelope, err := d.readPiiKeyEnvelope(ctx, identityKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			// Ciphertext exists but its identity has no piiKey — an invariant
			// violation (step 6.5 mints the piiKey in the same atomic batch as
			// the first sensitive write; a shred leaves a durable placeholder).
			return failure.Terminal(fmt.Errorf(
				"pipeline: secure column %q: identity %s has ciphertext but no piiKey aspect", col.Column, identityKey))
		}
		// Infra (KV unreachable, …) — classify via the standard path.
		return fmt.Errorf("pipeline: secure column %q: read piiKey for %s: %w", col.Column, identityKey, err)
	}

	if d.calls != nil {
		d.calls.Add(1)
	}
	plaintext, err := d.vault.Decrypt(ctx, identityKey, envelope, ct)
	if err != nil {
		if errors.Is(err, vault.ErrKeyShredded) {
			// Shredded → the PII is gone; project null and keep the row.
			row[col.Column] = nil
			return nil
		}
		// ErrDecryptFailed / ErrInvalidEnvelope (tampered ciphertext, wrong
		// identity binding) — permanently unrecoverable for this input.
		return failure.Terminal(fmt.Errorf("pipeline: secure column %q: decrypt under %s: %w", col.Column, identityKey, err))
	}

	var value map[string]any
	if err := json.Unmarshal(plaintext, &value); err != nil {
		return failure.Terminal(fmt.Errorf("pipeline: secure column %q: decrypted plaintext is not a JSON object: %w", col.Column, err))
	}
	if col.Field != "" {
		fv, present := value[col.Field]
		if !present {
			// The plaintext decrypted fine but lacks the declared field — a
			// spec/DDL mismatch. Fail loud rather than project a null that is
			// indistinguishable from a legitimate shred or absent aspect.
			return failure.Terminal(fmt.Errorf(
				"pipeline: secure column %q: decrypted object has no field %q — the secureColumns declaration does not match the aspect's plaintext shape",
				col.Column, col.Field))
		}
		row[col.Column] = fv
		return nil
	}
	row[col.Column] = value
	return nil
}

// readPiiKeyEnvelope point-reads and parses vtx.identity.<id>.piiKey — the
// same aspect shape the Processor's commit path writes (the aspect envelope's
// `data` field carries the vault.Envelope). A soft-deleted piiKey aspect is
// treated as absent (the engine treats soft-deleted aspects as absent too —
// the two layers must agree, and a "deleted" key must never open ciphertext).
// A piiKey that exists but cannot be parsed is permanently unusable, so the
// error is Terminal — a Transient classification would Nak-loop the
// triggering event forever.
func (d *SecureDecryptor) readPiiKeyEnvelope(ctx context.Context, identityKey string) (vault.Envelope, error) {
	entry, err := d.coreKV.Get(ctx, identityKey+".piiKey")
	if err != nil {
		return vault.Envelope{}, err
	}
	if entry == nil || len(entry.Value) == 0 {
		return vault.Envelope{}, substrate.ErrKeyNotFound
	}
	var doc struct {
		IsDeleted bool           `json:"isDeleted"`
		Data      vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return vault.Envelope{}, failure.Terminal(fmt.Errorf("parse piiKey %s.piiKey: %w", identityKey, err))
	}
	if doc.IsDeleted {
		return vault.Envelope{}, substrate.ErrKeyNotFound
	}
	return doc.Data, nil
}

// ciphertextFromMap re-parses a generically-decoded ciphertext envelope map
// ({ct, nonce, keyId} with base64-string byte fields) into a typed
// vault.Ciphertext, rejecting a map that cannot have been produced by
// step 6.5's encrypt-on-write (no ct bytes).
func ciphertextFromMap(m map[string]any) (vault.Ciphertext, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return vault.Ciphertext{}, fmt.Errorf("marshal ciphertext envelope: %w", err)
	}
	var ct vault.Ciphertext
	if err := json.Unmarshal(raw, &ct); err != nil {
		return vault.Ciphertext{}, fmt.Errorf("parse ciphertext envelope: %w", err)
	}
	if len(ct.CT) == 0 {
		return vault.Ciphertext{}, errors.New("not a ciphertext envelope (empty ct)")
	}
	return ct, nil
}
