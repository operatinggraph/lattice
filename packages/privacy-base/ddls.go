package privacybase

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations:
//   - `piiKey` (meta.ddl.aspectType, NOT sensitive) — the wrapped-DEK
//     envelope reference stored as vtx.identity.<NanoID>.piiKey
//     (design §2.1). PermittedCommands admits ShredIdentityKey (design
//     §2.2/§9 Fire 3) — the one OPERATION allowed to write piiKey directly
//     (to flip shredded=true); step-6's resolveGoverningDDL keys on the
//     MUTATION's class, so this only gates that write, and does NOT make
//     piiKeyDDLScript itself dispatchable (ClassForCommand indexes vertexType
//     DDLs only, mirroring freshnessExpiry/MarkExpired). The Processor's
//     commit-path step 6.5 still mints a NEW piiKey internally on an
//     identity's first sensitive-aspect write — an engine write, not a
//     dispatched op, so it bypasses this gate entirely as it always has.
//     This registers the class for DDL-cache/tooling introspection; it does
//     NOT guard against a script directly emitting a `.piiKey` mutation in
//     its OWN ScriptResult — no aspect-type DDL in this codebase blocks that
//     (the same trust model already governing every other reserved aspect:
//     package scripts are reviewed code, not untrusted input).
//   - `shredIdentityKey` (meta.ddl.vertexType) — the ShredIdentityKey op DDL
//     (design §2.2/§2.4, Fire 3). See shred_identity_key.go.
//   - `privacy.keyShredded` (meta.ddl.eventType) — the registered event-type
//     DDL for the op's emitted event (Contract #3 §3.4). See
//     shred_identity_key.go.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		{
			CanonicalName:     "piiKey",
			Class:             "meta.ddl.aspectType",
			Sensitive:         false,
			PermittedCommands: []string{"ShredIdentityKey"},
			Description: "Per-identity PII key-custody envelope (vault-crypto-shredding-design.md §2.1, " +
				"Contract #3 §3.10): stored as vtx.identity.<NanoID>.piiKey, holding only the wrapped " +
				"(ciphertext) data-encryption key — never plaintext key material. Minted lazily by the " +
				"Processor's commit-path step 6.5 on an identity's first sensitive-aspect write (an internal " +
				"engine write, not a dispatched op — bypasses permittedCommands), and read internally by " +
				"step 4 / kv.Read decrypt-on-read. ShredIdentityKey (design §2.2/§9 Fire 3) is the sole " +
				"OPERATION permitted to write it directly, to flip shredded=true.",
			Script: piiKeyDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"wrappedDEK":{"type":"string","description":"Base64 ciphertext of the per-identity DEK, wrapped under the Vault backend's master key."},` +
				`"keyId":{"type":"string","description":"Identity key the DEK was minted for."},` +
				`"kekVersion":{"type":"string","description":"Label of the KEK that wrapped this DEK, for future rotation detection."},` +
				`"alg":{"type":"string","description":"AEAD algorithm identifier (e.g. AES-256-GCM)."},` +
				`"createdAt":{"type":"string","description":"Envelope creation timestamp."},` +
				`"shredded":{"type":"boolean","description":"True once ShredIdentityKey has revoked this envelope."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"wrappedDEK": "Wrapped (ciphertext) data-encryption key — openable only by the Vault backend's master key, never plaintext.",
				"keyId":      "The identity key this DEK was minted for (AEAD-bound).",
				"kekVersion": "KEK label the wrap used, for detecting a future KEK rotation.",
				"alg":        "AEAD algorithm identifier.",
				"createdAt":  "Envelope creation timestamp.",
				"shredded":   "True once the identity's key has been irreversibly shredded.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "piiKey envelope",
					Payload:         map[string]any{"wrappedDEK": "<base64-ciphertext>", "keyId": "vtx.identity.<NanoID>", "kekVersion": "v1", "alg": "AES-256-GCM", "createdAt": "2026-07-02T00:00:00Z", "shredded": false},
					ExpectedOutcome: "Stored as vtx.identity.<NanoID>.piiKey by the Processor's step-6.5 encrypt hook on the identity's first sensitive-aspect write. Never written by a script.",
				},
			},
		},
		ShredIdentityKeyDDL(),
		KeyShreddedEventDDL(),
	}
}

// piiKeyDDLScript is the declaration-only Starlark for the piiKey
// aspect-type DDL. Mirrors identity-domain's sensitiveAspectDDLScript: an
// aspect-type DDL declares shape and anchoring, not an operation handler.
const piiKeyDDLScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`
