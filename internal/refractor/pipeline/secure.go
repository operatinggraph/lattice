package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
// `node.<aspect>.data`). Field optionally selects one field of the decrypted
// plaintext object to project (e.g. "value"); empty projects the whole
// decrypted object.
//
// HolderTypes enumerates the vertex types whose keys may open this column —
// ["identity"] for a subject-custodied record, ["retentionclass"] for one whose
// custody follows a retention obligation instead. Required and non-empty.
//
// It does not SELECT the decryption key: custody is read from the ciphertext's
// own keyId (vault.KeyHolder), which is the AEAD associated data the ciphertext
// was sealed under and therefore the only self-authenticating answer. What
// HolderTypes does is bound which of those answers this column will act on, so
// a lens that never meant to render a retained record cannot be made to by a
// ciphertext that names one. It is also the declared key by which an erasure
// mechanism asks "which lenses' rows depend on holder type T?" without parsing
// compiled cypher.
//
// A list, because re-classification is legal and does not migrate existing
// ciphertext: after a class change one column legitimately carries keyIds of two
// holder types across rows, and widening the list is the lens author's explicit
// act rather than something inferred from the corpus.
type SecureColumn struct {
	Column      string   `json:"column"`
	HolderTypes []string `json:"holderTypes"`
	Field       string   `json:"field,omitempty"`
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
	for _, col := range columns {
		// The spec validators refuse an empty holder-type list, but the
		// invariant belongs here too: an empty list makes every membership test
		// false, so the column would refuse every ciphertext it is ever given
		// while HolderTypes() reports no dependency on any holder at all. Fail
		// at construction rather than three packages away.
		if len(col.HolderTypes) == 0 {
			return nil, fmt.Errorf("pipeline: secure decryptor: column %q declares no holderTypes", col.Column)
		}
	}
	return &SecureDecryptor{vault: v, coreKV: coreKV, columns: columns, calls: calls}, nil
}

// HolderTypes returns the distinct holder types across every declared column —
// the set of vertex types whose key destruction this lens's rows depend on.
// Callers use it to answer "does this lens react to holder type T?" without
// parsing compiled cypher, which is the whole reason the type is declared.
func (d *SecureDecryptor) HolderTypes() []string {
	seen := make(map[string]struct{}, len(d.columns))
	out := make([]string, 0, len(d.columns))
	for _, col := range d.columns {
		for _, ht := range col.HolderTypes {
			if _, dup := seen[ht]; dup {
				continue
			}
			seen[ht] = struct{}{}
			out = append(out, ht)
		}
	}
	return out
}

// Columns returns this decryptor's declared secure columns' RETURN aliases —
// the exact set the divergence audit's comparison must exclude (§4.1 of the
// secure-plain-lens-retraction-and-audit-design): the audit's recompute never
// calls this decryptor, so a secure column's computed value is always the raw
// ciphertext envelope while the stored value is the decrypted plaintext (or
// null), and comparing them would read as `stale` forever. Order follows
// declaration order; SecureColumn.Column names a distinct RETURN alias per
// column, so no de-duplication is needed.
func (d *SecureDecryptor) Columns() []string {
	out := make([]string, len(d.columns))
	for i, col := range d.columns {
		out[i] = col.Column
	}
	return out
}

// SecureRedaction records one column this decryptor could not resolve and
// therefore projected as null. It is the payload of the privacy-critical alarm
// the caller raises: Column names the projected alias, Reason carries the
// precise refusal the resolution failed with.
type SecureRedaction struct {
	Column string
	Reason error
}

