package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// The privacy-base envelope lenses' read models
// (packages/privacy-base.PiiKeyEnvelopeBucket and RetentionKeyEnvelopeBucket)
// — kept as literals, like Config's other bucket defaults, so internal/bridge
// does not import packages/privacy-base (P5: the bridge reads these buckets as
// an ordinary lens consumer, exactly the pattern
// cmd/loftspace-app/objects_crypto.go ships). envelope_bucket_pin_test.go pins
// each literal equal to the package's own exported constant.
const (
	identityEnvelopeBucket       = "privacy-pii-key-envelopes"
	retentionClassEnvelopeBucket = "privacy-retention-key-envelopes"
)

// envelopeBucketByHolderKind is envelopeBucketFor's table, written as a map
// rather than a switch so a test can enumerate the kinds it serves and pin that
// set equal to vault.KeyHolderKinds() in both directions.
var envelopeBucketByHolderKind = map[string]string{
	"identity":       identityEnvelopeBucket,
	"retentionclass": retentionClassEnvelopeBucket,
}

// envelopeBucketFor maps a key holder's vertex type to the lens read model that
// projects that holder kind's live envelope. Every kind in vault.KeyHolderKinds
// has an entry (pinned by test); a kind with none is refused permanently,
// naming the kind.
func envelopeBucketFor(holderType string) (bucket string, ok bool) {
	bucket, ok = envelopeBucketByHolderKind[holderType]
	return bucket, ok
}

// maxNestedMarkerScanDepth bounds the nested-$sensitiveRef scan
// (nestedMarkerScan) so a deeply shaped param value cannot turn one unwrap into
// unbounded work. Eight levels is far past any shape a step emits (the deepest
// shipped params object nests two), and a value that does nest deeper is
// refused rather than passed: absence of a marker is what the boundary needs to
// establish, and a truncated walk establishes nothing.
const maxNestedMarkerScanDepth = 8

// vaultDecryptTimeout bounds the bridge's own wait on the lattice.vault.decrypt
// RPC, independent of the Vault responder's internal handlerTimeout — a wedged
// or unreachable Vault must not hang the dispatch handler indefinitely.
const vaultDecryptTimeout = 5 * time.Second

// maxEgressUnwrapAttempts bounds the transient-failure retry budget for an
// egress unwrap (an absent-but-not-yet-projected envelope row, or a Vault RPC
// error/timeout) before it escalates to a permanent failure — mirroring
// internal/refractor/keyshredded's maxNotRegisteredDeliveries pattern: naking
// forever on a condition that will never resolve just spams redelivery with no
// path out, so a bounded budget converges the pattern instead (design
// sensitive-param-egress §3.5).
const maxEgressUnwrapAttempts = 5

// egressFailureClass distinguishes a permanent (never retry) egress-unwrap
// failure from a transient (redeliver) one (design §3.5).
type egressFailureClass int

const (
	egressPermanent egressFailureClass = iota
	egressTransient
)

// egressFailure is the classified error unwrapEgressParams returns.
type egressFailure struct {
	err   error
	class egressFailureClass
}

func (f *egressFailure) Error() string { return f.err.Error() }

func permanentEgressFailure(err error) *egressFailure {
	return &egressFailure{err: err, class: egressPermanent}
}

func transientEgressFailure(err error) *egressFailure {
	return &egressFailure{err: err, class: egressTransient}
}

// sensitiveRefWrapper detects the `{"$sensitiveRef": {...}}` marker shape a
// resolved param value carries for a sensitive templated aspect
// (orchestration-base's resolve_subject_params, design §3.2/§3.3). Any other
// JSON shape (a plain string/number/bool/object/array) fails to unmarshal into
// this or leaves SensitiveRef nil, and is treated as "not a marker" — passed
// through untouched, exactly as coerceParams already tolerates.
type sensitiveRefWrapper struct {
	SensitiveRef json.RawMessage `json:"$sensitiveRef"`
}

