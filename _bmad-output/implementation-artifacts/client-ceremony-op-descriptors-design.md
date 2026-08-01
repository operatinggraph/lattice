# Client-ceremony ops become descriptor-driven — script-derived reads, a mint-and-reveal descriptor, and the credential-scoped actor

**Status: 📐 awaiting-Andrew (ratification)** — drafted 2026-08-01 (Winston, Designer fire); adversarial pass
run and folded the same fire (§10).
**Backlog row:** lattice.md *Component maintenance* — "[Pkgmgr] `OpMetaSpec` has no vocabulary for a
client-mint-and-reveal-secret ceremony". **Consumer row:** verticals.md — "Five identity ceremony ops stay
undiscoverable".
**Extends:** [vertical-package-standard.md](vertical-package-standard.md) §2 S1 + §8 (Inc 5 build note),
[facet-discovery-restoration-design.md](facet-discovery-restoration-design.md) (the generic pane executor),
[edge-showcase-app-design.md](edge-showcase-app-design.md) §3.3 (the descriptor vocabulary),
[multi-credential-identity-linking-design.md](multi-credential-identity-linking-design.md) §3/§8.
**Contracts:** builds to #1/#8/#9/#11; **frozen-contract change: Contract #2 §2.5** (one new declared-read
class — the edit is staged **UNCOMMITTED** in `main`).

---

## For Andrew

**What it does, in two lines.** Five identity ops carry `[no-op-meta:]` exemptions because their submission
is a *client-side ceremony*, not a filled form. This design closes three of the five — with a
**script-declared derived read** (so no client ever re-derives a platform hash) and a **mint-and-reveal**
descriptor field (so a client mints the secret instead of asking a human to type a 64-hex string) — leaves
the two link-ceremony ops exempt with corrected reasons, and sequences their closure behind a real
two-device consumer.

**Two decisions want your eye.**

**(1) A second, KV-less Starlark pre-pass per op.** Inc 1 adds `derive_reads(op)` at the head of commit
step 4; its returned exact keys join the declared read set. I recommend it over the alternatives
(§8-A1/A2: a client-side `{sha256NanoID(…)}` template vocabulary; a Gateway-side derivation) because the
derivation is **package semantics, not platform semantics** — identity-domain's index keys are
`sha256NanoID("name:" + " ".join(name.lower().split()))`, and that normalization is the package's own.
Today the function is hand-ported into **four** independent submitters, two of them **PCG-128-seeded NanoID
derivations re-implemented in browser JavaScript** (`cmd/loftspace-app/web/app.js:966`,
`cmd/clinic-app/web/app.js:654`), on a dedup path where silent divergence is a live failure mode Standard §8
documents twice. The alternatives keep those ports alive; the pre-pass deletes them. Cost, stated honestly
after the adversarial pass corrected me: it is a compile+Init+Call of the *same* ~960-line module, not a
cheap call — so it requires a shared compiled-program cache to be affordable (§4.1, §8-R1).

