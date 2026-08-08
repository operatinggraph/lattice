package privacybase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations:
//   - `piiKey` (meta.ddl.aspectType, NOT sensitive) — the wrapped-DEK
//     envelope reference stored as <holderKey>.piiKey, where the holder is an
//     identity or a retention class (design §2.1;
//     retention-class-key-custody-design.md §3). PermittedCommands admits the
//     shred verb and the finalization verb of EACH holder kind —
//     ShredIdentityKey / RecordShredFinalization and ShredRetentionClassKey /
//     RecordRetentionClassShredFinalization — the only OPERATIONS
//     allowed to write piiKey directly; step-6's resolveGoverningDDL keys on the
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
//   - `shredRetentionClassKey` (meta.ddl.vertexType) — the
//     ShredRetentionClassKey op DDL, the erase-on-EXPIRY sibling that destroys
//     a retention class's DEK rather than a person's
//     (retention-class-key-custody-design.md §4.3). See
//     shred_retention_class_key.go.
//   - `privacy.retentionClassKeyShredded` (meta.ddl.eventType) — the registered
//     event-type DDL for that op's emitted event. See
//     shred_retention_class_key.go.
//   - `erasureRequested` (meta.ddl.aspectType, NOT sensitive) — the
//     erasure-request marker, vtx.identity.<NanoID>.erasureRequested. Its
//     PermittedCommands admits SealIdentityForErasure alone, which refuses a
//     create/update of that CLASS to any other operation. It carries the same
//     caveat piiKey's entry above records for the same mechanism, plus one
//     more: a tombstone carries no document, so its class is empty and no
//     aspect-type DDL can refuse one. Non-removal of this marker is a
//     convention held by review, not a step-6 guarantee — see
//     seal_identity_for_erasure.go.
//   - `sealIdentityForErasure` (meta.ddl.vertexType) — the
//     SealIdentityForErasure op DDL. See seal_identity_for_erasure.go.
//   - `privacy.erasureRequested` (meta.ddl.eventType) — the registered
//     event-type DDL for the seal's emitted event.
//   - `privacy.dedupFootprintSwept` (meta.ddl.eventType) — the registered
//     event-type DDL for the dedup sweep's emitted event, and the only per-pass
//     record of what an erasure removed from that plane. See
//     purge_identity_dedup_footprint.go.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		{
			CanonicalName:     "piiKey",
			Class:             "meta.ddl.aspectType",
			Sensitive:         false,
			PermittedCommands: []string{
				"ShredIdentityKey", "RecordShredFinalization",
				"ShredRetentionClassKey", "RecordRetentionClassShredFinalization",
			},
			Description: "A key holder's wrapped-DEK custody envelope (vault-crypto-shredding-design.md §2.1, " +
				"retention-class-key-custody-design.md §3, Contract #3 §3.10): stored as <holderKey>.piiKey, " +
				"holding only the wrapped (ciphertext) data-encryption key — never plaintext key material. " +
				"A holder is either an identity (custody kind `identity`, policy erase-on-request) or a " +
				"retention class (custody kind `retentionClass`, policy erase-on-expiry); the aspect's shape " +
				"is the same for both, which is what lets one record survive its subject's erasure while " +
				"another dies with it. Minted lazily by the Processor's commit-path step 6.5 on the holder's " +
				"first sensitive-aspect write (an internal engine write, not a dispatched op — bypasses " +
				"permittedCommands), and read internally by step 4 / kv.Read decrypt-on-read. " +
				"ShredIdentityKey / ShredRetentionClassKey flip shredded=true for their respective holder " +
				"kinds; RecordShredFinalization and RecordRetentionClassShredFinalization flip the async " +
				"progress booleans the shredStatus and retentionKeyStatus lenses project. No other operation " +
				"may write it.",
			Script: piiKeyDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"wrappedDEK":{"type":"string","description":"Base64 ciphertext of the per-identity DEK, wrapped under the Vault backend's master key."},` +
				`"keyId":{"type":"string","description":"Key-holder vertex key the DEK was minted for — an identity or a retention class."},` +
				`"kekVersion":{"type":"string","description":"Label of the KEK that wrapped this DEK, for future rotation detection."},` +
				`"alg":{"type":"string","description":"AEAD algorithm identifier (e.g. AES-256-GCM)."},` +
				`"createdAt":{"type":"string","description":"Envelope creation timestamp."},` +
				`"shredded":{"type":"boolean","description":"True once ShredIdentityKey has revoked this envelope."},` +
				`"shreddedAt":{"type":"string","description":"Timestamp of the ShredIdentityKey commit."},` +
				`"vaultKeyDestroyed":{"type":"boolean","description":"True once the privacy-worker's Vault.ShredKey destruction landed (RecordShredFinalization)."},` +
				`"vaultKeyDestroyedAt":{"type":"string","description":"Timestamp of the vaultKeyDestroyed record."},` +
				`"projectionsNullified":{"type":"boolean","description":"True once the Refractor keyshredded listener nullified every configured target row (RecordShredFinalization)."},` +
				`"projectionsNullifiedAt":{"type":"string","description":"Timestamp of the projectionsNullified record."},` +
				`"projectionsRebuilt":{"type":"boolean","description":"Retention-class holders only: true once every secure lens carrying this holder's type has been rebuilt past the destruction (RecordRetentionClassShredFinalization)."},` +
				`"projectionsRebuiltAt":{"type":"string","description":"Timestamp of the projectionsRebuilt record."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"wrappedDEK":             "Wrapped (ciphertext) data-encryption key — openable only by the Vault backend's master key, never plaintext.",
				"keyId":                  "The key-holder vertex key this DEK was minted for, an identity or a retention class (AEAD-bound).",
				"kekVersion":             "KEK label the wrap used, for detecting a future KEK rotation.",
				"alg":                    "AEAD algorithm identifier.",
				"createdAt":              "Envelope creation timestamp.",
				"shredded":               "True once this holder's key has been irreversibly shredded — an identity's or a retention class's.",
				"shreddedAt":             "When the shred commit landed (ShredIdentityKey or ShredRetentionClassKey).",
				"vaultKeyDestroyed":      "True once the Vault key destruction finalized (async, privacy-worker).",
				"vaultKeyDestroyedAt":    "When the Vault key destruction was recorded.",
				"projectionsNullified":   "True once projected-row nullification finalized (async, Refractor keyshredded listener).",
				"projectionsNullifiedAt": "When the projection nullification was recorded.",
				"projectionsRebuilt":     "Retention-class holders only: true once every affected secure lens finished rebuilding past the destruction (async, Refractor).",
				"projectionsRebuiltAt":   "When the projection rebuild was recorded.",
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
		ShredRetentionClassKeyDDL(),
		RetentionClassKeyShreddedEventDDL(),
		ErasureRequestedAspectDDL(),
		SealIdentityForErasureDDL(),
		ErasureRequestedEventDDL(),
		PurgeIdentityDedupFootprintDDL(),
		DedupFootprintSweptEventDDL(),
		ErasureAspectDDL(),
		SealIdentityForErasureCompleteDDL(),
		ErasureCompletedEventDDL(),
	}
}

// piiKeyDDLScript is the declaration-only Starlark for the piiKey
// aspect-type DDL. Mirrors identity-domain's sensitiveAspectDDLScript: an
// aspect-type DDL declares shape and anchoring, not an operation handler.
const piiKeyDDLScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`