// sensitiveRefMarker is the inner `$sensitiveRef` shape: the sensitive
// aspect's canonical key, its at-rest ciphertext, the plaintext field name
// the resolver appended for the bridge's post-decrypt extraction, and the
// Processor-minted MAC binding {ref, requestId, ciphertext} (design
// sensitive-ref-mac-provenance-design.md §3.2) that proves provenance.
type sensitiveRefMarker struct {
	Ref        string           `json:"ref"`
	Ciphertext vault.Ciphertext `json:"ciphertext"`
	Field      string           `json:"field"`
	MAC        []byte           `json:"mac"`
}

// detectSensitiveRef reports whether raw carries a `$sensitiveRef` marker. ok
// is false for any value that is not a JSON object with that key present —
// the ordinary "ignore me" case for every literal and plain-field param value.
// When the key IS present, its inner shape is unmarshaled into a
// sensitiveRefMarker; a malformed inner shape is a genuine authoring error,
// surfaced via ferr rather than silently ignored. A marker carrying no `mac`
// — pre-MAC-era or fabricated by a script that assembles the shape without
// ever going through Processor hydration — fails closed HERE, before any
// Vault RPC (design §3.4: "a pre-MAC marker or a fabricated one never leaves
// the bridge").
func detectSensitiveRef(raw json.RawMessage) (marker sensitiveRefMarker, ok bool, ferr *egressFailure) {
	var w sensitiveRefWrapper
	if err := json.Unmarshal(raw, &w); err != nil || w.SensitiveRef == nil {
		return sensitiveRefMarker{}, false, nil
	}
	if err := json.Unmarshal(w.SensitiveRef, &marker); err != nil {
		return sensitiveRefMarker{}, false, permanentEgressFailure(
			fmt.Errorf("bridge: malformed $sensitiveRef marker: %w", err))
	}
	if len(marker.MAC) == 0 {
		return sensitiveRefMarker{}, false, permanentEgressFailure(
			fmt.Errorf("bridge: $sensitiveRef marker for %q carries no mac", marker.Ref))
	}
	return marker, true, nil
}

// sensitiveRefMarkerKey is the reserved param-value key a sensitive-ref marker
// is wrapped in — matched literally by the nested-marker scan, which looks for
// the key alone rather than a well-formed marker under it.
const sensitiveRefMarkerKey = "$sensitiveRef"

// nestedMarkerScan walks one top-level param value looking for a
// `$sensitiveRef` key BELOW that value's own top level. depth is the caller's
// level: 1 is a direct child of the param — a member of a child object OR an
// element of a child array, both of which are places the unwrap does not
// substitute — 2 a child of that, and so on. A key at depth 0 (the value
// itself) is the shape the unwrap serves and is never reported, so callers pass
// depth 0 for a value detectSensitiveRef has already judged.
//
// truncated reports that the walk stopped at maxNestedMarkerScanDepth with a
// container left unopened: the answer is then "unknown", not "no marker", and
// the caller refuses on it. Returning found and truncated separately is what
// keeps the depth bound from becoming the way to smuggle a marker past the
// scan.
func nestedMarkerScan(raw json.RawMessage, depth int) (markerDepth int, found, truncated bool) {
	if depth > maxNestedMarkerScanDepth {
		return 0, false, jsonContainer(raw)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		for k, v := range obj {
			if k == sensitiveRefMarkerKey && depth > 0 {
				return depth, true, false
			}
			d, hit, cut := nestedMarkerScan(v, depth+1)
			if hit {
				return d, true, false
			}
			truncated = truncated || cut
		}
		return 0, false, truncated
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		for _, v := range arr {
			d, hit, cut := nestedMarkerScan(v, depth+1)
			if hit {
				return d, true, false
			}
			truncated = truncated || cut
		}
	}
	return 0, false, truncated
}

// jsonContainer reports whether raw is a JSON object or array — the two shapes
// a marker key can hide inside, and so the shapes an unfinished walk must
// account for. A scalar (or null) hides nothing.
func jsonContainer(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj != nil
	}
	var arr []json.RawMessage
	return json.Unmarshal(raw, &arr) == nil
}

