package processor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// sensitiveReadTracker records whether this operation's SCRIPT consumed any
// sensitive aspect's decrypted PLAINTEXT (the egress=false disposition below) —
// consulted by step 6's emission guard (design sensitive-param-egress §3.6):
// an op that emits an `external.*`-domain event AND consumed sensitive
// plaintext this execution is rejected, because sensitive data may reach an
// external event only as an `egressReads` ref. Shared by pointer across step
// 4's contextHint.reads/optionalReads/egressReads decrypt calls, the `state`
// mapping, and the lazy kv.Read() seam (connKVReader) for one execution.
//
// Decryption alone does not set plaintextRead: hydration decrypts every present
// declared sensitive key, so keying the flag on the decrypt made a *surplus*
// declared read split an external-egress op's outcome on whether that key
// exists — an existence oracle over sensitive-classed aspects, since the script
// never names the key (design sensitive-read-tracker-consumption §1). Instead
// the decrypt records the key in plaintextKeys, and the seams the script itself
// reaches through consume it.
type sensitiveReadTracker struct {
	plaintextRead bool
	plaintextKeys map[string]struct{}
}

// markPlaintext records that key's document body is readable by the script —
// decrypted here, or never encrypted at rest in the first place — pending
// consumption.
func (t *sensitiveReadTracker) markPlaintext(key string) {
	if t == nil {
		return
	}
	if t.plaintextKeys == nil {
		t.plaintextKeys = make(map[string]struct{})
	}
	t.plaintextKeys[key] = struct{}{}
}

// consume records that the script took the document under key. Sets
// plaintextRead only when that document carries readable sensitive data, so a
// script reading a non-sensitive working set never trips the egress guard.
func (t *sensitiveReadTracker) consume(key string) {
	if t == nil {
		return
	}
	if _, ok := t.plaintextKeys[key]; ok {
		t.plaintextRead = true
	}
}

// consumeAll records an exposure that hands the script every hydrated document
// without naming a key — `state.items()` / `state.values()`, and `String()`,
// through which `str`/`repr`/`%`/`.format`/`+` render every document's data
// (design §2.1). Flips only when at least one recorded plaintext key exists.
func (t *sensitiveReadTracker) consumeAll() {
	if t == nil {
		return
	}
	if len(t.plaintextKeys) > 0 {
		t.plaintextRead = true
	}
}

// deferredMissTracker records the first declared-but-absent required read
// (ScriptContext.RequiredAbsent) this execution actually touched, so the runner
// can raise the HydrationMiss step 4 deferred. Shared by pointer across the
// `kv` builtins and the `state` mapping for one execution.
//
// One-shot by design: the first touch aborts the script, so a later key can
// only be reported by a mapping that swallowed the error, and reporting the
// key the operation demonstrably reached first is the honest diagnostic.
type deferredMissTracker struct {
	key string
}

// fault records key as the deferred miss, keeping the first one seen.
func (t *deferredMissTracker) fault(key string) {
	if t == nil || t.key != "" {
		return
	}
	t.key = key
}

// missed returns the recorded key, or "" when this execution touched none.
func (t *deferredMissTracker) missed() string {
	if t == nil {
		return ""
	}
	return t.key
}

