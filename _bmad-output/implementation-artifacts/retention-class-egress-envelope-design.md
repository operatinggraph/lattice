# Retention-class egress — the bridge serves every key-holder kind custody can name

**Status: 📐 awaiting-Andrew (ratification).** Designer fire 2026-09-03 · Winston. Two things need Andrew:
a **product/privacy call** (§6, F1) and a **frozen-contract change** (§7: four clauses in Contract #3, one
in Contract #10, plus F2 — a sentence the demand contradicts). Per the 2026-09-01 exception, the contract
text lands with the build's commit; its text of record is §7. **Nothing is staged in the tree.**
**Board row:** `[bridge] Egress-unwrap serves identity-custodied sensitive aspects only — a retention-class
holder's $sensitiveRef permanently fails` (`backlog/lattice.md`, ★★, S–M).
**Parent:** `retention-class-key-custody-design.md` §11 deferred tail (a) — *"Retained-class egress refs …
Trigger: the first package needing to egress a retained record."* The trigger is met: the LoftSpace
executed lease (`verticals.md`, blocked on this row; refuted fire brief `lease-tenant-name-fire-brief.md`).
**Adversarial pass:** run this fire (§13); one blocking and five major findings folded — the design below is
the post-fold shape.

## For Andrew

- **What it does, in two lines.** The bridge's egress unwrap resolves a sensitive-ref's key envelope from
  a lens that enumerates identity holders alone, so a retention-class-custodied record is refused at mint
  and again at unwrap. This adds the sibling envelope projection for `vtx.retentionclass.*` holders, has
  the bridge pick its envelope source by the holder kind the ciphertext already names, and re-sources
  both refusals from one closed set of holder kinds — so a third custody kind, if one ever lands, is
  refused loudly until its projection exists rather than admitted by accident.
- **No new mechanism.** One more `full`-engine `nats-kv` lens in `privacy-base` (a copy of
  `piiKeyEnvelope` anchored on the other holder label, into its own bucket), a two-entry bucket table in
  the bridge, one shared predicate in `internal/vault`, and two small bridge hardenings the review found
  necessary for the consumer to work at all (§3.5). The MAC, the ref-verified decrypt RPC and the
  Refractor are untouched.
- **F1 — the privacy call (product altitude).** Does a retained record's *egress* licence follow its
  class? Two things widen, not one. (i) *Erasure survival:* a name snapshot custodied on
  `underwritingRecord` reaches the document vendor on a dispatch that runs after the tenant's
  `ShredIdentityKey`; under identity custody that dispatch fails closed. (ii) *Anchor reach:* today an
  egressable sensitive aspect can only hang off an identity vertex (identity custody requires it); after
  this, any business vertex — a lease application, an appointment — can carry one, on any pattern step
  whose subject it is. **My recommendation: yes, it follows the class, for both.** Custody answers who can
  erase; the egress declaration and the operation's actor answer who may read (parent §6.4); and an
  executed lease re-rendered after the tenancy is the retained obligation working as declared. §6 prices
  the stricter postures and names each one's revive trigger.
- **F2 — a contract sentence the demand contradicts.** Contract #3 §3.10: *"A retained record must not
  duplicate its subject's direct identifiers, or the subject's erasure is defeated by that duplication."*
  Your 2026-09-01 product answer — snapshot the party names onto the application — is exactly such a
  duplication, and the shipped `underwritingParties` aspect already retains co-applicant and guarantor
  names under the same class. §7.1(d) proposes narrowing the sentence to the obligation it was reaching
  for. Option A keeps it; then the tenant-name snapshot is non-conformant at the contract, not the
  mechanism, and the verticals row closes won't-fix.
- **A committed clause that is not enforced, found on the way (§6.1).** Contract #3's *Reveal* rule —
  a decrypt with no actor and no purpose is denied for a non-identity holder — is the parent's ratified
  §6.5 posture, and the runtime does not enforce it: the wholesale decrypt RPC checks only that a holder
  key is present, and Loupe's Reveal has followed a class holder "as readily as" an identity since the
  parent's own Fire 1 item 2 landed (`a4a2ccd9`). Inc 1 builds the refusal where the parent placed it
  (the RPC). It is building to a ratified design and a committed promise, so I have not made it a fork;
  strike it if you want it separate.
- **Blast radius, measured live (§5):** 118 identity envelope rows; the new lens projects **2** rows (the
  sibling status lens over the same anchors projects exactly 2 today); **0** retained aspects are
  egressed by any shipped pattern, so nothing changes behaviour until a package templates one.

---

## 0. The ask, verbatim, clause by clause

From the filing commit `bd7cb745` (Lattice Steward, 2026-09-02):

> *"resolveSensitiveRef (internal/bridge/egress.go:190) hard-rejects any non-identity holder, and its
> source (privacy-base's piiKeyEnvelope lens) is MATCH (i:identity) only — there is no envelope
> projection for a vtx.retentionclass.* holder."* → §2.1 confirms both, and names the second gate the row
> did not (the Processor's, at mint). Both are re-sourced, not merely lifted (§3.2).

> *"a new envelope lens plus a bridge branch"* → §3.1, §3.3. The lens is a sibling into its **own** bucket
> (§3.1 says why). The "bridge branch" turned out to be three: the bucket table, and two hardenings the
> adversarial pass found the consumer needs (§3.5).

> *"a real privacy question: should retention-class PII, which exists specifically to survive its
> subject's erasure, ever reach a third-party vendor?"* → §6 (F1), for Andrew.

From the blocked verticals row (`verticals.md`, LoftSpace): *"Andrew's 2026-09-01 fallback direction is
sound Loom/Processor-side but the bridge can't decrypt a non-identity-holder `$sensitiveRef` at all — a
real second primitive gap."* → the Loom/Processor half is the verticals lane's increment (§12 Inc 2). The
refuted brief built and tested it; the review found its emitted **shape** must change too (§3.5), which
§12 Inc 2 now states.

## 1. Grounding ledger (every load-bearing fact, verified in code or live this fire)

| # | Fact | Where |
|---|---|---|
| 1 | The bridge refuses any holder whose type segment is not `identity`, permanently, before the envelope read | `internal/bridge/egress.go:190-194` |
| 2 | The envelope read is `KVGet(envelopeLensBucket, keyHolderKey)`; the bucket is the literal `privacy-pii-key-envelopes` | `egress.go:19`, `:262` |
| 3 | `piiKeyEnvelope` is `MATCH (i:identity) WHERE i.piiKey.data.keyId <> null`, projecting `key, wrappedDEK, keyId, kekVersion, alg, shredded` | `packages/privacy-base/lenses.go:211-218` |
| 4 | The Processor refuses to mint an egress ref for a non-`identity` holder, typed, at hydration — inside the `v != nil` arm (a Vault-less pipeline mints a MAC-less marker, which the bridge refuses at `detectSensitiveRef`) | `internal/processor/sensitive_decrypt.go:246-258`, `:369-392`; `egress.go:100-104` |
| 5 | Custody is DDL-resolved: `keyHolderFor` returns the class holder for `retentionClass`, the anchoring identity otherwise, and refuses an unknown kind; the class holder's **type** is validated at step 6 | `internal/processor/step65_encrypt.go:221-245`; `step6_validate.go:350-360` |
| 6 | The two custody kinds are the only ones install admits; the class holder key is resolved at build time from `(pkg, class)` | `internal/pkgmgr/custodyscope.go:33-40`; `build.go:758` |
| 7 | Every decrypt site resolves the holder from the ciphertext's own `keyId`, which is AEAD associated data — a substituted holder fails the tag | `internal/vault/keyholder.go:34-44` |
| 8 | `KeyHolderType` exists "for the two egress sites" and returns the vertex-type segment | `keyholder.go:45-61` |
| 9 | The ref-verified decrypt RPC resolves the holder from `keyId`, recomputes the MAC before any decrypt, delegates to `Vault.Decrypt` — no holder-kind refusal, **no actor field** | `internal/vault/service.go:461-530` |
| 10 | The **wholesale** decrypt RPC checks only that a holder key is non-empty — no holder-kind refusal; Loupe's Reveal resolves the holder from `keyId` and calls it for any kind | `service.go:255-290`; `cmd/loupe/vault.go:253-285` (since `a4a2ccd9`, 2026-08-08) |
| 11 | The Vault's shred gate is `envelope.Shredded` OR the backend's in-memory set, checked **before** the empty-`WrappedDEK` check; a lens row must therefore carry `shredded` | `internal/vault/local.go:398-404`; lens comment `lenses.go:189-210` |
| 12 | `ShredRetentionClassKey` rewrites the class `.piiKey` with `shredded: true` (placeholder carrying `keyId: holder` when never minted) | `packages/privacy-base/shred_retention_class_key.go:176-230` |
| 13 | The MAC input covers `ct.KeyID`, so custody is authenticated, not re-derived, at unwrap | parent §8.9; `internal/vault/refmac.go` |
| 14 | The Refractor already reads a class holder's `.piiKey` for Secure Lenses, keyed on `HolderTypes`; the class-shred rebuild fan-out narrows by `HolderTypes` too, so a plain envelope lens is outside it | `internal/refractor/pipeline/secure.go:221`; `cmd/refractor/main.go:344-350`; `registry_probe.go:263` |
| 15 | `retentionKeyStatus` projects policy + shred status for `(r:retentionclass)` and **no envelope columns**, by design, into `privacy-retention-keys` | `lenses.go:25-37`, `:173-185` |
| 16 | A plain (non-actor-aggregate) `nats-kv` lens's `Truncate` has no prefix, so it purges the whole bucket. The two **automatic** truncating paths (taxonomy narrowing, re-activation) check `RebuildTruncateIsScoped` and **decline** an unscoped purge; the **operator** `Rebuild(truncate=true)` path does not, and truncates the whole bucket | `internal/refractor/projection/driver.go:868-898`; `cmd/refractor/taxonomy_reload.go:305-319`; `internal/refractor/pipeline/rebuild.go:385-393` (declines), `:434-452` (does not) |
| 17 | The Cypher engine has no label alternation; `LabelExpansion` is the taxonomy subtype expansion, not OR | `internal/refractor/ruleengine/full/label_expansion.go` |
| 18 | An unlabeled anchor seeds by listing the whole bucket | `lenses.go:295-312` (the identityErasureResidue comment, citing `executor.go`) |
| 19 | The bridge's read set is "whatever `$JS.API.>` reaches minus the core-kv denies" — a new lens bucket needs no grant, no bootstrap registration (`ReservedBuckets` covers platform buckets only), and the adapter auto-creates it; the Refractor holds `$KV.>` | `internal/natsperm/matrix.go:451`, `:504-520`; `internal/bootstrap/platform_buckets.go:135-143`; `cmd/refractor/main.go:81`, `:1064-1072`; pin `bridge_egress_test.go:98-140` |
| 20 | The bridge's unwrap retries an absent row 5 times then fails permanently; a lens lag past the budget is terminal for the dispatch | `egress.go:31-38`, `:200-212`; `internal/leaseconvergence/harness_test.go:336-350` |
| 21 | `unwrapEgressParams` walks the **top level** of `params` only; the adapter receives both the unwrapped `Params` (flat string map) and the **original** event params as `RawParams` | `egress.go:127-165`; `dispatch.go:222-228` |
| 22 | The docGen adapter reads its document from `RawParams` (`params.doc.*`, nested one level), and `tenantName` there is a `string` — a marker object fails the unmarshal, terminally | `internal/bridge/docgen_adapter.go:20-32`, `:129-134`, `:42`; emitter `packages/lease-signing/leasedoc_scripts.go:263-271` |
| 23 | The one shipped sensitive egress consumer templates at the **top level** and reads `req.Params` | `packages/lease-signing/patterns.go:52`; `internal/bridge/fake_background_check.go:68`; pin `internal/leaseconvergence/sensitive_param_egress_test.go:81-82` |
| 24 | The only egress-ref producer is Loom's actuator; the keys are `subjectKey + "." + aspect` parsed from a step's `subject.<aspect>.data.<field>` params | `internal/loom/engine.go:1085-1115`, `externaltask_params.go:42-86` |
| 25 | For a direct submitter, `env.Actor` is whatever the publisher stamped: the Gateway stamps a verified actor for external callers, and internal engines "keep their sanctioned direct-submit path"; the ratified admission predicate admits `env.Actor ∈ {Loom, Weaver}` | `internal/gateway/gateway.go:1-13`; `cmd/processor/main.go:159-162`; `egress-read-declaration-authority-design.md` §3.1 |
| 26 | The runtime actor check on an external-emitting op is the script's own `if op.actor != primordialActor["loom"]: fail(...)`; the lint proves the guard's shape | `packages/lease-signing/leasedoc_scripts.go:164-165`; `scripts/lint-conventions.go:1730-1870` |
| 27 | Retention classes declared today: `underwritingRecord` (3 aspects, lease-signing), `clinicalRecord` (1 aspect, clinic-domain) | §5 C2 |
| 28 | Both class holders carry a `.piiKey` live; 0 shipped patterns template a class-custodied aspect | §5 C5, C1 |
| 29 | `underwritingParties` retains `guarantorName`, `coApplicantName`, `coApplicantContact` under the class — third-party direct identifiers, by design | `packages/lease-signing/ddls.go:405-440` |
| 30 | The refuted brief built the snapshot half and it passed; the egress half failed at mint with the exact refusal in #4 | `lease-tenant-name-fire-brief.md` header |
| 31 | A retention-class holder is never *submitted* for tombstone by uninstall; Contract #8 still names an "already-stranded (found already tombstoned)" holder as a recognized state; a lens's `delete_mode` defaults to `hard`, so an anchor tombstone retracts the row | `docs/contracts/08-package-install.md:115-125`; `internal/refractor/lens/schema.go:252-259` |

## 2. Problem, re-derived

### 2.1 Two gates, one lens, and the reason the parent gave for both

The parent design moved custody to the ciphertext's `keyId` (ledger #7) and then, at its own
adversarial pass (item 6), *narrowed* rather than removed the egress refusal: *"the bridge's envelope
source is the identity-only `piiKeyEnvelope` lens, so the refusal is narrower, not unnecessary."* Its
Fire 1 item 2 made the refusal **typed at both sites**: the Processor refuses at mint so a script author
sees one error naming the holder kind (ledger #4), and the bridge refuses at unwrap (ledger #1) *"because
the bridge is where an unserveable holder would otherwise decay into an envelope that simply never
projects"* — an absence indistinguishable from lens lag, retried until the budget is spent (ledger #20).
Both comments cite the lens as the reason. The lens is the whole gap.

The ref-verified RPC deliberately carries no such refusal (ledger #9), and the parent said so: *"the
identity-only rule lives in the Processor and the bridge, not at the crypto boundary … the rule's
enforcement points move together or not at all."*

### 2.2 What the refusal costs today

The live consumer is the executed lease. The brief that tried to close it (ledger #30) built the
snapshot — `SignLease` walks `applicationFor`, reads the applicant's `.name`, writes a sensitive
`.tenantName` on the leaseapp custodied on a retention class — and the harness rejected
`CreateLeaseDocInstance` at hydration with the mint-time refusal. Its alternatives were: make the name
non-sensitive (a plaintext party name at rest with no destruction path — the shape clinic's shipped F3(b)
decision rejects); link-discovered egress reads (**held** by Andrew 2026-09-01,
`declared-path-reads-design.md`); or this. Andrew's product answer at that hold chose the snapshot.

### 2.3 What the refusal hides — the consumer's shape is wrong too

Lifting the two gates does not, on its own, put a name on the lease. The unwrap substitutes markers at
the top level of `params` only (ledger #21), the docGen event nests every document field under
`params.doc` (ledger #22), and the adapter reads that nested object from the **original** params, where a
marker would arrive as a `$sensitiveRef` object in a `string` field and fail the render terminally. The
one working sensitive egress (background check) has the opposite shape on both axes (ledger #23). So the
consumer must template the name as a **top-level** param and the adapter must read it from the
unwrapped map — §3.5, §12 Inc 2 — and a nested marker must be a loud refusal rather than a retained
record's ciphertext riding to a vendor inside `RawParams` (§3.5(b)).

## 3. The shape

### 3.1 A sibling envelope lens, into its own bucket

`packages/privacy-base` gains one lens, a copy of `piiKeyEnvelope` anchored on the other holder label:

```
CanonicalName: "retentionClassKeyEnvelope"   Class: "meta.lens"   Adapter: "nats-kv"   Engine: "full"
Bucket: RetentionKeyEnvelopeBucket = "privacy-retention-key-envelopes"

MATCH (r:retentionclass)
WHERE r.piiKey.data.keyId <> null
RETURN
  r.key AS key,
  r.piiKey.data.wrappedDEK AS wrappedDEK,
  r.piiKey.data.keyId AS keyId,
  r.piiKey.data.kekVersion AS kekVersion,
  r.piiKey.data.alg AS alg,
  r.piiKey.data.shredded AS shredded
```

Same six columns, same absence semantics (no row until the class's `.piiKey` is minted, which step 6.5
does lazily in the same batch as the first class-custodied write — so a ciphertext naming a class holder
implies a row will project), same `shredded` projection the Vault's gate needs (ledger #11, #12). The
row is a complete `vault.Envelope` (`json` tags match), so the bridge parses it with the code it has.

**Why its own bucket, not `privacy-pii-key-envelopes`.** A plain lens has no output descriptor, so its
`Truncate` has no prefix and purges the whole bucket (ledger #16). The two automatic truncating paths
decline an unscoped purge, so the hazard is **the operator's `Rebuild(truncate=true)`** — a routine
verb on a lens whose spec an operator has just changed — which would purge the identity lens's 118 rows
for the seconds-to-minutes the replay takes, against a five-attempt unwrap budget (ledger #20), for every
egress dispatch in flight. Two buckets, two independent truncates; and the decline path's warning
("rows for the dropped labels are not retracted") never applies to either.

**Why not widen `piiKeyEnvelope`'s MATCH.** No label alternation exists (ledger #17), and an unlabeled
anchor seeds from the whole Core-KV bucket — 182,835 keys live — with an anchor test per event of every
type (ledger #18).

**Period bucketing (parent tail (b)) is covered by construction:** a bucketed holder is *id-varies,
type-fixed*, and the lens anchors on the label.

### 3.2 One closed set of holder kinds, read by both gates

`internal/vault/keyholder.go` gains the set the platform can mint — exactly the two custody kinds
(ledger #5, #6):

```go
// KeyHolderKinds is the closed set of key-holder vertex types custody can resolve to
// (Contract #3 §3.10: identity, retentionClass). A ciphertext naming any other kind was
// not written by the Processor's commit path.
func KeyHolderKinds() []string { return []string{"identity", "retentionclass"} }
func IsKeyHolderKind(vertexType string) bool
```

- **Processor, at mint** (`refusableEgressHolder`): refuses `!IsKeyHolderKind(type)` instead of
  `type != "identity"`. With both kinds served this arm is unreachable from any Processor-written
  ciphertext — it is kept as the **pin that makes a third custody kind fail at mint, typed**, until its
  envelope projection exists. Its doc comment is rewritten (the current one names the lens as the
  reason). It sits inside the `v != nil` arm (ledger #4); that is fine — a Vault-less pipeline's marker
  is refused by the bridge for having no MAC.
- **Bridge, at unwrap** (`resolveSensitiveRef`): the refusal becomes *"no envelope source for holder kind
  %q"*, decided by the bucket table below, which is pinned equal to `KeyHolderKinds()` by a test — so the
  Processor's set and the bridge's set cannot drift apart (one value, read twice).

### 3.3 The bridge picks its envelope source by holder kind

```go
// envelopeBucketFor maps a key holder's vertex type to the lens read model that
// projects that holder kind's live envelope. Every kind in vault.KeyHolderKinds
// has an entry; a kind with none is refused permanently, naming the kind.
func envelopeBucketFor(holderType string) (bucket string, ok bool) {
    switch holderType {
    case "identity":       return identityEnvelopeBucket, true       // "privacy-pii-key-envelopes"
    case "retentionclass": return retentionClassEnvelopeBucket, true // "privacy-retention-key-envelopes"
    }
    return "", false
}
```

`fetchLiveEnvelope(ctx, bucket, keyHolderKey)` takes the bucket; everything after it — transient budget
on absence, permanent on a bad row, `ErrKeyShredded` permanent, MAC-unverified permanent — is unchanged.
The literals stay literals for the reason `egress.go:14-19` gives (no `packages/` import from
`internal/bridge`'s non-test code). **The pin lives in `internal/bridge`:** a `_test.go` there imports
`packages/privacy-base` and asserts the two literals equal the package's two exported bucket constants
and the table's key set equals `vault.KeyHolderKinds()`. (A pin in `packages/privacy-base` cannot see an
unexported bridge constant; `verify-package-privacy-base.go`'s bucket table is a duplicated literal, not
a pin. Phase 0 runs `go list -deps` to confirm the test-only import introduces no cycle.)

### 3.4 What contains a forged declaration — stated honestly

`contextHint.egressReads` is contained today by wire-struct shape: no shipped client can express it, and
a raw `ops.>` credential can (`egress-read-declaration-authority-design.md` §2.2). Widening the holder
set widens what such a credential could carry to a vendor from subject-erasable PII to retained records.
**The ratified admission binding does not close that**: it admits `env.Actor ∈ {Loom, Weaver}`, and a
direct publisher on `core-operations` stamps its own `env.Actor` (ledger #25) — the Gateway's stamping is
unforgeable for *external* callers only. The credential class in question is platform-trusted
infrastructure (the app-tier read-scope design is shelved on exactly "revive when the app-tier NKey stops
being trusted infra"), and the identity path carries the same residual today. This design **does not
sequence behind that fire** and claims no closure from it; §11 records the residual as unchanged in kind.

### 3.5 Two bridge hardenings the consumer needs

**(a) The docGen adapter reads `tenantName` from the unwrapped map.** `docGenFields` keeps its nested
`doc` from `RawParams` (the numeric fields need it — `docgen_adapter.go:26-28`), and gains one read from
`req.Params["tenantName"]` — the flat, unwrapped string map every adapter already receives — used for the
"Tenant" line ahead of the existing `doc.Applicant` fallback. This is the shape the background-check
consumer already has (ledger #23). The verticals increment then templates
`"tenantName": "subject.tenantName.data.value"` at the **top level** of the `leaseDocument` step's
`Params`, beside `"family"`, and leaves `doc` as it is.

**(b) A nested marker is refused, permanently.** `unwrapEgressParams` gains a depth-bounded scan of
each non-marker value for a `$sensitiveRef` key; a hit is `permanentEgressFailure("marker at a depth the
unwrap does not serve")`. Today a nested marker rides out to a vendor as ciphertext + MAC inside
`RawParams` (ledger #21) — inert (the MAC is bound to this `requestId` and only the bridge holds the
decrypt grant), but after this design it is a *retained record's* ciphertext leaving the platform, and
the same shape is the silent way Inc 2 fails if it templates into `doc`. Refusing converts both into one
typed terminal outcome.

### 3.6 Read path / write path

Reads: the bridge reads one lens read model per holder kind (P5, as today). Writes: none — no operation,
no Core-KV mutation, no schedule. The Refractor projects the new lens like any other package lens
(bucket auto-created on activation; hot-reload on install; outside the class-shred rebuild fan-out,
ledger #14, which is correct — its rows are envelopes, not decrypted columns).

## 4. State-lifetime table — the `retentionClassKeyEnvelope` row

| Boundary | What happens | Consumer effect |
|---|---|---|
| **Created** | On the class's first `.piiKey` mint (step 6.5, lazily, same batch as the first class-custodied write) | A ref naming this holder can exist only after the mint, so the row is never structurally absent — only lagging |
| **Never written** | A class with a `.retentionPolicy` and no `.piiKey` (no record ever written) projects **no row** | No ciphertext names it, so no ref reaches the bridge; nothing to serve |
| **Lag** | CDC → Refractor → bucket; same latency as the identity lens | Bridge: transient, 5 attempts on the redelivery floor, then permanent (`egress.go:200-212`) — unchanged posture; §10 keeps the harness floor |
| **Shred** | `ShredRetentionClassKey` rewrites `.piiKey` (`shredded: true`; placeholder with `keyId` set when never minted, so the WHERE still admits it) → row updates | Vault refuses `ErrKeyShredded` before it looks at `wrappedDEK` (ledger #11) → bridge permanent → terminal `replyOp` (converge, never park) |
| **Holder tombstoned ("already-stranded", ledger #31)** | `delete_mode: hard` retracts the row; the ciphertext and `.piiKey` still exist in Core KV | Bridge sees absence → 5 transient attempts → permanent, worded "not yet projected". Fail-closed, and the same posture as a tombstoned identity holder today. The wording is accepted: distinguishing it would need a Core-KV read the bridge is denied (P5) |
| **Rebuild / truncate** | Own bucket; an unscoped operator truncate purges only its ≤2 rows, then replays | Identity dispatches unaffected; a class dispatch in that window is transient (as any lens rebuild is) |
| **Crash / restart** | Durable consumer; no in-memory state anywhere in this design | None |
| **Uninstall of the declaring package** | The holder is never submitted for tombstone (ledger #31); `.piiKey` stays; row stays | A ref minted by a still-installed op unwraps; there is no such op after uninstall |
| **Period bucketing (parent tail b)** | New holder ids under the same label | Project by construction; no lens edit |
| **A third custody kind** | Not in `KeyHolderKinds()` until someone adds it | Processor refuses at mint, typed; bridge refuses at unwrap; the pin forces a bucket entry when the kind is added |

## 5. Executable censuses (run this fire; raw output pasted)

**C1 — egress-ref producers and templated egress params.** Expected: one Loom producer; one shipped
pattern templates sensitive aspects (identity-custodied, top-level); zero template a class-custodied one.

```
$ grep -rln 'egressReads' packages/ | grep -v _test
packages/orchestration-base/external_params.go
packages/lease-signing/patterns.go
packages/lease-signing/leasedoc_scripts.go
packages/lease-signing/scripts.go
packages/lease-signing/lenses.go
$ grep -rn '"subject\.' packages/*/patterns.go        # sensitive-templating rows only
packages/lease-signing/patterns.go:52:  "name": "subject.name.data.value", "dob": "subject.dob.data.value"
(capability-author + privacy-base rows template non-sensitive request/guard fields)
```

**C2 — retention-class custody declarations.** Expected: 4 aspect DDLs, 2 classes, 2 packages. The
reviewer re-ran with the family pattern `RetentionClass\b|retentionClass` and found no fifth.

```
$ grep -rn 'CustodyKindRetentionClass' packages/ --include='*.go' | grep -v _test | grep 'Custody:'
packages/lease-signing/ddls.go:362   (.profile)
packages/lease-signing/ddls.go:424   (.underwritingParties)
packages/lease-signing/ddls.go:554   (.decidedProfileSnapshot)
packages/clinic-domain/ddls.go:1020  (.encounter)
```

**C3 — every holder-kind decision site.** The first pattern (`KeyHolderType(`) found four; the reviewer's
broader `KeyHolder(` found the fifth — Loupe's Reveal, which resolves the holder and never tests its kind
(ledger #10). Expected after the build: the two egress gates re-sourced, the wholesale RPC gains the kind
refusal (§6.1), the Secure-Lens `HolderTypes` check and the class-shred consumer untouched.

```
$ grep -rn 'KeyHolderType(\|KeyHolder(' internal cmd --include='*.go' | grep -v _test | grep -v 'func KeyHolder'
internal/bridge/egress.go:190              != "identity"          ← re-sourced
internal/processor/sensitive_decrypt.go:386 != "identity"         ← re-sourced
internal/vault/service.go:~485             KeyHolder only (ref RPC; MAC is the gate)   untouched
cmd/loupe/vault.go:258                     KeyHolder only, no kind test               ← §6.1
internal/refractor/pipeline/secure.go:221  slices.Contains(col.HolderTypes, …)        untouched
internal/refractor/classkeyshredded/manager.go:327                                    untouched
```

**C4 — consumers of the identity envelope bucket (tests included).** Expected: the bridge and
loftspace-app's blob path read it by key; nothing lists it; nothing assumes a class row is absent.

```
$ grep -rn 'privacy-pii-key-envelopes\|PiiKeyEnvelopeBucket\|envelopeLensBucket' --include='*.go' . | grep -v _bmad
internal/bridge/egress.go:19,:262          KVGet by holder key
cmd/loftspace-app/objects_crypto.go:37     KVGet by identity key
internal/natsperm/bridge_egress_test.go:105,:116,:137   read-isolation pin (mirrored for the new bucket, §10)
internal/bridge/egress_test.go, cmd/loftspace-app/objects_crypto_test.go   fixtures
```

**C5 — live (2026-09-03, `nats --nkey=deploy/nkeys/lattice.nk`).**

```
privacy-pii-key-envelopes: 177 values, 118 vtx.identity.* rows   (core-kv vtx.identity.*.piiKey: 118)
core-kv vtx.retentionclass.*: 2 holders (7hRM… clinicalRecord, ZFSe… underwritingRecord), both with .piiKey
privacy-retention-keys (the sibling status lens over the SAME anchors): 2 rows  ← the new lens's day-one row count
class-custodied aspects live: leaseapp.profile 2 · leaseapp.underwritingParties 1 · appointment.encounter 9
leaseapp roots: 64
```

**C6 — registration sites of a privacy-base lens.** The first grep keyed on the sibling's name and was
answer-shaped; the corpus enumeration is the census. `internal/refractor` carries **nine**
`*corpus_census*_test.go` files; the four the name-grep hid (`actor_onekey`, `actor_walk_scope`,
`anchor_hopindex`, `personal_derivation`, `rel_projection`) pin no privacy lens, so the conclusion holds
but the build re-runs all nine.

```
$ ls internal/refractor/*corpus_census*_test.go | wc -l     → 9
$ grep -rln 'retentionKeyStatus\|RetentionKeyStatusBucket' . --include='*.go' --include='*.md' --include='*.yaml' --include='*.js' | grep -v _bmad
packages/privacy-base/{lenses.go, package_test.go, lens_cypher_test.go, manifest.yaml, ddls.go, shred_retention_class_key.go}
scripts/verify-package-privacy-base.go
internal/refractor/{plain_scanroot, plain_with_alias_closure, branch_decomposition, branch_decomposition_pins,
                    grouping_reduction, label_derivation}_corpus_census_test.go
internal/refractor/classkeyshredded/manager{,_test}.go
cmd/loupe/{vault.go, vault_test.go, web_logic_test.go, web/js/logic/retention.js, web/js/views/component.js}
```

The build adds the new lens at: `lenses.go`, `package_test.go`'s `wantLenses`, `lens_cypher_test.go`,
`verify-package-privacy-base.go`'s `privacyLensChecks`, the manifest + `Version` bump; and re-runs all
nine corpus-census pins. Loupe needs nothing — it lists buckets by lens.

## 6. The privacy call (F1) — a retained record's egress licence follows its class

**The question the row asked:** should retention-class PII, which exists to survive its subject's
erasure, ever reach a third-party vendor?

**What the platform says, and what it enforces.** Contract #3 §3.10 *Reveal* denies a decrypt carrying
no actor and no purpose for a non-identity holder; the parent's §6.4 separates custody (*can this be
decrypted at all*) from authorization (*which actor sees it*). The *Reveal* clause is **not enforced**
today (ledger #10; §6.1 below builds it), so this section does not lean on it as a live boundary. It
leans on what the egress path actually carries, in the deciding code: a ref exists only because an
operation (a) **declared** the read under `egressReads` for a **named adapter** — the purpose, in the
operation's own contract vocabulary (Contract #2 §2.5 class (f)); (b) ran under an **actor** the
script's own guard pins to the engine's primordial actor (ledger #26 — the script's `fail`, not the lint
that keeps it well-formed); and (c) carries a Processor-minted **MAC** over `{ref, requestId, ciphertext}`
that the decrypt RPC verifies before touching a key. The ref-verified RPC itself carries no actor field
(ledger #9): the licence is granted at mint, and the RPC verifies that the mint happened. That is an
actor, a purpose and provenance for every holder kind; the wholesale RPC has none of the three.

**What changes, concretely — two widenings, not one.**

1. *Erasure survival.* Before: a snapshot of the tenant's name on the lease application is unreachable
   by any vendor. After: it reaches the document vendor when the `leaseDocument` step dispatches — at
   signing, while the subject is a live tenant, and on any later dispatch of that pattern (a re-render, a
   renewal package that adopts the same template) including after the subject's `ShredIdentityKey`. Under
   identity custody that later dispatch fails `ErrKeyShredded` and the document renders a NanoID; under
   the class it renders the name. **That difference is the class's declared meaning**: the landlord's
   contract record outlives the tenant's erasure, and a contract record without its parties' names is not
   the record the obligation retains (F2).
2. *Anchor reach.* Identity custody requires the aspect's anchor to **be** an identity (ledger #5), and
   Loom's egress keys are `subjectKey + "." + aspect` (ledger #24) — so today an egressable sensitive
   aspect exists only on an identity vertex, and only a step whose subject is that identity can carry it.
   Retention-class custody permits any anchor. After this, a sensitive aspect on a lease application, an
   appointment, or any business vertex can be templated by any pattern step over that subject. The
   admission gates are unchanged (the DDL declares custody at install; the pattern templates at install;
   the script's actor guard at run) — but the population a package author *can* egress grows from
   "the subject's own PII" to "any retained record on the step's subject".

**Recommendation: F1 = yes, licence follows the class, on both widenings.** No new declaration, no new
refusal. The package author who declares a retention class, templates one of its aspects into an external
adapter's params, and ships the step has decided three times, in the three places the platform reads it.

**The stricter postures, priced (each is an alternative in §9):**

| Posture | Cost | Why not now | Revive trigger |
|---|---|---|---|
| Per-class `egressable: false` declaration | a third declaration by the same author; a refusal with no distinct principal behind it | duplicates what templating already says | Andrew wants a platform-level posture for a class family (e.g. clinical) |
| Refuse egress once the record's *subject* is erased | the snapshot must carry a `subjectKey`, and the mint must read that identity's `.piiKey` — one internal `KVGet`, fail-closed only | the subject is caller-supplied (parent §3.3), and it inverts the class's meaning for a contract record | a class whose obligation is to the fact, not the person, needing egress |
| Route retained egress through the purpose RPC (parent tail (c), `decryptretained`) | a second decrypt path for the bridge | the purpose is already in the declaration and the MAC; tail (c) is for actor-carrying operator reveal, a different consumer | an operator reveal / audit export of a retained record |

### 6.1 The *Reveal* clause is unenforced — Inc 1 builds it where the parent placed it

The parent's §6.5 said: *"Increment 1's posture: refuse, structurally … Loupe's Reveal already refuses a
non-identity anchor … [its] reason must be re-derived: it becomes a keyId-based refusal."* Fire 1 item 2
(`a4a2ccd9`) moved Loupe to `keyId` and **dropped the refusal** instead of re-deriving it: the handler's
comment now says the reveal follows a class holder "as readily as" an identity (ledger #10), and the
wholesale RPC it calls tests only that a holder key is present. The committed contract clause promises a
denial the runtime does not make — fail-open at the operator console, for exactly the records the parent
said have "no data subject whose grant scopes the disclosure".

The fix is three lines at the enforcement point the parent named: `handleDecrypt` refuses
`KeyHolderType(in.KeyHolderKey) != "identity"` with a typed error, Loupe surfaces it and its comment is
rewritten to the parent's re-derived reason. No contract change — the clause already says it. It belongs
in Inc 1 because it is the same seam (the holder-kind gates), and because §6's argument is only honest if
the *other* decrypt path is what the contract says it is.

## 7. Contract surface — text of record (lands with the build's commit)

All four Contract #3 clauses assert a refusal the build removes, so they are observable against the
current text and this is a contract change. Per Andrew's 2026-09-01 exception they are held out of the
tree until the build; this section is their text of record. Mechanism-free, as a public contract must
be.

### 7.1 Contract #3 §3.10 — `docs/contracts/03-mutation-batch-event-list.md`

**(a) Lines 173-175, replace:**

> **The external-egress boundary carries identity-held records only.** The bridge resolves a holder's
> envelope from a lens that enumerates identity holders alone, so an egress ref for any other holder type
> is refused, with the type named, at the site that authors the operation.

**with:**

> **The external-egress boundary serves every key-holder kind custody can name.** A sensitive-ref's
> holder is served from a live envelope projection for that holder kind. An egress ref whose holder kind
> has no such projection is refused, with the kind named, at the site that authors the operation — never
> deferred to the boundary as an envelope that fails to appear. A sensitive-ref is served only where the
> operation placed it; one carried inside a nested parameter value is refused at the boundary.

**(b) Lines 236-240, replace:**

> A sensitive-ref for a **non-`identity`** holder is **refused** at hydration until the external-egress
> key-envelope read path covers non-identity holders; the refusal is typed and loud, never a silent
> pass-through of raw ciphertext.

**with:**

> A sensitive-ref whose holder kind the external-egress envelope read path does not serve is **refused**
> at hydration; the refusal is typed and loud, never a silent pass-through of raw ciphertext.

**(c) Lines 242-247 (*Reveal*), append one sentence:**

> An external-egress unwrap is not such a request: the ref it opens was minted inside an operation that
> declared the read for a named adapter and ran under an accountable actor, and the unwrap verifies that
> provenance before any key is touched. Egress is therefore licensed for every holder kind by the
> declaration, not by the holder's custody.

(The clause's existing denial is built by §6.1 in the same fire, so the appended sentence lands on a
promise that is then true.)

**(d) Lines 190-193 — F2, Andrew's call.** Current:

> A retained record must not duplicate its subject's direct identifiers, or the subject's erasure is
> defeated by that duplication.

Option B (recommended):

> A retained record must not duplicate its subject's direct identifiers beyond those the retention
> obligation itself requires: a contract record keeps its parties' names for as long as the contract must
> be kept; a record whose obligation is to the fact and not the person carries none. Duplication past
> that line defeats the subject's erasure.

Option A keeps the sentence; then the tenant-name snapshot (and the shipped `underwritingParties`
names) are non-conformant, and the verticals row closes as *won't-fix at the contract*.

### 7.2 Contract #10 §10.5 — `docs/contracts/10-orchestration-loom.md` lines 111-113

Replace *"using the identity's **live** key envelope"* with *"using the holder's **live** key envelope"*
and *"a shredded identity's ref"* with *"a shredded holder's ref"*. Wording only; the promise is
unchanged and holder-kind-agnostic.

### 7.3 What has no contract surface

The lens, the bucket, the bucket table, the shared kind set, the docGen read, the wholesale-RPC refusal
(§6.1 builds to an existing clause) — mechanism, all of it.

## 8. Reconciliation with the existing mental model

- **Didn't we already handle this?** The parent sized it (§8.9, §11 tail (a)) and deferred it behind
  *"the first package needing to egress a retained record"* with zero live consumers. The consumer
  arrived 2026-08-27 and was refused at mint exactly as the parent's Fire 1 item 2 intended. This is that
  tail, built to the parent's own sizing — *"widen the envelope read path past `MATCH (i:identity)` and
  lift the two identity-only holder refusals"* — with three corrections the parent could not have made:
  the widening is a sibling lens in its own bucket (ledger #16), the refusals are re-sourced from one set
  rather than lifted (§3.2), and the consumer's params shape has to change for the plaintext to land
  anywhere (§3.5).
- **Does it contradict the design of record?** The *Reveal* rule stands as written for the wholesale
  RPC — and is built (§6.1). §7.1(c) says why egress is not that RPC. The parent's tail (c) purpose RPC
  stays deferred with its trigger. The held `declared-path-reads` design is not revived: the snapshot is
  subject-rooted, so the shipped template path serves it once it is top-level.
- **New state?** One lens row per class holder, in a read model — the same state the identity lens keeps
  for identities. No engine, bridge, or Processor state.
- **Why the Processor gate stays when it cannot refuse.** It is the difference between a third custody
  kind failing as one typed error at mint (what made the 2026-08-27 block diagnosable in one line) and
  failing as five silent retries at the bridge. It costs a string comparison.

## 9. Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 1 | **Do not have this thing.** Keep both refusals. The executed lease names its tenant by NanoID; the verticals row closes won't-fix; the parent's tail (a) stays deferred. | The demand is one consumer, but it is the trigger the ratified parent named, chosen by Andrew over the two other routes (§2.2). Deletion of the refusal is exactly what this design is; deletion of the *demand* is Andrew's F2 option A. |
| 2 | Widen `piiKeyEnvelope`'s MATCH to both holder labels | **Refuted**: no label alternation (ledger #17); an unlabeled anchor seeds from the whole bucket (ledger #18). |
| 3 | Sibling lens into the **same** bucket (the row's "a new envelope lens") | **Refuted**: a plain lens's truncate is unscoped, and while the automatic rebuild paths decline it, the operator's `Rebuild(truncate=true)` does not (ledger #16) — a routine verb on either lens purges the other's rows under a five-attempt unwrap budget. The hazard `90d79ff8` closed applies to descriptor-bearing lenses only. |
| 4 | Widen `retentionKeyStatus` with the four envelope columns; bridge reads `privacy-retention-keys` | Viable, second choice. Its anchor is the policy, so a row exists before any key is minted with a null envelope — the bridge needs a new "present but empty ⇒ transient" branch; and it couples the operator surface's row to the bridge (the coupling `lenses.go:30` refused for the identity lens). A dedicated lens has identical absence semantics to the one the bridge already handles. |
| 5 | Bridge reads the class `.piiKey` from Core KV | **Refuted**: P5, and the bridge's core-kv read is denied by natsperm and pinned (ledger #19). |
| 6 | Retained egress via the purpose RPC (parent tail (c)) | Rejected (§6 table): the purpose already rides the declaration + MAC; tail (c) is for actor-carrying operator reveal. |
| 7 | Per-class `egressable` declaration | Rejected for now (§6 table); Andrew's F1 alternative. |
| 8 | Refuse egress after the subject's erasure (recorded `subjectKey` + mint-time shred check) | Rejected (§6 table): caller-supplied subject; inverts a contract record's declared meaning. Constructible; trigger named. |
| 9 | Demand-side rewrites of the one consumer: non-sensitive name (rejected by clinic F3(b)); link-discovered egress (held 2026-09-01); snapshot on the identity (the template is rooted on the leaseapp, not the identity) | None delivers the payoff without a platform change; the held route is Andrew's, not mine, to revive. |
| 10 | Delete the Processor gate outright (both kinds served ⇒ unreachable) | Rejected (§8, last bullet); kept as a re-sourced pin. If Andrew prefers deletion, it is a two-line change with no contract surface. |
| 11 | Recursive unwrap + substituted `RawParams` instead of §3.5 (a)+(b) | Rejected: it changes an invariant every adapter relies on (`RawParams` is the event as emitted) to serve one consumer, and it makes a nested marker's *location* a silent contract. Top-level templating is the shipped shape (ledger #23); refusing nesting is smaller and louder. |

Row 3 leans on nothing else; row 4 is the combination check for row 3 (one lens, no shared bucket) and
loses only on the transient branch. Row 8's objection — caller-supplied subject — is the parent's §3.3
objection, run back against my own shape: my design supplies no subject at all, so it does not reproduce it.

## 10. Migration, compatibility, tests

- **Install:** `privacy-base` `0.15.7 → 0.16.0` (manifest + `Version`); a plain `lattice pkg install`
  diff-applies the new lens; the Refractor creates the bucket on activation and projects 2 rows. No wipe.
- **Rolling order:** install `privacy-base` **before cycling the Processor** — the Processor is the gate
  that admits a class ref at mint, and a new Processor against a bucket that does not exist yet turns a
  mint-time refusal into five bridge attempts and a terminal failure on a real dispatch. Then the bridge
  and the Vault host (Processor again, for §6.1). Old bridge + new Processor: the bridge's typed refusal,
  unchanged. New bridge + old Processor: never sees a class ref. Every ordering fails closed; only the
  named one fails *at mint*.
- **Unit — Processor:** flip `TestEgressReads_ClassHeldRecord_RefusedAtMint`
  (`sensitive_decrypt_keyid_test.go`) to *admitted*, asserting the minted marker's `keyId` is the class
  holder; add the negative for a kind outside `KeyHolderKinds()` with a positive vector proving the ref
  reaches the gate (Processor dossier: "a gate's negative test must first prove its positive vector
  reaches the gate").
- **Unit — bridge:** flip `egress_test.go`'s `"non-identity key holder"` to a positive (seed a class
  envelope row in `privacy-retention-key-envelopes`, a MAC-minted ref over a class-held ciphertext →
  plaintext); add `"unknown holder kind"` (`vtx.foo.<id>`) → permanent, naming the kind; keep the
  shredded-class case (row `shredded: true`, empty `wrappedDEK` → permanent `ErrKeyShredded`); add the
  **nested marker ⇒ permanent** case with a positive top-level control; add the table-equals-
  `KeyHolderKinds()` + bucket-literal pin (§3.3); docGen: a top-level `tenantName` in `req.Params`
  renders on the Tenant line, and `doc.tenantName` alone still falls back to the applicant.
- **Unit — Vault + Loupe (§6.1):** `handleDecrypt` refuses a `vtx.retentionclass.*` holder with the typed
  error; the positive vector (an identity holder decrypts) sits beside it; Loupe's handler test asserts
  the surfaced refusal.
- **Unit — natsperm:** mirror `TestBridgeCoreKVReadIsolation`'s last assertion for the new bucket.
- **Package:** `package_test.go` `wantLenses`, `lens_cypher_test.go` (the spec is the identity spec with
  the label swapped — pin that equality so they cannot drift), `verify-package-privacy-base.go`.
- **E2E:** `internal/leaseconvergence` is the harness with a real Vault, a real bridge and the envelope
  bucket (ledger #20). Inc 1 adds one scenario in the **consumer's shape, not the background-check's**: a
  class-custodied aspect templated at the top level of an externalTask step whose adapter reads
  `req.Params` → the fake adapter receives plaintext; and the same aspect templated *into a nested value*
  → terminal failed outcome naming the depth. A test-package fixture suffices; Inc 1 is green before Inc 2.
- **Gates:** all `scripts/lint-*.go`; `verify-package-privacy-base`; the nine Refractor corpus-census
  pins (C6); `DIFF_BASE=<base> go run ./scripts/lint-package-version.go`; `go list -deps` for the
  test-only import in §3.3.

## 11. Risks

| Risk | Disposition |
|---|---|
| The new lens lags a fresh class mint past the 5-attempt budget | Same posture as the identity lens today; the harness's measured worst case sized the floor (ledger #20). A class key is minted once per class, not per subject, so the window is hit at most once per class lifetime. |
| A forged `egressReads` under a raw `ops.>` credential reaches a retained record | **Residual, unchanged in kind** (§3.4): that credential class is platform-trusted infrastructure and already reaches identity-custodied PII the same way; no fire on the board closes it, and this design claims no closure. The revive trigger is the app-tier read-scope design's: the app-tier NKey stops being trusted infra. |
| The two kind sets drift | One value in `internal/vault`, read by both; the bridge's table is pinned equal to it. |
| A future consumer templates into a nested value | Refused, typed, at the boundary (§3.5 b) — the same terminal outcome Inc 2 would have hit silently. |
| F2 option A | The mechanism ships and is inert; the verticals row closes at the contract. No wasted build — the lens is 2 rows and the gates are re-sourced either way. |

## 12. Decomposition for the Steward

**Inc 1 — the primitive (Lattice lane, S–M, one fire). Posture-changing: full review depth.**
`privacy-base` lens + bucket + version; `vault.KeyHolderKinds`; Processor gate re-sourced; bridge bucket
table + `fetchLiveEnvelope(bucket)`; §3.5 (a) docGen top-level `tenantName` read and (b) nested-marker
refusal; §6.1 wholesale-RPC refusal + Loupe comment; the contract clauses (§7.1 a–c, §7.2) with the
commit, and §7.1(d) with whichever option Andrew picks; the doc table below; **every test in §10** (all
owned here). No `seq:`.

**Inc 2 — the consumer (Verticals lane; the existing `verticals.md` row, unblocked by Inc 1).** The
refuted brief's steps 4–5 as built and tested — sensitive `.tenantName` on the leaseapp under a retention
class — **with one correction the review found:** `"tenantName": "subject.tenantName.data.value"` goes at
the **top level** of the `leaseDocument` step's `Params` (beside `"family"`), not into `doc`, because the
unwrap is top-level and the adapter reads the name from `req.Params` (§3.5 a). Plus the backfill the
brief's "absence problem" section requires (7 live signed applications at the time; consider the
`EgressAbsenceTolerant` descriptor floor before writing an op), and `leasedoc_scripts.go:224-232`'s
comment rewritten. Not designed here; named so its owner is unambiguous and its shape is not re-refuted.

**Doc table (Inc 1):** `docs/components/vault.md:136` (the failure-mode row: "names a holder kind with no
envelope projection"; add the wholesale-RPC non-identity refusal); `docs/components/bridge.md` In/Out
table (a second lens read model; the nested-marker refusal under "Failure modes");
`packages/privacy-base/lenses.go:25-37` (the `RetentionKeyStatusBucket` comment now also names its
envelope sibling); `internal/vault/keyholder.go:45-53` and the two gate comments (the reason changes
from "the lens enumerates identity holders alone" to "the kind has no envelope projection");
`cmd/loupe/vault.go:253-257` (the "as readily as" comment becomes the parent's re-derived reason).

## 13. Adversarial pass (run this fire, cold reviewer, security plane; findings folded)

| # | Severity | Finding | What changed |
|---|---|---|---|
| 1 | BLOCKING | The consumer cannot receive an unwrapped ref: docGen reads `RawParams`, its fields are nested, the unwrap is top-level only; the prescribed E2E would have mirrored the background-check shape and passed while Inc 2 stayed broken | §2.3, §3.5 (a)+(b), §7.1(a) last sentence, §9 row 11, §10 E2E shape, §12 Inc 2 corrected |
| 2 | MAJOR | `seq:` after the egress-declaration-authority fire buys nothing against the named threat — a direct publisher stamps `env.Actor`, and the predicate admits Loom's key | §3.4 rewritten; `seq:` dropped; §11 states the residual as unchanged in kind |
| 3 | MAJOR | §6 cited the *Reveal* clause as a live boundary; the wholesale RPC and Loupe enforce no holder-kind refusal (the parent's Fire 1 item 2 dropped it) | §6 re-argued from the deciding code; §6.1 builds the refusal in Inc 1; ledger #10; C3 widened to `KeyHolder(` |
| 4 | MAJOR | The truncate argument named the automatic paths, which decline an unscoped purge; the hazard is the operator rebuild | §3.1, §9 row 3, ledger #16 rewritten with both citations |
| 5 | MAJOR | The widening changes anchor reach (any vertex), not only holder kind — unstated for F1 | For Andrew F1 (ii); §6 widening 2 |
| 6 | MAJOR | The cross-package pin as specified was not constructible (unexported constant) | §3.3: pin lives in `internal/bridge`, test-only import, `go list -deps` gate |
| 7 | MINOR | Closed-set citations named a const block and the wrong function | Ledger #5, #6 re-cited (`step6_validate.go:350-360`, `custodyscope.go`, `build.go:758`) |
| 8 | MINOR | State table missed the stranded (tombstoned) holder | §4 row added |
| 9 | MINOR | Rolling order named the bridge; the mint gate is the Processor | §10 rewritten |
| 10 | MINOR | C6's pattern was answer-shaped (5 census files listed, 9 exist) | C6 re-run by enumeration |
| 11–13 | NOTE | Lint cited as the actor enforcement (the script's `fail` decides); C5 predicted the row count (now read from the sibling bucket); the mint gate is inside `v != nil` | Ledger #26, #4; C5 |
| 14 | NOTE | A nested marker rides out as ciphertext + MAC in `RawParams` | Folded into §3.5 (b) |

Confirmed unchanged by the pass: ledger #1–3, #7–9, #11–15, #17–20, #24, #27–31; the class-shred rebuild
fan-out does not reach a plain envelope lens; P5/P2 untouched; a never-minted-then-shredded class returns
`ErrKeyShredded` (permanent) because the shred check precedes the empty-`WrappedDEK` check.
