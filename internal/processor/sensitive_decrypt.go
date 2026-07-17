package processor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
)

// sensitiveReadTracker records whether this operation's execution decrypted
// any sensitive aspect as PLAINTEXT (the egress=false disposition below) —
// consulted by step 6's emission guard (design sensitive-param-egress §3.6):
// an op that emits an `external.*`-domain event AND decrypted sensitive
// plaintext this execution is rejected, because sensitive data may reach an
// external event only as an `egressReads` ref. Shared by pointer across step
// 4's contextHint.reads/optionalReads/egressReads decrypt calls and the lazy
// kv.Read() seam (connKVReader) for one execution.
type sensitiveReadTracker struct {
	plaintextRead bool
}

// decryptSensitiveDoc applies the Contract #3 §3.10 read-side disposition
// when doc's class resolves to a sensitive DDL: shared by step 4's
// contextHint.reads/optionalReads/egressReads hydration and the lazy
// kv.Read() seam (connKVReader). ddls nil, or doc's class not found / not
// sensitive, leaves doc untouched: the aspect's ciphertext shape passes
// through as opaque data.
//
// egress selects the disposition for a sensitive doc: false decrypts to
// plaintext (v nil leaves the ciphertext untouched — the safe default for a
// pipeline that never wired a Vault, most test harnesses that do not
// exercise PII) and marks tracker plaintext-read; true never decrypts —
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
	ref, ok := ddls.Lookup(doc.Class)
	if !ok || !ref.Sensitive {
		return nil
	}
	vertexKey, vertexType, _, _, ok := substrate.ParseAspectKey(doc.Key)
	if !ok || vertexType != "identity" {
		// A malformed or non-identity-anchored sensitive aspect should never
		// have committed (step 6 rejects it at write time) — decrypt-on-read
		// is not the place to re-litigate that; leave the document as-is.
		return nil
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
	if v == nil {
		return nil
	}
	envelope, err := readPiiKeyEnvelope(ctx, conn, bucket, vertexKey)
	if err != nil {
		return fmt.Errorf("read piiKey for %s: %w", doc.Key, err)
	}
	ct, err := ciphertextFromData(doc.Data)
	if err != nil {
		return fmt.Errorf("parse ciphertext for %s: %w", doc.Key, err)
	}
	plaintext, err := v.Decrypt(ctx, vertexKey, envelope, ct)
	if err != nil {
		return fmt.Errorf("decrypt %s: %w", doc.Key, err)
	}
	var value map[string]interface{}
	if err := json.Unmarshal(plaintext, &value); err != nil {
		return fmt.Errorf("unmarshal decrypted %s: %w", doc.Key, err)
	}
	doc.Data = value
	if tracker != nil {
		tracker.plaintextRead = true
	}
	return nil
}

// readPiiKeyEnvelope reads and parses vertexKey's piiKey aspect. Internal
// Processor bookkeeping — never declared in a script's contextHint.reads;
// Starlark never sees the envelope, only the decrypted plaintext (design
// §2.2's "Starlark stays pure" guarantee).
func readPiiKeyEnvelope(ctx context.Context, conn *substrate.Conn, bucket, vertexKey string) (vault.Envelope, error) {
	entry, err := conn.KVGet(ctx, bucket, vertexKey+".piiKey")
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