**(2) Does the credential↔identity binding become a first-class link?** This is a genuine fork the
adversarial pass surfaced, and it is the one thing in this design that changes a shipped data model. Inc 2
(`UnlinkCredential` becomes descriptor-driven) needs one Protected row **per bound credential**; the bound
set today is a variable-length array inside an *encrypted* aspect (`packages/identity-domain/lenses.go:43-52`),
and the rule engine has no row-fanout (`internal/refractor/ruleengine/full/visitor.go:146`: "UNWIND is not
supported"). **My recommendation: emit `lnk.identity.<credentialId>.boundTo.identity.<ownerId>`** — which is
what Contract #1 requires of a relationship anyway (relationships are links, not `data` refs), and which
makes the per-credential row a plain `MATCH` fan-out needing no new engine primitive. **The trade-off is a
privacy surface:** the owner→credentials direction becomes enumerable in the plaintext link keyspace. The
credential→identity direction is *already* there via `vtx.credentialindex.<sha256(credKey)>`, so this adds
the reverse edge, not the correlation itself. The alternative — a platform array-fanout projection
primitive — is a larger, more speculative build with this as its only consumer. **Your call; Inc 2's build
is sequenced behind it** (§4.2).

**Frozen-contract change — Contract #2 §2.5, staged UNCOMMITTED in `main`.** One new row in the read-class
table — **(g) script-derived exact-key** — plus the paragraph defining `derive_reads`' contract
(deterministic, fail-closed stubs for the impure modules, Contract #1 key-grammar validated, weakest-wins
merge precedence, the `egressReads` exclusion re-checked over the merged set, counted in the 1000-key
ceiling). No existing class changes; every op whose DDL declares no `derive_reads` is byte-identical.
Affected consumers: the Processor (step 4 + the DDL cache), `lint-conventions`' read-posture classifier, and
the Edge predictability rule (which the change strengthens — §5).

**What ratification unlocks.** Inc 1 and Inc 3 are build-ready on your sign-off. Inc 2 is build-ready on
your sign-off **plus** decision (2). Inc 4 is designed through and deliberately **not** build-ready: it has
no live consumer (§4.4) — the dead-scaffolding test, not a gap.

---

## 1. Problem + intent

The Vertical Package Standard's **S1** says every op a human may trigger ships a full `OpMetaSpec`, so a
client can render a form and author a valid Contract #2 envelope **from the descriptor alone**, with no
per-op hardcoded knowledge. `lint-package-standard` makes that binding: the `s1Debt` baseline is empty
(`scripts/lint-package-standard.go:102`) and S1 holds over all 29 registered packages.

It holds because of an escape hatch. Standard §8 (Inc 5, `52711a5a`) planned seven identity-domain
descriptors and shipped **two**, because an adversarial pass proved five could not honour the promise. Those
five carry `[no-op-meta: <reason>]` notes today (`packages/identity-domain/permissions.go:49, 67, 85, 91,
97`). §8's amendment to S1 — "a second exempt class exists: a **client-side ceremony** op" — is still marked
*flagged for Andrew*.

The exemption is honest, and it is also load-bearing debt, because the hardcoding it sanctions has **already
been paid for four times**:

| Submitter | What it hardcodes |
|---|---|
| `cmd/lattice/identity/identity.go:32,278-303` | mint 32 random bytes → hex → sha256; `substrate.SHA256NanoID("email:"+e)` / `"phone:"+p` / `"name:"+n` probe keys; the contact normalization |
| `cmd/facet/credentials.go:150-160,244-277` | `mintLinkSecret()`; `substrate.SHA256NanoID(a2Key)` credentialindex; a hand-built two-op, two-actor envelope pair |
| `cmd/loftspace-app/web/app.js:945-1020` + `credentials_link.go:137-155` | `sha256Hex`; **`sha256NanoID` re-implemented in JS** — "byte-identical to `internal/substrate.SHA256NanoID` / the Starlark `crypto.sha256NanoID(s)` builtin (both seed a 128-bit PCG from the digest)"; the same contact normalization, again |
| `cmd/clinic-app/web/app.js:654-660,706-714` | the **same JS port again**, byte-for-byte |

The last two rows are the ones that matter. A **security-relevant key derivation** — the function that
decides whether a registration dedups against an existing person, and whether an unlinked credential can
ever be re-linked — now exists in Go (`internal/substrate/derive.go:69`), in Starlark
(`crypto.sha256NanoID`), and **twice** in browser JavaScript, with nothing but a comment holding the four in
agreement. Standard §8 records both live failure modes this class of divergence produces: a second
registration sharing a normalized name is **rejected with `RevisionConflict`** (`ddls.go:753-758`,
`commit_path.go:598-606` — `absentConditionedCreates` will not retry a create whose probe was never
declared), and a previously-unlinked credential can **never be re-linked** because the revive needs the
hydrated `credentialindex` revision (`ddls.go:552-565,1260`).

**Intent.** Stop treating "the client must compute something" as a reason to give up on the descriptor.
Split it into the two genuinely different cases and give each its own answer:

- **The client must compute something the *package* owns** (a hash, a normalization, an index key). That is
  not a client job at all — it is a missing declaration primitive, and the client should never see it.
  → **Inc 1**.
- **The client must compute something *only the client can*** (a secret the platform must never learn).
  That is a real client job, and it needs a small, fixed, fail-closed vocabulary so every client does it the
  same way instead of each inventing it. → **Inc 3**.

`UnlinkCredential`'s exemption is a third thing again — not a computation but a **missing row shape**
(§4.2).

## 2. What the census actually says

The five, re-grounded against HEAD (not against §8's summary):

| Op | Stated obstacle | What the code says | Answer |
|---|---|---|---|
| `CreateUnclaimedIdentity` | mints the claim secret; cannot declare its sha256-derived `identityindex` probes | both true. `ddls.go:715-728` derives three keys from `crypto.sha256NanoID` over package-normalized contacts | Inc 1 + Inc 3 |
| `RotateClaimKey` | mints the new claim secret | true, and the only obstacle — its reads are `{payload.targetIdentityKey}` + `.state`, plain templates | Inc 3 |
| `InitiateCredentialLink` | mints the link secret | true, and the only obstacle. But the *useful* half is what happens on the other device | Inc 3 mechanism, Inc 4 consumer |
| `CompleteCredentialLink` | submitted as a different actor than the client authenticated as; needs the derived `credentialindex` | true. `gateway.go:308-315,502-505` carves this op out of `resolveActor`, so `env.Actor` is the **raw** credential; `step3_auth_capability.go:540-546` requires `authContext.target == env.Actor`, and a client's `self` is `{target: me().identityKey}` — the **resolved** identity (`app.js:2087-2089`) | Inc 4 (designed, deferred) |
| `UnlinkCredential` | its one input is a key nothing projects | **half stale.** The client half is solved: the generic pane executor (`cmd/facet/pane.go`) reads Protected tables, a section may declare `dispatch:{targetColumn,targetType}` (`internal/pkgmgr/definition.go:189,632`; shipped in `packages/edge-manifest/panes.go:90,112`), and `paneRowOps` (`app.js:1407-1416`) offers matching ops with the row's key as `ctx.entityKey`. The **row** half is not: there is no per-credential row to dispatch from | Inc 2 (needs a data-model call) |

So: **three obstacles, not five ops** — a derived key, a minted secret, a raw-credential actor — plus one
missing row shape.

## 3. Reconciliation with the existing mental model

**"Didn't we already solve the derived-key problem with `optionalReads`?"** No. `optionalReads` fixed the
*fail-closed direction* of a read-before-create probe (Contract #2 §2.5 class (d)) — a declared key whose
absence is a legitimate branch. It says nothing about a submitter that **cannot express the key at all**.
Every one of the four census keys is already correctly filed as an `optionalRead` by the submitters that
manage to compute it; the gap is upstream of the required/optional split.

**"Doesn't `whoami` already hand the client its derived key?"** For exactly one key —
`internal/gateway/whoami.go:26` returns `credentialIndexKey`. That is a *precedent for the direction* (the
platform derives, the client consumes) and simultaneously the evidence that the general form is missing: one
bespoke field, one endpoint, one op, and nothing for the three `identityindex` probes.

**"Doesn't Contract #2 already reserve this?"** §2.5's *Future evolution* reserves **static** analysis of a
script's reads ("used to lint class-(b) debt and to derive the Edge per-op predictability flag"). Inc 1 is
the **dynamic** cousin and is not the same thing: an `identityindex` key depends on the payload's *value*,
so no static pass can produce it. The two compose — static analysis classifies, `derive_reads` computes —
and the contract paragraph says so.

**"Does this introduce new state?"** Inc 1 and Inc 3, no: `derive_reads` reads and writes nothing (a pure
function of the envelope), and the minted plaintext never leaves the client, which is the whole security
property (`permissions.go:67`: "Lattice only ever stored the hash, never the plaintext"). Inc 2 **does** —
one link per bound credential — and that is exactly the fork §4.2 puts to Andrew rather than deciding
quietly.

**"Does this contradict the design-of-record?"** It reinforces three. **P2** — nothing here writes Core KV
outside the Processor. **§2.5's read posture** — the declared-read norm exists so write-path execution is "a
pure function of `(op payload, declared+hydrated read-set)`"; a key that is a deterministic function of the
payload is the purest possible member of that set, and today it is the one class of declarable key the
posture cannot actually declare, which is why it leaks into class (b) whenever a submitter gets the
derivation wrong. **Contract #1's "relationships are links, not `data` refs"** — Inc 2's recommended shape
brings the credential binding back into conformance with a rule it currently sits outside of.

**"Isn't the ceremony vocabulary re-opening the door §8 closed?"** §8's rule is that a descriptor is a
**promise** a client can build a valid envelope from these fields, and that shipping an unhonourable one is
worse than the debt. Inc 3 keeps that rule and makes it enforceable in the other direction: a descriptor
carrying `ceremony` is a promise a client can only honour if it *implements* the ceremony, so a client that
does not understand the field **must not offer the op** (§4.3, fail-closed). The failure §8 feared — "a
hand-typed 64-hex string is *accepted*" — is impossible under Inc 3, because the hash field is never
rendered.

## 4. The shape

### 4.1 Inc 1 — script-derived reads (`derive_reads`)

**The primitive.** A DDL script may define a top-level function:

```python
def derive_reads(op):
    # op: {"operationType": str, "actor": str, "payload": dict}
    # returns {"reads": [key, ...], "optionalReads": [key, ...]}  (both optional)
    name = op["payload"].get("name")
    if not name:
        return {}
    normalized = " ".join(name.lower().split())
    return {"optionalReads": ["vtx.identityindex." + crypto.sha256NanoID("name:" + normalized)]}
```

**Where it runs.** At the head of **commit step 4 (Hydrate)** — after step 3 authorization
(`commit_path.go:232-282` runs `Authorize`, then `commitPipeline`, whose first stage is hydrate), before the
first Core KV GET. Step 4 sits inside the OCC retry loop (`commit_path.go:310,335`), so a *pure*
`derive_reads` recomputes the identical set on every attempt — which is why purity is enforced rather than
requested.

**Purity is enforced by fail-closed STUBS, not by unbinding.** `starlarksandbox.Execute` resolves every name
against the predeclared set at **compile** time (`internal/starlarksandbox/sandbox.go:110`; an unbound name
is "a compile-time `SandboxViolation`", `sandbox.go:99-104`), and the pre-pass compiles the **same module**
as step 5 — identity-domain's is one 960-line source referencing `kv.Links`/`kv.Read` at
`ddls.go:618,624,637,846`. Unbinding `kv` would therefore fail to compile the whole module and kill every op
on that DDL. So the pre-pass binds:

- a **`kv` stub** whose every builtin `fail()`s when *called*, naming `derive_reads` in the message — a
  derivation that reads state is not a derivation, it is a read, and it must be declared like one;
- a **`nanoid` stub** that fails identically. This is not optional hygiene: `nanoidModule(rid)` seeds a PCG
  from the requestId (`starlark_builtins.go:32-38`), so two independently-constructed modules for one op
  seed identically and a `derive_reads` calling `nanoid.new()` would hand step 5's first `nanoid.new()` the
  *same* id;
- **no mutation sink** — the return value is the entire output;
- the existing sandbox's withholding of wall-clock and randomness, inherited unchanged.

**Cost, and how it is made affordable.** `Budget.Wall` covers Init+Call and explicitly **not** compile
(`sandbox.go:26-29`), and `Init` runs the module's whole top level. A naive second `Execute` therefore pays
a second compile *and* a second Init of the same 960 lines, per op, per OCC retry. So Inc 1 **caches the
compiled `*starlark.Program` on the DDL cache entry** and both passes share it, and the pre-pass budget is
sized against the same Init the main pass pays — never "well under" it. The DDL cache entry also carries a
**`HasDeriveReads` flag** computed once at cache-refresh from a single parse: there is no way to detect the
entrypoint from a running `Execute` without paying for it (`sandbox.go:110,138,143` returns
`InvalidReturnShape` only *after* Init), so without the flag the "zero cost for the ~28 packages that
declare none" claim is false and its test cannot pass.

**What it may return.** A dict with `reads` and/or `optionalReads`, each a list of strings. Every entry is
validated against the Contract #1 key grammar (3-segment vertex, 4-segment aspect, 6-segment link) before it
joins the set; **anything else fails the operation closed**, with the derivation named. Merge rules, both
fail-closed and both forced by the adversarial pass:

- **Weakest wins.** `step4_hydrate.go:215-232` gives `reads` precedence and never demotes, so a derived
  `reads` entry colliding with an envelope-declared `optionalReads` key would silently harden it — and the
  script's dedup branch would fault `HydrationMiss` instead of taking the no-duplicate path, which is
  precisely the `CreateUnclaimedIdentity` shape. A key the envelope already declared keeps the envelope's
  (weaker) disposition.
- **The `egressReads` exclusion is re-checked over the merged set.** `ParseEnvelope` rejects a key in both
  `egressReads` and `reads`/`optionalReads` (`opwire.go:290-310`) because otherwise the hydration loop wins
  and caches the key as **plaintext**, silently demoting its egress disposition. `derive_reads` runs after
  parse, so the check must run again at step 4 and fault closed naming the derivation — otherwise the
  collision surfaces as an opaque step-6 rejection (`step6_validate.go:97-110`).

The merged total is checked against the 1000-key declared-read ceiling; a breach is a **runtime fault at
step 4**, not `EnvelopeMalformed` — the keys are not envelope-supplied, the same reasoning §2.5 already
applies to the live-read budget.

**Which script.** The DDL that owns the `operationType` — the resolution step 5 already performs. One op,
one derivation, no ambiguity.

**What it deletes.** `cmd/loftspace-app/web/app.js:945-1020` and `cmd/clinic-app/web/app.js:654-714` (both
`sha256NanoID` ports, the contact normalization, the probe-key builders), `cmd/lattice/identity/identity.go:278-303`,
and the `substrate.SHA256NanoID` call sites in `cmd/facet/credentials.go:274` and
`cmd/loftspace-app/credentials_link.go:152`. Each submitter simply stops declaring what it can no longer get
wrong. **Out of scope and explicitly left alone:** the object-id derivations at `cmd/loupe/objects.go:161,238`,
`cmd/loftspace-app/objects.go:429`, `cmd/loftspace-app/objects_crypto.go:79` — they derive object ids, not
declared read keys, and they carry the `// derived-key:` annotation G2 requires (§6).

**What it does not change.** An op whose owning DDL has `HasDeriveReads == false` behaves byte-identically —
no extra invocation, no extra cost.

### 4.2 Inc 2 — `UnlinkCredential` becomes descriptor-driven

**The client half is already solved; the row half is not.** The adversarial pass confirmed every client-side
assumption and refuted the row-side one:

*Confirmed.* The op's payload field and the `self` authContext target come from **different** values —
`resolveTargetKey` matches by vertex type over `[ctx.entityKey, …]` (`app.js:2148-2150`) while
`buildAuthContext("self")` returns `{target: me().identityKey}` (`app.js:2087-2089`) — so a pane-row
dispatch fills `credentialActorKey` from the row and the envelope target from the session. `PaneSpec` with
`dispatch:{targetColumn,targetType}` is installable and shipped (`internal/pkgmgr/definition.go:189,632`;
`packages/edge-manifest/panes.go:90,112`), projected over `holdsRole → offeredTo`. The pane executor already
enforces `appsession.ViaCookie` (`pane.go:327`) and sets `lattice.actor_id` matching the credentials lens's
`authz_anchors` shape (`pane.go:215`; `lenses.go:110-117`).

*Refuted.* There is no per-credential row and one cannot be projected today. The bound set is a
variable-length array **inside the encrypted** `credentialBinding` aspect; `lenses.go:43-52` says so
explicitly ("a bound-credential list is a variable-length array that only exists inside the ciphertext …
rather than a second lens per entry"), `SecureColumns` decrypt happens at projection into a *column*
(`internal/pkgmgr/definition.go:889-895`) so the graph engine never sees the elements, the engine has no
fanout (`internal/refractor/ruleengine/full/visitor.go:146`: "UNWIND is not supported"), and the pane
executor's row model is one map per SQL row (`pane.go:230-248`).

**Recommended shape (Andrew's call — For-Andrew decision (2)): make the binding a link.** Emit
`lnk.identity.<credentialId>.boundTo.identity.<ownerId>` on the paths that bind (`CompleteCredentialLink`,
`ClaimIdentity`) and tombstone it on `UnlinkCredential`. Then the per-credential Protected lens is a plain
`MATCH` fan-out — one row per link, columns `identity_key`, `credential_actor_key`, `bound_at`, the same
`authz_anchor` RLS shape the existing lens uses — with **no new engine primitive**. It also brings the
binding into conformance with Contract #1's "relationships are links, not `data` refs", which the array
currently sits outside of, and the direction reads correctly under §1.1 (the later-arriving credential is
the source: *credential boundTo identity*).

*The trade-off, stated plainly:* the owner→credentials direction becomes enumerable in the **plaintext**
link keyspace. The credential→identity direction is already there (`vtx.credentialindex.<sha256(credKey)>`
plus its `indexes` link), so this adds the reverse edge rather than the correlation itself — but it is a
privacy-surface change on the identity plane, which is why it is flagged rather than assumed. The
alternative is a platform **array-fanout projection primitive**; that is a larger, more speculative build
whose only consumer would be this row, which is the dead-scaffolding test failing.

**The rest of Inc 2, once the shape is settled.** A `signInMethods` `PaneSpec` section over that lens with
`dispatch: {targetColumn: "credential_actor_key", targetType: "identity"}`, and a full `OpMetaSpec` for
`UnlinkCredential` — `AuthContext: "self"`, `TargetField: "credentialActorKey"`, `TargetType: "identity"`,
`Reads: ["{actor}", "{actor}.state"]`, `OptionalReads: ["{actor}.credentialBinding"]` (the envelope
`cmd/facet/credentials.go:369-378` already builds by hand). The "last credential cannot be removed" rule
stays where it belongs, in the script.

**Inc 2 depends on Inc 1 after all.** `ddls.go:1334` tombstones
`vtx.credentialindex.<crypto.sha256NanoID(credential_actor_key)>` — a key neither the shipped envelope nor
the proposed descriptor declares, so it commits unconditioned today (`applyHydratedRevisions` skips
un-hydrated keys, `commit_path.go:584-586`). That is a class-(g) key on this very op: "zero new vocabulary"
is true of the *client*, not of the op.

**One paragraph the adversarial pass deleted.** An earlier draft warned that `resolveTargetKey` falls back
to `me().identityKey` for an unresolvable `identity` target. It does not: `app.js:2151` is
`return selfAnchorKey(want)`, and `selfAnchors` carries no `identity` entry
(`packages/edge-manifest/lenses.go:524-529`), so the fallback is `undefined` for every identity target. The
same accident is what stops `crossHatMismatch` degrading the op (`app.js:1908-1918`). Both behaviours are
**load-bearing and accidental**, so Inc 2 pins them with a test rather than relying on them silently.

**Silent obligations of the handlers Inc 2 could replace** (a checklist, not a drop-in claim):
`/api/credentials` + `/api/credentials/unlink` also carry (a) the `ViaCookie` gate — **already carried** by
`pane.go:327`, so this one is discharged; (b) the error-vs-empty distinction that stops a broken read model
rendering as the affirmative "no sign-in methods" (`app.js:1640-1646`); (c)
`refreshCredentialsUntilChanged`'s bounded poll over async CDC projection lag. Inc 2 ships the pane +
descriptor and removes the bespoke handlers only once (b) and (c) are demonstrated on the pane path, or
leaves them and files the removal. Removing them is not what this increment is for.

### 4.3 Inc 3 — the mint-and-reveal descriptor

**New field on `OpMetaSpec`** (`internal/pkgmgr/definition.go`), emitting a `.ceremony` aspect exactly as
`Presentation`/`Dispatch` emit theirs:

```go
// OpCeremonySpec declares the one thing a descriptor-driven client must DO
// rather than ask: mint a secret the platform must never learn, submit only
// its hash, and show the plaintext to the person once.
type OpCeremonySpec struct {
    // MintedSecretHashField names the InputSchema field that carries the
    // lowercase-hex sha256 of a client-minted 256-bit secret. The client
    // mints, hashes, fills — and never renders the field.
    MintedSecretHashField string

    // RevealTitle / RevealHelp are the copy for the ONE-TIME post-acceptance
    // display of the plaintext. The client shows it once and never persists it.
    RevealTitle string
    RevealHelp  string
}
```

**The client contract** (three rules, all fail-closed):

1. A client that does not implement `ceremony` **must not offer** an op whose descriptor carries one. It
   degrades exactly as it already degrades an unresolvable `TargetType` (`app.js:966-978`). Absence of
   support denies; it never falls back to rendering the hash field as a text input — precisely the
   accepted-garbage failure §8 refuses to ship.
2. `MintedSecretHashField` is **removed from the rendered form** and filled by the client: 32 bytes from the
   platform CSPRNG (`crypto.getRandomValues` in a browser, `crypto/rand` in Go), hex-encoded as the
   plaintext, `sha256` hex of that plaintext into the field. The plaintext is never sent, never logged,
   never stored.
3. On `accepted`, the client displays the plaintext once under `RevealTitle`/`RevealHelp`, then drops it. On
   any non-accepted outcome it drops it **without** display — a secret for a write that did not land is not
   a secret anybody should be handed.

**Descriptors this ships.** `CreateUnclaimedIdentity` (with Inc 1 supplying its three `identityindex`
probes) and `RotateClaimKey`. Both are `AuthContext: "standing"`: verified — both are `Scope:"any"` grants
to `frontOfHouse`/`backOfHouse`/`operator` (`permissions.go:47-51,65-69`), with no relationship to a target,
and `"standing"` is a real value (`definition.go:559-568`, `app.js:2094`). `RotateClaimKey` additionally
takes `TargetField: "targetIdentityKey"`, `TargetType: "identity"`. `InitiateCredentialLink` uses the same
mechanism but ships with Inc 4, for the reason in §4.4.

### 4.4 Inc 4 — the credential-scoped actor and the paired code (designed, build DEFERRED)

**The mechanism, resolved.** Two additions close the link pair:

- **`OpDispatchSpec.AuthContext = "credential"`** — a fifth value meaning *populate `target` with your
  **raw** authenticated actor, not your resolved business identity*. It exists because the Gateway's
  `rawCredentialCarveOut` (`gateway.go:308-315,502-505`) stamps `env.Actor` with the raw credential for
  exactly this op, while `scope=self` requires `authContext.target == env.Actor`
  (`step3_auth_capability.go:540-546`) and the client's `self` is `{target: me().identityKey}`. Client-side
  it resolves from `whoami.actorId` (`internal/gateway/whoami.go:24`), which the edge manifest's
  `manifest.me` row would need to carry alongside the resolved key.
- **A paired code.** `OpCeremonySpec` gains `RevealWith []string` (the fields concatenated into the revealed
  code — for `InitiateCredentialLink`, the arming identity's key plus the minted secret) and
  `PairedCodeField string` on the consuming op (one input the client splits back into two payload fields).
  Without it, `CompleteCredentialLink`'s descriptor asks a person to hand-type a `vtx.identity.<NanoID>`
  **and** a 64-hex secret — the same unhonourable promise as before, in two fields instead of one. With it,
  a person reads one code off one device and types it into the other, which is the only shape the two-device
  flow has ever had as a product.

**Why the build is deferred (the dead-scaffolding test).** *Does this increment realize value before its
consumer exists?* No — verified, not assumed. There is **no real two-device link flow in the corpus**: Facet
runs both halves server-side against a *fabricated* throwaway device credential, and that path is dev-only
(`cmd/facet/credentials.go:175-178`, `devSigner == nil` → "linking is disabled (FACET_DEV_AUTH not set)"),
so in the production verify-only posture it does not exist at all; LoftSpace's browser-direct variant mints
its own device key the same way (`credentials_link.go:137-155`); `cmd/clinic-app` has no link path. Until a
client signs in on a genuinely second device and completes a link with a code a human carried, `credential`
+ `RevealWith` + `PairedCodeField` are three pieces of vocabulary with no caller, and
`InitiateCredentialLink`'s descriptor arms a secret nothing consumes.

**So the output is: the design is ratified and shelved, not started.** `InitiateCredentialLink` and
`CompleteCredentialLink` keep their `[no-op-meta:]` exemptions, Notes corrected to name this design and the
specific missing consumer (§6-G1). Residual after Inc 1–3: **2** stated exemptions, down from 5.

## 5. Contract surface

**Contract #2 §2.5 — change (staged UNCOMMITTED in `main`).** One row added to the read-class table:

> **(g) script-derived exact-key** — a key that is a deterministic function of the op payload under
> *package* semantics (a normalized-contact index hash, a credential index), so the submitter cannot express
> it. Declared by the owning DDL's `derive_reads(op)` and resolved Processor-side at the head of step 4,
> before hydration. **Folds into (a)/(d)** — declared, snapshotted, Edge-predictable — and is counted in the
> declared-read ceiling.

plus the defining paragraph: determinism (impure modules bound as fail-closed stubs, never unbound — the
sandbox resolves globals at compile time); Contract #1 key-grammar validation with a fail-closed reject;
**weakest-wins** merge precedence against an envelope-declared key; the `egressReads` mutual-exclusion check
re-run over the merged set; ceiling accounting as a step-4 runtime fault. Plus one sentence in *Future
evolution* noting that static classification and `derive_reads` compose rather than compete.

**Contract #1** — unchanged as a *contract*; the derivation must produce Contract #1 keys and is validated
against the grammar. Inc 2's recommended `boundTo` link is a package data-model change **into**
conformance with §1.1, not a contract change. **Contract #8 §8.1** — unchanged; the `[no-op-meta:]` marker
lives in a permission Note, whose *shape* §6 tightens without touching the contract. **Contract #9 / #11** —
unchanged; Inc 4 names the existing raw-credential carve-out rather than moving it. **Contract #6** —
unchanged.

**Edge predictability.** §2.5's rule is that "an op is locally predictable iff all its reads are declared
and ⊆ the local mirror". A class-(g) key is declared by the *script*, and the Edge's premise is that it can
run that script — so the same pre-pass yields the same keys. This is strictly better than the status quo,
under which a derived key is either undeclared (class-(b), not predictable) or declared by a hand-ported
derivation that may disagree. **No Edge code changes now**: there is no `starlark` reference anywhere under
`cmd/facet` or `internal/edge`, and no predictability / mirror-coverage gate exists in code yet, so local
prediction exercises neither path today.

## 6. The gates (they ship with the design, not after it)

A convention that only lives in normative text binds nobody. Two gates, both default-deny with a cheap
declaration escape, mirroring the shipped `# read-posture: (a|c|d|e|f)` idiom:

**G1 — `[no-op-meta:]` reasons become a closed vocabulary** (`scripts/lint-package-standard.go`). The marker
today accepts arbitrary prose (`exemptionMarker = "[no-op-meta:"`, line 80), so an exemption cannot be
re-checked and cannot expire. It becomes `[no-op-meta: <code> — <prose>]` with `<code>` from a fixed set —
`engine-op`, `reply-op`, `lifecycle-op`, `client-agent-op`, `ceremony-mint`, `raw-credential-actor`,
`paired-code` — and an unrecognized code fails the gate. Each exemption becomes *machine-attributable to a
named missing mechanism*, so when Inc 3 lands, every surviving `ceremony-mint` exemption is a
gate-enforced question rather than prose nobody re-reads. **In scope for the same increment:** rewriting all
six shipped notes — identity-domain's five (`permissions.go:49,67,85,91,97`) **and**
`packages/control-authz/permissions.go:78` (a client sync-manager verb, which is why `client-agent-op`
exists in the set), plus `packages/identity-domain/package_test.go:235`, which asserts on the raw marker.

**G2 — no client re-derives a *declared-read* key** (`scripts/lint-conventions.go`). A default-deny scan
over `cmd/**` for a `substrate.SHA256NanoID(` call site outside `internal/`, and for a locally-defined
`sha256NanoID` in JS, with one escape: an explicit `// derived-key: <reason>` annotation on the line.
Post-Inc-1 the identityindex/credentialindex sites are gone, so the gate ships **blocking, not warn-first**
(a warn-first gate over a clean tree is the fingers-crossed state the fire exists to end), with the six
object-id sites (§4.1) carrying annotations. Two scope facts the adversarial pass established:
`lint-conventions` discovers files via `git ls-files "*.go"` and hard-filters non-`.go`
(`scripts/lint-conventions.go:1307-1310`, `:511`), and its annotation parser is Go-comment shaped
(`:388`) — so **the JS half ships as its own `scripts/lint-web.go`**, wired into the same
`make lint-conventions` target, rather than as a filter change. (There is no `apps/` directory; `cmd/**` is
the whole surface.)

Neither gate needs semantic analysis; both make the author *declare*, which is cheap, and forgetting fails
closed.

## 7. Migration, compatibility, test strategy

**Compatibility.** Every increment is additive. A DDL with `HasDeriveReads == false` is unchanged. An op
meta with no `ceremony` is unchanged. A client that ignores `ceremony` offers strictly fewer ops than
before — never a broken one.

**Migration order matters in two places.** Inc 1's submitter cleanup (deleting the four hand-ported
derivations) must land **after** the `derive_reads` it replaces is installed, and package installs are
versioned (a same-version edit no-ops), so the identity-domain version bump is part of Inc 1. Inc 2's
`boundTo` links must be **backfilled** for already-bound credentials before the pane section is offered, or
an existing user sees an empty sign-in-methods list — a one-shot backfill op over the existing
`credentialBinding` arrays, and the pane section ships only once it has run.

**Tests.**

- *Inc 1, unit* (`internal/processor`, `internal/starlarksandbox`): a `derive_reads` returning a well-formed
  key joins the hydrated set; a malformed key fails the op closed with the derivation named; a `kv` or
  `nanoid` call inside the pre-pass fails closed with that message (the **stub** path, since unbinding is
  not available); a derivation exceeding its wall budget aborts; derived + declared keys summing over 1000
  fault at step 4 rather than at parse; a derived `reads` entry colliding with an envelope `optionalReads`
  key keeps the **weaker** disposition; a derived key colliding with `egressReads` faults at step 4 naming
  the derivation, not at step 6; an op with `HasDeriveReads == false` performs **zero** extra invocations
  (asserted against the cache flag, so the opt-in claim is pinned, not stated); the compiled program is
  shared, asserted by a compile-count that does not rise with a second pass.
- *Inc 1, equivalence* (`packages/identity-domain`): the three `identityindex` keys `derive_reads` returns
  are byte-identical to the ones `ddls.go:715-728` computes in the main script and to the ones
  `cmd/lattice/identity` computes today — one table-driven vector set run against all three, so the
  divergence this design exists to kill is itself pinned.
- *Inc 1, e2e*: the `dedup_test.go` scenario — a second registration sharing a normalized name — passes with
  a submitter that declares **nothing**, proving the `RevisionConflict` failure mode §1 names is closed at
  the platform layer rather than by a well-behaved client.
- *Inc 2*: a lens test that one row per `boundTo` link is projected and RLS-confines to the owner; a
  pane-executor test that the section compiles; client tests pinning the two accidental-but-load-bearing
  behaviours (`resolveTargetKey` returns the row key and the `selfAnchorKey` fallback is `undefined` for
  `identity`; `crossHatMismatch` does not degrade the op); a backfill test that a pre-existing
  `credentialBinding` array yields the same row set; the existing `credentials_test.go` unlink assertions
  re-pointed at the descriptor path.
- *Inc 3*: the minted-hash field is absent from the rendered form and present in the submitted payload; a
  non-accepted reply reveals nothing; a client with ceremony support disabled **degrades** the op rather
  than rendering it (the fail-closed direction, asserted in the direction that can regress).
- *Gates*: `lint-package-standard` rejects an unrecognized exemption code and passes all six rewritten
  notes; `lint-conventions` rejects an unannotated `SHA256NanoID` in `cmd/**`; `lint-web` rejects a
  locally-defined `sha256NanoID` in JS.

## 8. Risks and alternatives

**A1 — a client-side `{sha256NanoID(...)}` template vocabulary in `OpDispatchSpec.Reads`.** *Rejected.* It
is the smaller platform change and the wrong one: it requires the derivation **and** identity-domain's
contact normalization (`" ".join(name.lower().split())`, E.164, lowercase-email) in every client language —
the status quo with a schema around it. This is the "a missing primitive is debt to add, not a workaround to
enshrine in a contract" case, and it fails twice over: the two JS ports survive, and the normalization rules
— *package* semantics, not platform ones — end up expressed in a platform template grammar that would have
to grow a mini expression language (`lower`, `trim`, `collapse`, `e164`) to hold them. *Could a variant beat
the recommendation?* Only if the derivations were platform-uniform. They are not; §1's table is three
different normalizations.

**A2 — the Gateway derives (generalize `whoami.credentialIndexKey`).** *Rejected.* It puts package semantics
in the Gateway, covers only Gateway-submitted ops — not Loom/Weaver/bridge-dispatched ones, not the CLI's own
envelope construction — and leaves the derivation two hops from the script that must agree with it.

**A3 — leave all five exempt and de-duplicate the client code into a shared Go/JS helper.** *Rejected, but
the closest call.* It fixes the triplication for Go submitters, does nothing for the browser (a shared JS
helper is still a second implementation of a Go function), does nothing for S1, and leaves the exemption a
prose amnesty with nothing for G2 to point at. Its one real merit — smallest thing that addresses the
observed pain — is preserved inside Inc 1, which deletes the same code *and* removes the reason it existed.

**A4 — make `derive_reads` a declarative DDL field rather than a script function.** *Rejected.* It
re-invents an expression language for the same reason A1 needs one, and it splits the derivation away from
the script that must produce the identical key — exactly the divergence hazard. A Starlark function calling
the same `crypto.sha256NanoID` builtin the main script calls is the only shape where "these two keys agree"
is true by construction rather than by test.

**A5 — a platform array-fanout projection primitive instead of Inc 2's `boundTo` link.** *Rejected as the
recommendation, surfaced as the fork's other branch.* It is a substantially larger engine change (the full
visitor explicitly refuses `UNWIND`) whose only known consumer is this one row, and it leaves the credential
binding modelled as a `data` array where Contract #1 §1.1 says a relationship is a link. If Andrew prefers
not to add the plaintext edge, this is the branch to take — and then Inc 2 waits on a driver, rather than
Inc 2 being built on a worse shape.

**R1 — the pre-pass is a compile+Init, not a cheap call.** Stated honestly after the adversarial pass:
`Budget.Wall` excludes compile (`sandbox.go:26-29`) and `Init` runs the module's whole top level, once per
op and again per OCC retry. The bounding constraints, not a headline: the shared compiled-program cache, the
`HasDeriveReads` flag that makes it zero-cost for every op without one, and a pre-pass budget sized against
the same Init the main pass pays. The ops that will have one are index/dedup ops — low-volume registration
and linking paths, not the hot write path.

**R2 — "the script names its own reads" as a new seam.** The read declaration is not an authorization
boundary: the adversarial pass confirmed `step3_auth_capability.go` never reads `ContextHint`, and a
submitter's declared keys are never returned to the submitter — only the trusted, versioned, reviewable
package script sees the hydrated state. What the declaration *is* is a determinism/cost boundary
(exact-key, bounded, no scans), and every one of those properties is preserved by construction. **The
earlier draft over-claimed this as a "narrowing" of trust; it is not** — the derived channel is *additive*,
the submitter's channel is unchanged, and submitters stop declaring by convention (Inc 1's deletions) and by
G2's call-site ban, not by enforcement of the declaration itself.

**R3 — a ceremony descriptor a client half-implements.** Handled by rule 1 of §4.3 (no support ⇒ no offer)
and pinned by the Inc 3 degrade test. The default direction is deny.

**R4 — Inc 2's handler removal drags in obligations.** Explicitly scoped: one of the three is already
discharged by `pane.go:327`, the other two are the precondition, and none is asserted as "the FE contract is
unchanged".

## 9. Decomposition for the Steward

| Inc | Scope | Size | Depends on |
|---|---|---|---|
| **1** | `derive_reads` pre-pass (step 4) + shared compiled-program cache + `HasDeriveReads` on the DDL cache + fail-closed `kv`/`nanoid` stubs + merge/egress rules + Contract #2 §2.5 (Andrew commits) + identity-domain declares its four derived keys (version bump) + delete the four hand-ported derivations + **G2** (incl. `scripts/lint-web.go`) | **M–L** | ratification |
| **2** | `boundTo` link emit/tombstone + one-shot backfill + per-credential Protected lens + `signInMethods` pane section + `UnlinkCredential` descriptor; exemption removed | **M** | ratification **+ For-Andrew decision (2)** + Inc 1 (its own class-(g) key) |
| **3** | `OpCeremonySpec` + Facet client ceremony support + `CreateUnclaimedIdentity` / `RotateClaimKey` descriptors + **G1** (incl. the six note rewrites + `package_test.go`); two exemptions removed | M | Inc 1 (Create's probes) |
| **4** | `credential` authContext + `RevealWith`/`PairedCodeField` + the two link descriptors | M | **a real two-device link consumer** — do not start (§4.4) |

Build order: **1 → 3**, with **2** after decision (2) and Inc 1. Inc 1 grew from M to M–L on the adversarial
pass (the compile-cache and cache-flag work is not optional); Inc 2 grew from S–M to M and lost its
independence. Each increment remains independently shippable and green.

## 10. Adversarial pass (run this fire — findings folded)

A full adversarial review was run against the draft with instructions to refute rather than confirm. It
returned **2 blockers, 7 major and 6 minor findings**, and confirmed 12 claims. Everything is folded above;
the material corrections, so a reader can see what changed and why:

- **BLOCKER — unbinding `kv` cannot work.** Globals resolve at compile time and the pre-pass compiles the
  same module the main pass does, so an unbound `kv` fails to compile identity-domain's whole DDL. → §4.1
  now specifies **fail-closed stubs**, not unbinding.
- **BLOCKER — the per-credential lens is not expressible.** Variable-length array inside an encrypted
  aspect; no `UNWIND` in the engine; the pane row model is one map per SQL row. → §4.2 rewritten around the
  `boundTo` link, and the choice escalated to Andrew as decision (2) rather than assumed.
- **MAJOR** — a script's `derive_reads` cannot be detected without running it (→ `HasDeriveReads` on the DDL
  cache); the wall budget excludes compile and `Init` re-runs the whole module (→ shared compiled-program
  cache, budget sized against Init); merging into `reads` bypasses the `egressReads` mutual-exclusion check
  (→ re-checked at step 4); a **fourth** JS port exists in `cmd/clinic-app` and six object-id
  `SHA256NanoID` sites would trip G2 (→ both scoped); `lint-conventions` is Go-only (→ the JS half becomes
  `scripts/lint-web.go`); G1's vocabulary orphaned `control-authz`'s exemption (→ `client-agent-op` added);
  R2's trust-narrowing argument was false (→ restated).
- **MINOR** — the `resolveTargetKey` fallback hazard the draft cited does not exist at HEAD (→ paragraph
  replaced, and the accidental behaviour pinned by test); `nanoid` must be stubbed too (shared PCG seed);
  merge precedence must be weakest-wins; there is no `apps/` directory; `UnlinkCredential` has its own
  class-(g) key; three citations corrected.
- **Confirmed under attack:** step 4 is after step 3 auth; no OCC/replay divergence; no existence-oracle
  regression; nothing authorizes on `contextHint.reads`; the payload field and the `self` target come from
  different values (so Inc 2's client half works); the pane executor's session gate and RLS are as assumed
  and `PaneSpec` dispatch is shipped; both Inc 3 ops are standing role grants; the Gateway carve-out and
  step-3 self check are exactly as described; Facet's link is dev-only and no two-device consumer exists,
  so Inc 4's deferral is correct; no Edge Starlark exists; `s1Debt` is empty.

**No deferred gate remains** — this design self-flags no further pre-build review.