// decryptSensitiveDoc applies the Contract #3 §3.10 read-side disposition
// when doc's class resolves to a sensitive DDL: shared by step 4's
// contextHint.reads/optionalReads/egressReads hydration and the lazy
// kv.Read() seam (connKVReader). ddls nil, or doc's class not found / not
// sensitive, leaves doc untouched: the aspect's ciphertext shape passes
// through as opaque data.
//
// Resolution reuses the same ddlResolver step 6 and step 6.5 share: exact
// class→DDL lookup first, then — on a miss — the bounded instanceOf-chain
// walk to the type authority. A document read here is already committed, so
// only the walk's live layers are meaningful (there is no in-flight batch or
// hydrated working set to consult): the instanceOf link itself resolves via
// the on-demand linkReader, and a chain terminal that is a business vertex
// (rather than a DDL meta-vertex) resolves its class via the resolver's
// classReader — a plain, non-decrypting class-only read (a vertex's class is
// never encrypted; only its data is). This deliberately does NOT reuse the
// script-facing, decrypt-aware ScriptKVReader/connKVReader for that lookup:
// doing so would call back into decryptSensitiveDoc for the chain terminal,
// which — now that this function itself performs the chain walk — could
// recurse without bound across a mutual instanceOf cycle (each nested call
// gets its own fresh hop bound, with no depth guard shared across calls). conn
// nil (a test affordance) leaves every live layer disabled, same as the
// exact-match-only behavior before this resolver existed.
//
// egress selects the disposition for a sensitive doc: false decrypts to
// plaintext (v nil leaves the ciphertext untouched — the safe default for a
// pipeline that never wired a Vault, most test harnesses that do not
// exercise PII) and records the key on tracker as carrying plaintext, which
// the script's own read seams later consume; true never decrypts —
// instead doc.Data becomes a `$sensitiveRef` marker (the aspect's at-rest
// ciphertext verbatim, keyed by its own aspect key, Processor-authenticated
// with a MAC when a Vault is wired — design sensitive-ref-mac-provenance
// §3.2) that the bridge unwraps at the external-egress boundary (design
// sensitive-param-egress §3.2). A non-sensitive doc is unaffected by egress
// either way.
//
// requestID is the minting operation's request ID, bound into the egress
// marker's MAC (splice-resistance, §3.2); ignored for the non-egress
// disposition.
func decryptSensitiveDoc(ctx context.Context, conn *substrate.Conn, bucket string, ddls *DDLCache, v vault.Vault, doc *VertexDoc, egress bool, tracker *sensitiveReadTracker, requestID string) error {
	if ddls == nil || doc == nil {
		return nil
	}
	resolver := &ddlResolver{DDLs: ddls}
	if conn != nil {
		resolver.linkReader = &connInstanceOfReader{conn: conn, coreBucket: bucket}
		resolver.classReader = &connVertexClassReader{conn: conn, coreBucket: bucket}
	}
	ref, ok := resolver.resolveGoverningDDL(ctx, doc.Class, doc.Key, substrate.ClassifyKey(doc.Key), ScriptResult{}, HydratedState{})
	if !ok || !ref.Sensitive {
		return nil
	}
	if doc.IsDeleted {
		// A soft-deleted sensitive aspect must never yield plaintext, and must
		// never be handed onward as an egress ref the bridge can open — the
		// same rule the Refractor enforces on a soft-deleted piiKey
		// (internal/refractor/pipeline/secure.go). The tombstone retains the
		// aspect's ciphertext (step 8 preserves the prior document), so the
		// deletion flag is the only thing standing between a dead aspect and a
		// live decrypt: fail closed here rather than relying on an erased body.
		return fmt.Errorf("read deleted sensitive aspect %s", doc.Key)
	}
	if egress {
		// Ref-marker authoring needs only the DDL lookup + the ciphertext
		// already in hand — no live Vault backend required (design §3.2). The
		// key envelope is deliberately NOT carried: a consumer must always
		// resolve it live at decrypt time (the restart-/replay-proof shred
		// gate), never from a frozen copy.
		marker := map[string]interface{}{
			"ref":        doc.Key,
			"ciphertext": doc.Data,
		}
		if v != nil {
			// MAC over the decoded ciphertext bytes (ciphertextFromData), never
			// doc.Data's base64-string JSON shape directly — the responder
			// recomputes over the same decoded bytes it receives in
			// DecryptRefRequest.Ciphertext, so mint and verify must agree
			// byte-for-byte (the canonicalization trap, design §3.2).
			ct, err := ciphertextFromData(doc.Data)
			if err != nil {
				return fmt.Errorf("parse ciphertext for ref-mac %s: %w", doc.Key, err)
			}
			if err := refusableEgressHolder(ct); err != nil {
				return fmt.Errorf("author egress ref for %s: %w", doc.Key, err)
			}
			mac, err := v.MAC(ctx, vault.RefMACPurpose, vault.RefMACInput(doc.Key, requestID, ct))
			if err != nil {
				// A live Vault that fails to mint a MAC must never author an
				// unauthenticated ref — fail closed (design §3.2, the D1
				// direction), not silently degrade to an unmarked marker.
				return fmt.Errorf("mint ref-mac for %s: %w", doc.Key, err)
			}
			marker["mac"] = base64.StdEncoding.EncodeToString(mac)
		}
		doc.Data = map[string]interface{}{"$sensitiveRef": marker}
		return nil
	}
	// Sensitive-classed and NOT the egress ref disposition: from here the body
	// reaches the script readable whatever happens next, so record it before the
	// Vault branch rather than after a successful decrypt. With no Vault wired
	// step 6.5 never encrypted on the way in (commit_path.go), so the aspect sits
	// in Core KV as plaintext and returning here without recording left step 6's
	// egress guard vacuous for exactly the deployment that has no crypto boundary
	// at all. Recording on "sensitive-classed and readable" instead of "I
	// decrypted it" is the fail-closed predicate.
	tracker.markPlaintext(doc.Key)
	if v == nil {
		return nil
	}
	// The ciphertext is parsed before anything else is resolved, because it is
	// what names its own key holder (vault.KeyHolder). Nothing about where the
	// aspect is stored takes part in that answer.
	ct, err := ciphertextFromData(doc.Data)
	if err != nil {
		return fmt.Errorf("parse ciphertext for %s: %w", doc.Key, err)
	}
	keyHolderKey, err := vault.KeyHolder(ct)
	if err != nil {
		return fmt.Errorf("resolve key holder for %s: %w", doc.Key, err)
	}
	envelope, err := readPiiKeyEnvelope(ctx, conn, bucket, keyHolderKey)
	if err != nil {
		return fmt.Errorf("read piiKey for %s: %w", doc.Key, err)
	}
	plaintext, err := v.Decrypt(ctx, keyHolderKey, envelope, ct)
	if err != nil {
		return fmt.Errorf("decrypt %s: %w", doc.Key, err)
	}
	var value map[string]interface{}
	if err := json.Unmarshal(plaintext, &value); err != nil {
		return fmt.Errorf("unmarshal decrypted %s: %w", doc.Key, err)
	}
	doc.Data = value
	return nil
}