// unwrapEgressParams walks raw (the external event's params object) and
// replaces every `$sensitiveRef` marker with the vendor-ready plaintext field
// it names, fetched via the bridge's egress-unwrap boundary (design §3.5).
// Non-marker values pass through byte-identical. Called at the dispatch.go
// chokepoint BEFORE coerceParams, so a substituted plaintext string flows
// through the ordinary string-param coercion unchanged. RawParams (the
// caller's Request.RawParams) is deliberately NOT derived from this output —
// it stays the original event params, still carrying refs (design §8).
//
// A marker is served at the TOP LEVEL of params only, and a marker found
// deeper is a permanent failure rather than a pass-through: nothing
// substitutes it, so it would otherwise ride out to the vendor as ciphertext +
// a MAC inside RawParams, and a pattern that templates a sensitive aspect into
// a nested value would fail silently in exactly that shape. Refusing makes the
// silent shape one typed terminal outcome, naming the param and the depth.
//
// numDelivered (msg.NumDelivered) buys the transient-failure retry budget
// (maxEgressUnwrapAttempts): an envelope-lens row not yet projected for a
// just-created identity, or a Vault RPC hiccup, is retried a bounded number of
// times before escalating to a permanent failure — never an unbounded Nak
// loop (design §3.5). requestID is the minting op's requestId, read from the
// external event's top-level envelope (never caller-chosen at unwrap time) —
// it splice-binds every marker's MAC verification to its minting execution
// (design §3.2).
func (e *Engine) unwrapEgressParams(ctx context.Context, raw json.RawMessage, requestID string, numDelivered uint64) (json.RawMessage, *egressFailure) {
	if len(raw) == 0 {
		return raw, nil
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		// params is not a JSON object (e.g. a bare array/scalar) — no marker to
		// find; pass through unchanged, exactly as coerceParams already does.
		return raw, nil
	}

	changed := false
	for k, v := range generic {
		marker, ok, ferr := detectSensitiveRef(v)
		if ferr != nil {
			return nil, ferr
		}
		if !ok {
			depth, nested, truncated := nestedMarkerScan(v, 0)
			if nested {
				return nil, permanentEgressFailure(fmt.Errorf(
					"bridge: $sensitiveRef marker under param %q at a depth the unwrap does not serve (depth %d) — a sensitive-ref is served only at the top level of params",
					k, depth))
			}
			if truncated {
				return nil, permanentEgressFailure(fmt.Errorf(
					"bridge: param %q nests past the %d levels scanned for a $sensitiveRef marker, so no marker can be ruled out — a sensitive-ref is served only at the top level of params",
					k, maxNestedMarkerScanDepth))
			}
			continue
		}
		plaintext, ferr := e.resolveSensitiveRef(ctx, marker, requestID, numDelivered)
		if ferr != nil {
			return nil, ferr
		}
		substituted, err := json.Marshal(plaintext)
		if err != nil {
			return nil, permanentEgressFailure(fmt.Errorf("bridge: marshal unwrapped field %q: %w", marker.Field, err))
		}
		generic[k] = substituted
		changed = true
	}
	if !changed {
		return raw, nil
	}
	out, err := json.Marshal(generic)
	if err != nil {
		return nil, permanentEgressFailure(fmt.Errorf("bridge: remarshal unwrapped params: %w", err))
	}
	return out, nil
}