// Apply decrypts every declared secure column across results, mutating rows
// in place, and returns the columns it redacted. Delete results and nil rows
// pass through untouched.
//
// Failure posture (fail closed, never fail open):
//   - absent aspect (column value null) → column stays null, no redaction: an
//     aspect a vertex never had is not a failure to resolve one.
//   - shredded key → column projects null, no redaction: erasure is this
//     mechanism working, and alarming on it would drown the signal in the
//     expected case.
//   - malformed envelope, plaintext where ciphertext was declared, a holder
//     type the column never declared, ciphertext with no piiKey, an unparseable
//     piiKey, or authenticated-decrypt failure → the column projects null and
//     the row is KEPT, recorded as a SecureRedaction for the caller to alarm on
//     (retention-class-key-custody-design.md §6.2, fork F2). A Terminal error
//     here used to discard the whole result set, which left a previously
//     projected PLAINTEXT row standing — the wrong direction on this plane, and
//     one bad row then blocked every other row of the lens from updating,
//     including a later erasure scrub. Redaction can never over-reveal.
//   - piiKey read infra errors surface unwrapped for the standard
//     infra/transient classification (retry/pause). These are deliberately NOT
//     redactions: they are recoverable and the row is untouched, so retrying
//     converges, whereas nulling a live column on a KV blip would destroy good
//     plaintext and flap it back on the next reprojection.
func (d *SecureDecryptor) Apply(ctx context.Context, results []ruleengine.EvalResult) ([]SecureRedaction, error) {
	var redactions []SecureRedaction
	for i := range results {
		if results[i].Delete || results[i].Row == nil {
			continue
		}
		for _, col := range d.columns {
			err := d.decryptColumn(ctx, results[i].Row, col)
			if err == nil {
				continue
			}
			// The redaction decision keys off the classification the refusal
			// already carries, so every call site keeps its precise message and
			// only its DISPOSITION changes: Terminal (unresolvable for this
			// input, however often it is retried) redacts; anything else is
			// transient and still propagates.
			if failure.Classify(err) != failure.CatTerminal {
				return redactions, err
			}
			results[i].Row[col.Column] = nil
			redactions = append(redactions, SecureRedaction{Column: col.Column, Reason: err})
		}
	}
	return redactions, nil
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

	// Custody comes from the ciphertext, never from the row. vault.KeyHolder
	// reads the keyId the ciphertext was sealed under, so a projected column
	// the cypher happens to return takes no part in deciding which DEK opens
	// it — which is what lets a row whose custody sits on a retention-class
	// holder project through the same decryptor as a subject-custodied one.
	keyHolderKey, err := vault.KeyHolder(ct)
	if err != nil {
		return failure.Terminal(fmt.Errorf("pipeline: secure column %q: %w", col.Column, err))
	}
	// The ciphertext names its holder; the declaration says which holders this
	// column agreed to render. A ciphertext naming a holder type the lens never
	// declared is refused rather than opened — the lens author, not the write
	// path, decides whether a retained record surfaces here.
	if holderType := vault.KeyHolderType(keyHolderKey); !slices.Contains(col.HolderTypes, holderType) {
		return failure.Terminal(fmt.Errorf(
			"pipeline: secure column %q: ciphertext is held by %s, whose type %q is not among the column's declared holderTypes %v",
			col.Column, keyHolderKey, holderType, col.HolderTypes))
	}

	envelope, err := d.readPiiKeyEnvelope(ctx, keyHolderKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			// Ciphertext exists but its key holder has no piiKey — an invariant
			// violation (step 6.5 mints the piiKey in the same atomic batch as
			// the first sensitive write; a shred leaves a durable placeholder).
			return failure.Terminal(fmt.Errorf(
				"pipeline: secure column %q: key holder %s has ciphertext but no piiKey aspect", col.Column, keyHolderKey))
		}
		// Infra (KV unreachable, …) — classify via the standard path.
		return fmt.Errorf("pipeline: secure column %q: read piiKey for %s: %w", col.Column, keyHolderKey, err)
	}

	if d.calls != nil {
		d.calls.Add(1)
	}
	plaintext, err := d.vault.Decrypt(ctx, keyHolderKey, envelope, ct)
	if err != nil {
		if errors.Is(err, vault.ErrKeyShredded) {
			// Shredded → the PII is gone; project null and keep the row.
			row[col.Column] = nil
			return nil
		}
		// ErrDecryptFailed / ErrInvalidEnvelope (tampered ciphertext, wrong
		// holder binding) — permanently unrecoverable for this input.
		return failure.Terminal(fmt.Errorf("pipeline: secure column %q: decrypt under %s: %w", col.Column, keyHolderKey, err))
	}

	var value map[string]any
	if err := json.Unmarshal(plaintext, &value); err != nil {
		return failure.Terminal(fmt.Errorf("pipeline: secure column %q: decrypted plaintext is not a JSON object: %w", col.Column, err))
	}
	if col.Field != "" {
		fv, present := value[col.Field]
		if !present {
			// The plaintext decrypted fine but lacks the declared field — a
			// spec/DDL mismatch. Terminal, so Apply redacts the column: the
			// resulting null IS indistinguishable from a legitimate shred, which
			// this site once chose Terminal to avoid, and the alarm Apply raises
			// is what carries that distinction instead. Silence about a defect is
			// the cost; standing plaintext was the alternative.
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

// readPiiKeyEnvelope point-reads and parses a key holder's <holder>.piiKey —
// the same aspect shape the Processor's commit path writes (the aspect
// envelope's `data` field carries the vault.Envelope). A soft-deleted piiKey
// aspect is treated as absent (the engine treats soft-deleted aspects as
// absent too — the two layers must agree, and a "deleted" key must never open
// ciphertext). A piiKey that exists but cannot be parsed is permanently
// unusable, so the error is Terminal — a Transient classification would
// Nak-loop the triggering event forever.
func (d *SecureDecryptor) readPiiKeyEnvelope(ctx context.Context, keyHolderKey string) (vault.Envelope, error) {
	entry, err := d.coreKV.Get(ctx, keyHolderKey+".piiKey")
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
		return vault.Envelope{}, failure.Terminal(fmt.Errorf("parse piiKey %s.piiKey: %w", keyHolderKey, err))
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