// refusableEgressHolder refuses an egress ref whose key holder the bridge
// cannot resolve an envelope for.
//
// The bridge unwraps a $sensitiveRef by reading the holder's envelope live
// from the piiKeyEnvelope lens (packages/privacy-base), and that lens
// enumerates identity holders alone. A retention-class-custodied record handed
// to the bridge would therefore fail as an envelope that never projects —
// indistinguishable from a lens that is merely lagging, and retried until the
// unwrap budget is spent. Refusing it here, where the operation is authored,
// turns that into one typed error naming the holder type, at the point a
// script author can act on it.
func refusableEgressHolder(ct vault.Ciphertext) error {
	keyHolderKey, err := vault.KeyHolder(ct)
	if err != nil {
		return err
	}
	if holderType := vault.KeyHolderType(keyHolderKey); holderType != "identity" {
		return fmt.Errorf(
			"key holder %s is a %q holder, and only an identity holder's envelope is reachable at the external-egress boundary",
			keyHolderKey, holderType)
	}
	return nil
}

// readPiiKeyEnvelope reads and parses keyHolderKey's piiKey aspect. Internal
// Processor bookkeeping — never declared in a script's contextHint.reads;
// Starlark never sees the envelope, only the decrypted plaintext (design
// §2.2's "Starlark stays pure" guarantee).
func readPiiKeyEnvelope(ctx context.Context, conn *substrate.Conn, bucket, keyHolderKey string) (vault.Envelope, error) {
	entry, err := conn.KVGet(ctx, bucket, keyHolderKey+".piiKey")
	if err != nil {
		return vault.Envelope{}, err
	}
	var doc struct {
		Data vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return vault.Envelope{}, err
	}
	return doc.Data, nil
}

// ciphertextFromData re-parses an aspect's generically-decoded Data map back
// into a vault.Ciphertext with proper []byte fields. The first json.Unmarshal
// (into VertexDoc.Data map[string]interface{}) decodes CT/Nonce as base64
// strings rather than bytes; round-tripping through JSON a second time, this
// time into the typed struct, is the simplest way to recover the []byte
// shape without threading raw bytes through VertexDoc's generic map.
func ciphertextFromData(data map[string]interface{}) (vault.Ciphertext, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return vault.Ciphertext{}, err
	}
	var ct vault.Ciphertext
	if err := json.Unmarshal(raw, &ct); err != nil {
		return vault.Ciphertext{}, err
	}
	return ct, nil
}