// resolveSensitiveRef unwraps one sensitive-ref marker to its plaintext field
// value: resolve the key holder from the ciphertext's own keyId, fetch that
// holder's LIVE key envelope from the lens that projects its holder kind's
// envelopes (never a stored/carried copy — the restart-/replay-proof shred
// gate, design §3.2/§3.5), call the ref-verified Vault decrypt RPC (mandatory
// MAC verification), and extract the requested field. Every failure is
// classified permanent (do not retry) or transient (redeliver, bounded).
//
// The holder kinds served are exactly the ones envelopeBucketFor has a lens
// for; a holder of any other kind is refused permanently, naming the kind. The
// Processor refuses to author such a ref in the first place, so this is the
// second of two gates on the same rule rather than the only one; it exists
// because the bridge is where an unserveable holder would otherwise decay into
// an envelope that simply never projects.
func (e *Engine) resolveSensitiveRef(ctx context.Context, marker sensitiveRefMarker, requestID string, numDelivered uint64) (string, *egressFailure) {
	if _, _, _, _, ok := substrate.ParseAspectKey(marker.Ref); !ok {
		return "", permanentEgressFailure(
			fmt.Errorf("bridge: $sensitiveRef.ref %q is not a well-formed aspect key", marker.Ref))
	}
	keyHolderKey, err := vault.KeyHolder(marker.Ciphertext)
	if err != nil {
		return "", permanentEgressFailure(
			fmt.Errorf("bridge: $sensitiveRef for %q: %w", marker.Ref, err))
	}
	holderType := vault.KeyHolderType(keyHolderKey)
	bucket, ok := envelopeBucketFor(holderType)
	if !ok {
		return "", permanentEgressFailure(fmt.Errorf(
			"bridge: $sensitiveRef for %q is held by %s, a %q holder — no envelope source for that holder kind",
			marker.Ref, keyHolderKey, holderType))
	}
	if marker.Field == "" {
		return "", permanentEgressFailure(fmt.Errorf("bridge: $sensitiveRef for %q carries no field", marker.Ref))
	}

	envelope, err := e.fetchLiveEnvelope(ctx, bucket, keyHolderKey)
	if err != nil {
		if numDelivered < maxEgressUnwrapAttempts {
			verb := "read"
			if errors.Is(err, substrate.ErrKeyNotFound) {
				verb = "not yet projected"
			}
			return "", transientEgressFailure(
				fmt.Errorf("bridge: envelope for %s %s (attempt %d): %w", keyHolderKey, verb, numDelivered, err))
		}
		// Every fetchLiveEnvelope failure — absent row, unparseable value, a
		// persistent bucket error — must escalate past the retry budget, not
		// only the ErrKeyNotFound arm: an unconditional transient return here
		// would Nak forever on a bad row, parking the pattern unbounded (FR29).
		return "", permanentEgressFailure(
			fmt.Errorf("bridge: envelope for %s unusable after %d attempts: %w", keyHolderKey, numDelivered, err))
	}

	plaintext, verr := e.vaultDecryptRef(ctx, marker.Ref, requestID, envelope, marker.Ciphertext, marker.MAC)
	if verr != nil {
		if errors.Is(verr, vault.ErrRefUnverified) {
			// A bad MAC cannot become good on redelivery — the mint-time
			// binding to this exact (ref, requestId, ciphertext) tuple is
			// permanent, so retrying never helps (design §3.4).
			return "", permanentEgressFailure(fmt.Errorf("bridge: %s ref unverified: %w", marker.Ref, verr))
		}
		if errors.Is(verr, vault.ErrKeyShredded) {
			return "", permanentEgressFailure(fmt.Errorf("bridge: %s is shredded: %w", keyHolderKey, verr))
		}
		if numDelivered < maxEgressUnwrapAttempts {
			return "", transientEgressFailure(
				fmt.Errorf("bridge: vault decrypt for %s (attempt %d): %w", keyHolderKey, numDelivered, verr))
		}
		return "", permanentEgressFailure(
			fmt.Errorf("bridge: vault decrypt for %s failed after %d attempts: %w", keyHolderKey, numDelivered, verr))
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(plaintext, &fields); err != nil {
		return "", permanentEgressFailure(fmt.Errorf("bridge: unmarshal decrypted %s: %w", keyHolderKey, err))
	}
	fv, ok := fields[marker.Field]
	if !ok {
		return "", permanentEgressFailure(fmt.Errorf("bridge: decrypted %s has no field %q", keyHolderKey, marker.Field))
	}
	var s string
	if err := json.Unmarshal(fv, &s); err != nil {
		return "", permanentEgressFailure(fmt.Errorf("bridge: decrypted %s field %q is not a scalar string", keyHolderKey, marker.Field))
	}
	if s == "" {
		return "", permanentEgressFailure(fmt.Errorf("bridge: decrypted %s field %q is empty", keyHolderKey, marker.Field))
	}
	return s, nil
}

// fetchLiveEnvelope reads keyHolderKey's wrapped-DEK Envelope out of bucket —
// the read model of whichever privacy-base envelope lens projects that holder
// kind (piiKeyEnvelope for an identity, retentionClassKeyEnvelope for a
// retention class), chosen by the caller via envelopeBucketFor. Both lenses
// project the same six Envelope columns, so one parse serves either. This is
// the P5-compliant read the bridge uses in place of a Core-KV read (P2: the
// bridge reads no Core KV; its two transport surfaces are these lens-bucket
// reads and the vault decrypt RPC). Always resolved fresh for this call —
// never cached or carried across calls — so a shred that lands between op
// commit and egress is observed (design §3.2/§3.5's live-envelope rule).
func (e *Engine) fetchLiveEnvelope(ctx context.Context, bucket, keyHolderKey string) (vault.Envelope, error) {
	entry, err := e.conn.KVGet(ctx, bucket, keyHolderKey)
	if err != nil {
		return vault.Envelope{}, err
	}
	var env vault.Envelope
	if err := json.Unmarshal(entry.Value, &env); err != nil {
		return vault.Envelope{}, fmt.Errorf("parse envelope row for %s in %s: %w", keyHolderKey, bucket, err)
	}
	return env, nil
}

// vaultDecryptRef calls the lattice.vault.decryptref RPC (design
// sensitive-ref-mac-provenance-design.md §3.3) — the bridge's sole decrypt
// authority once Fire 2's natsperm grant swap lands: unlike the wholesale
// lattice.vault.decrypt (Loupe's inspector RPC, unchanged), this endpoint
// mandatorily verifies the caller-supplied MAC against {ref, requestId,
// ciphertext} before decrypting, so a fabricated or harvested-and-spliced ref
// is refused rather than honored. A bad or missing MAC returns
// vault.ErrRefUnverified; a shredded identity returns vault.ErrKeyShredded
// (both matched by errors.Is against the wire-carried error string, mirroring
// internal/vault/service.go's own sentinel comparison).
func (e *Engine) vaultDecryptRef(ctx context.Context, ref, requestID string, envelope vault.Envelope, ct vault.Ciphertext, mac []byte) ([]byte, error) {
	reqBody, err := json.Marshal(vault.DecryptRefRequest{Ref: ref, RequestID: requestID, Envelope: envelope, Ciphertext: ct, MAC: mac})
	if err != nil {
		return nil, fmt.Errorf("marshal decryptref request: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, vaultDecryptTimeout)
	defer cancel()
	msg, err := e.conn.NATS().RequestWithContext(rctx, vault.DecryptRefSubject, reqBody)
	if err != nil {
		return nil, fmt.Errorf("vault decryptref RPC: %w", err)
	}
	var resp vault.DecryptRefResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("parse vault decryptref reply: %w", err)
	}
	if resp.Error != "" {
		if resp.Error == vault.ErrKeyShredded.Error() {
			return nil, vault.ErrKeyShredded
		}
		if resp.Error == vault.ErrRefUnverified.Error() {
			return nil, vault.ErrRefUnverified
		}
		return nil, fmt.Errorf("vault decryptref RPC: %s", resp.Error)
	}
	if len(resp.Plaintext) == 0 {
		// The wire contract guarantees exactly one of Plaintext/Error set; an
		// empty reply body unmarshals to both fields zero-valued, which is never
		// a genuine "decrypted to nothing" — treat as a malformed reply.
		return nil, errors.New("vault decryptref RPC: empty reply")
	}
	return resp.Plaintext, nil
}
