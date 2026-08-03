# Client-ceremony ops become descriptor-driven — script-derived reads, a mint-and-reveal descriptor, and the credential-scoped actor

**Status: ✅ Andrew-ratified (2026-08-02).** Drafted 2026-08-01 (Winston, Designer fire); adversarial pass
run and folded the same fire (§10).

> **Ratification decisions (Andrew, 2026-08-02):**
> 1. **Design ratified.** Inc 1 and Inc 3 are build-ready. Inc 4 stays designed-not-built — no live
>    consumer (§4.4).
> 2. **Decision (2) — the credential↔identity binding becomes a first-class link.** Emit
>    `lnk.identity.<credentialId>.boundTo.identity.<ownerId>`, which is what Contract #1 requires of a
>    relationship anyway, and which makes the per-credential Protected row a plain `MATCH` fan-out needing
>    no new engine primitive. Inc 2 is unblocked. *Road not taken:* a platform array-fanout projection
>    primitive — a larger speculative build whose only consumer is this row.
>    **Recorded cost, corrected at ratification:** the draft's "adds the reverse edge, not the correlation
>    itself" holds for a *targeted lookup* and not for *enumeration*. `credentialindex` answers "which
>    owner?" only to a caller who already holds the credential key; the reverse edge lets a caller walking
>    the link keyspace enumerate an owner's whole credential set knowing nothing first. Accepted because
>    reaching it requires **substrate access** — the same threat model under which the keyed
>    identity-index HMAC row is already `🗄️ shelved (revive: production threat model)`. It joins that
>    shelf's accepted exposure; it does not open a new class, and it is not to be re-litigated per-fire.
> 3. **Inc 1 carries priority: the contract landed ahead of the build.** Contract #2 §2.5 is **already
>    committed** — Andrew committed it as `4965b28a` *"(doc) contract updates - ratified"* (2026-08-01),
>    not staged-uncommitted as this doc first recorded. Nothing implements it: `derive_reads` appears
>    nowhere in `internal/`, so the contract currently documents a step-4 mechanism the Processor does not
>    run, and a package author following the contract would get it **silently ignored** — landing in the
>    two failure modes §1 catalogues. No package has adopted it yet (verified at ratification), so the
>    window is clean; Inc 1 closes it.
> 4. **"Wear the other hat" — this item owns its vertical-lane tails; the consumer row is deleted.**
>    The verticals.md row *"Five identity ceremony ops stay undiscoverable"* is **removed**, not left
>    blocked: its whole scope is the client-side consumer migration this design already carries
>    (§9 — the four hand-ported derivations, Facet ceremony support, the `signInMethods` pane section).
>    Those are `cmd/**` FE/app tails of a Lattice item, and per `agents/steward/SKILL.md` §2 the Lattice
>    Steward **builds them in this item, in the Lattice lane** — invoking `owner`/`fe-engineer` against
>    the vertical app — rather than filing them across boards. §9 names the lane tails explicitly so the
>    routing is not re-derived per fire.

**Backlog row:** lattice.md *Component maintenance* — "[Pkgmgr] `OpMetaSpec` has no vocabulary for a
client-mint-and-reveal-secret ceremony". *(Consumer row on verticals.md removed at ratification —
decision 4.)*
**Extends:** [vertical-package-standard.md](vertical-package-standard.md) §2 S1 + §8 (Inc 5 build note),
[facet-discovery-restoration-design.md](facet-discovery-restoration-design.md) (the generic pane executor),
[edge-showcase-app-design.md](edge-showcase-app-design.md) §3.3 (the descriptor vocabulary),
[multi-credential-identity-linking-design.md](multi-credential-identity-linking-design.md) §3/§8.
**Contracts:** builds to #1/#8/#9/#11; **frozen-contract change: Contract #2 §2.5** (one new declared-read
class — **committed** `4965b28a`, 2026-08-01; see decision 3).

---

## For Andrew

**What it does, in two lines.** Five identity ops carry `[no-op-meta:]` exemptions because their submission
is a *client-side ceremony*, not a filled form. This design closes three of the five — with a
**script-declared derived read** (so no client ever re-derives a platform hash) and a **mint-and-reveal**
descriptor field (so a client mints the secret instead of asking a human to type a 64-hex string) — leaves
the two link-ceremony ops exempt with corrected reasons, and sequences their closure behind a real
two-device consumer.

**Two decisions wanted Andrew's eye. Both are decided (2026-08-02) — see the banner; kept here for the
reasoning that produced them.**

**(1) A second, KV-less Starlark pre-pass per op. RATIFIED.** Inc 1 adds `derive_reads(op)` at the head of commit
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

**(2) Does the credential↔identity binding become a first-class link? DECIDED: yes — emit the link
(Andrew, 2026-08-02).** This was a genuine fork the
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
primitive — is a larger, more speculative build with this as its only consumer.

*Corrected at ratification:* "adds the reverse edge, not the correlation itself" is true of a **targeted
lookup** and false of **enumeration** — `credentialindex` requires the caller to already hold the credential
key, whereas the reverse edge lets a link-keyspace walk enumerate an owner's whole credential set from
nothing. The cost is accepted, not absent: reaching it needs **substrate access**, the same threat model
that already shelved the keyed identity-index HMAC row. Banner decision 2 records it as a known cost on
that shelf. **Inc 2 is unblocked** (§4.2).

**Frozen-contract change — Contract #2 §2.5, COMMITTED (`4965b28a`, 2026-08-01).** One new row in the read-class
table — **(g) script-derived exact-key** — plus the paragraph defining `derive_reads`' contract
(deterministic, fail-closed stubs for the impure modules, Contract #1 key-grammar validated, weakest-wins
merge precedence, the `egressReads` exclusion re-checked over the merged set, counted in the 1000-key
ceiling). No existing class changes; every op whose DDL declares no `derive_reads` is byte-identical.
Affected consumers: the Processor (step 4 + the DDL cache), `lint-conventions`' read-posture classifier, and
the Edge predictability rule (which the change strengthens — §5).

**What ratification unlocked.** Inc 1, Inc 2, and Inc 3 are all build-ready (Inc 2 by decision (2)); build
order **1 → 3**, with **2** after Inc 1 (§9). Inc 4 is designed through and deliberately **not**
build-ready: it has no live consumer (§4.4) — the dead-scaffolding test, not a gap.

**Inc 1 is the one to start**, and not only because everything sequences behind it: Contract #2 §2.5 is
already committed and **nothing implements it**, so the contract presently describes a step-4 mechanism the
Processor does not run (banner decision 3). That is a fail-open gap against a frozen contract, and Inc 1 is
what closes it.

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
    # op is a struct: op.operationType, op.actor, op.payload (also a struct --
    # attribute access, not subscript; see `deriveReadsOpValue`)
    # returns {"reads": [key, ...], "optionalReads": [key, ...]}  (both optional)
    name = getattr(op.payload, "name", None)
    if name == None or type(name) != type("") or len(name.strip()) == 0:
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

**Shape — RATIFIED (Andrew, 2026-08-02; For-Andrew decision (2)): make the binding a link.** Emit
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

Build order: **1 → 3**, with **2** after Inc 1 (decision (2) is ratified). Inc 1 grew from M to M–L on the
adversarial pass (the compile-cache and cache-flag work is not optional); Inc 2 grew from S–M to M and lost
its independence. Each increment remains independently shippable and green.

**Lane routing — this item owns its `cmd/**` tails; do not file them to `verticals.md`.** Several
increments end in vertical-app work rather than `internal/*`. Per `agents/steward/SKILL.md` §2 ("wear the
other hat"), the **Lattice** Steward builds these inside this item, invoking `owner` / `fe-engineer`
against the vertical app exactly as it does for its own lane. They are the application of a ratified
pattern, not new design, so none of them routes back through the Designer. Named here so the routing is
settled once:

| Inc | Vertical-lane tail (`cmd/**`) | Hat |
|---|---|---|
| **1** | delete the two browser-JS `sha256NanoID` PCG-128 ports — `cmd/loftspace-app/web/app.js:945-1020` (+ `credentials_link.go:137-155`) and `cmd/clinic-app/web/app.js:654-660,706-714`; plus `cmd/facet/credentials.go:150-160,244-277`. (`cmd/lattice/identity/identity.go:32,278-303` is the CLI — already Lattice's own.) | `fe-engineer` ×2, `owner` ×1 |
| **2** | the `signInMethods` pane section over the new per-credential Protected lens | `fe-engineer` |
| **3** | Facet client ceremony support (the mint-and-reveal executor) — `cmd/facet` | `owner` |

The verticals.md consumer row *"Five identity ceremony ops stay undiscoverable"* was **deleted at
ratification** (banner decision 4): its entire scope is the table above, and a blocked row on the other
board would only re-import the ping-pong this rule exists to kill.

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

## 11. Increment 1a fire brief (build note, 2026-08-03)

Inc 1 is §9's **M–L** row and does not fit one fire. It splits at a real seam: the **platform primitive**
(`internal/*`, no package or client change) and its **adoption** (identity-domain's declaration, the four
hand-ported deletions, the two gates). This fire builds **1a**, the primitive.

**Scope sentence (this fire).** The `derive_reads(op)` pre-pass at the head of step 4 — shared compiled
program, `HasDeriveReads`, fail-closed `kv`/`nanoid` stubs, key-grammar validation, weakest-wins merge, the
`egressReads` re-check, and the ceiling accounting — implemented exactly to Contract #2 §2.5 class (g), with
its unit tests. No package declares one yet; no submitter changes.

**Scope-diff gate against §9's Inc 1 row, item by item.** `derive_reads` pre-pass ✓ · shared
compiled-program cache ✓ · `HasDeriveReads` ✓ · `kv`/`nanoid` stubs ✓ · merge/egress rules ✓ ·
Contract #2 §2.5 — already committed (`4965b28a`), nothing to prepare · **1b:** identity-domain declares its
four derived keys (version bump) · the four hand-ported derivations deleted · G2 + `scripts/lint-web.go` ·
the equivalence and `dedup_test.go` e2e vectors. Narrow-only; no adjacent mechanism substituted.

### 11.1 Verified touch-list (checked live at `570e5102`)

| Site | What it is now | What 1a does |
|---|---|---|
| `internal/starlarksandbox/sandbox.go:108-167` | `Execute` compiles (`SourceProgram`, :110) then Init+Call; compile is not separable | split into `Compile` + `Run`; `Execute` becomes the two composed |
| `internal/starlarksandbox/sandbox.go:110` | `SourceProgram` already returns the `*syntax.File` and discards it (`_`) | keep it — top-level `def`/assign names come free, no second parse |
| `internal/processor/ddl_cache.go:24-48` | `MetaVertexRef` carries `ScriptSource` as a string | add a shared `*CompiledScript` built at `loadMetaVertex` (:268-279) |
| `internal/processor/step4_hydrate.go:154-284` | the three hydration loops read `env.ContextHint` directly | read a merged, derived-aware read set instead; `env` stays untouched |
| `internal/processor/starlark_runner.go:100-135` | `Run` builds 8 globals then `Execute`s (a compile per op) | build the same 8 names, reuse the shared program |
| `internal/processor/opwire/opwire.go:104,273-310` | `MaxDeclaredReads = 1000`; the `egressReads` mutual exclusion at parse | the step-4 re-check mirrors both over the merged set |
| `internal/processor/commit_path.go:907-914` | a `*HydrationError` → `ErrCodeHydrationFailed` + `Term` | the derivation faults reuse it — terminal, fail-closed, already wired |

### 11.2 The one deviation, recorded not hidden

§4.1 specifies `HasDeriveReads` "computed once at cache-refresh **from a single parse**", separate from the
compiled-program cache. Built instead as **one** artifact: the flag falls out of the same
`SourceProgram` call that produces the shared program, because that call already returns the `*syntax.File`
and today throws it away. This is strictly less work than the design's shape (one compile, not a parse plus
compiles) and satisfies the same contract sentence — *"An operation whose owning DDL defines no
`derive_reads` is unaffected — no invocation, no cost"* — because the flag is checked before any Init or
Call. What it does **not** claim is "zero compiles": the compile happens once per cache generation and is
**shared with step 5**, which today pays one compile *per operation*. The honest statement of the cost is a
net reduction, and that is what the test asserts.

Detection covers a top-level `def derive_reads` **and** a top-level assignment to that name. A design that
matched only `DefStmt` would silently ignore an assigned derivation — fail-open, in the one direction this
increment exists to close.

### 11.3 Increment order + green checks

1. `starlarksandbox`: `Compile`/`Run` split + `Program.DefinesTopLevel` — `go test ./internal/starlarksandbox/`
2. `CompiledScript` + `MetaVertexRef.Script` + `ScriptContext.Compiled`; `StarlarkRunner.Run` on the shared
   program — `go test ./internal/processor/`
3. the pre-pass itself (stubs, grammar, weakest-wins, egress re-check, ceiling) — same
4. full `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
   `go test ./...`

### 11.4 In-scope gotchas

- **The predeclared name set must be identical across the two passes**, or the shared program is invalid:
  `SourceProgram` resolves globals at compile time against `globals.Has`. One canonical name list owns both,
  with a test that the runner's globals dict and the list agree — drift here is a compile error at runtime
  on a live op, which is the worst place to find it.
- **`state` in the pre-pass is not the hydrated state** — hydration has not run. It is an empty mapping, and
  `derive_reads` reading it gets nothing rather than stale anything.
- **Weakest-wins applies to a derived/derived collision too**, not only derived-vs-envelope. §4.1 states the
  rule only against the envelope; a key in both derived lists is the same ambiguity and the same hardening
  hazard, so it resolves the same way.
- **The ceiling counts the distinct union**, matching `distinctKeys`' existing semantics — the ceiling has
  always bounded round trips, not mentions (§2.5), and a derived duplicate must not consume budget twice.

### 11.5 Non-goals

Everything in 1b above; Inc 2/3/4; any change to `kv.Read`'s live fallthrough, the live-read budget, or the
sensitive-read tracker; any contract edit (§2.5 class (g) is already committed and this fire builds to it).

### 11.6 Review — two adversarial passes launched, both killed; the audit was done by hand

Two opus adversarial reviewers (security/egress lens, correctness/lifecycle lens) were launched over
the diff and **both were stopped after running well past a reasonable window** for a ~600-line change.
The correctness reviewer's last words name a cause worth carrying forward: *"the tree appears to be
mutating under me"* — the fire had committed and rebased the worktree while a read-only reviewer was
mid-snapshot. **A review fan-out needs a frozen tree**; freeze before launching, or hand the reviewer a
SHA rather than a working directory.

The review was therefore completed inline, and the two findings it produced are in the shipped commit:

1. **The stub set was complete but not drift-proof** (fixed). `failingModule` takes a hand-written
   member list, verified here against the real modules' own sets — `kv` = `{Read, Links}`
   (`starlark_kv.go:238-239`), `nanoid` = `{new, short}` (`starlark_builtins.go:55-56`). A future
   builtin added to either would have landed **unstubbed**, and because the pre-pass shares the main
   pass's compiled program that is not a loud unbound name — the member is simply absent from the
   struct. `TestDeriveReads_StubsCoverEveryRealMember` now compares against `AttrNames()`.
2. **A dead compile-failure branch** (removed). `deriveReads` handled a compile error that
   `hasDeriveReads` had already made unreachable. Collapsed into one `deriveReadsProgram()` answering
   "is there a derivation, and what runs it?" in a single call, so there is no state between the
   question and the answer and no second path for a compile failure to take.

**Verified and NOT found (the refuted list — the part a findings-only report would hide):**

- **Program reuse is safe by construction, not by assumption.** `Program.Init` allocates a fresh
  `Function` + `Module` per call, with `globals: make([]Value, ...)` and freshly materialized
  `constants`, writing nothing back to the program
  (`go.starlark.net@v0.0.0-20260326113308/starlark/eval.go:437-445, 498-529` — the pinned version, per
  the vendor rule). Repeated **and** concurrent Init are both safe, and no top-level-defined value can
  leak from one operation into another.
- **No existence oracle.** Every derivation fault — `DeriveReadsFailed`, `DeriveReadsInvalid`,
  `DeriveReadsEgressConflict`, `DeclaredReadCeilingExceeded` — is decided **before any Core KV GET**, so
  none of them can carry key-existence information. The keys are script-computed, not attacker-chosen,
  and the one that reaches the caller (`MissingKey` on the egress conflict) is a key the caller
  **already declared in `egressReads`** — that is how the collision arose.
- **Sensitive/PII parity.** A derived key runs the identical `decryptSensitiveDoc(..., false, tracker,
  rid)` path a declared read runs, charges the same `sensitiveReadTracker`, and is seen by step 6's
  external-egress guard identically. A derivation cannot produce an `egressReads` key at all (closed
  return shape), and a collision with one faults.
- **No new reach.** A script can already `kv.Read` any key at step 5 as a class-(b)/(c) live read, so
  pre-hydrating a derived key grants no reach the script lacked — it only moves the read into the
  declared, snapshotted, OCC-conditioned class, which is the whole point.
- **No live-read-budget bypass.** Declared reads have never been charged against `LiveReads` (that
  budget is execution-time); the derived set is bounded by the same 1000-key declared-read ceiling,
  enforced over the merged distinct union.
- **No behaviour change for shipping ops.** No package declares `derive_reads`, the entrypoint is
  resolved off the compiled program, and the full `-p 4` suite is green across 115 packages.

**One residual, accepted and named.** A derivation fault's message reaches the submitter through
`handleStubFailure`'s `err.Error()`, which for `DeriveReadsInvalid` includes the malformed key the
script returned. It is derived from the submitter's own payload and appears only on a terminal reject,
so it discloses nothing about another subject — but it is the one place a package's internal derivation
becomes caller-visible, and a package whose derivation could embed something non-payload-derived should
know that before it ships.

### 11.7 CHECKPOINT — Inc 1a shipped; Inc 1b is the adoption half

**Shipped** `8aaea2b9` (2026-08-03): the platform primitive, gates green, full suite green.

**Inc 1b — the adoption half, unstarted.** Worktree `lattice-wt-derivereads` (branch
`fire/derive-reads-inc1a`) is merged and reusable. Remaining, per §9's Inc 1 row:

1. identity-domain declares its four derived keys in a `derive_reads` — **with the package version
   bump**, since a same-version package edit no-ops.
2. Delete the four hand-ported derivations: `cmd/loftspace-app/web/app.js:945-1020` (+
   `credentials_link.go:137-155`), `cmd/clinic-app/web/app.js:654-660,706-714`,
   `cmd/facet/credentials.go:150-160,244-277`, `cmd/lattice/identity/identity.go:278-303`. These are the
   §9 lane tails this item owns — build them here, do not file them to `verticals.md`.
3. **G2** — the default-deny call-site ban, incl. the new `scripts/lint-web.go` for the JS half, shipping
   **blocking** over the then-clean tree, with the six object-id sites annotated.
4. The §7 equivalence vectors (the three `identityindex` keys byte-identical across `derive_reads`,
   `ddls.go:715-728`, and `cmd/lattice/identity`) and the `dedup_test.go` e2e with a submitter that
   declares nothing.

Ordering matters: (2) must land **after** (1) is installed, or the submitters lose a derivation nothing
yet supplies.

## 12. Increment 1b fire brief (build note, 2026-08-03)

**Scope sentence (this fire).** identity-domain declares its four derived keys in a `derive_reads(op)`
(version bump included), the five hand-ported derivations that computed the same keys client-side are
deleted, and **G2** ships blocking over the then-clean tree — the `substrate.SHA256NanoID` call-site ban in
`scripts/lint-conventions.go` plus the new `scripts/lint-web.go` for the JavaScript half — with the
equivalence vectors and the declares-nothing `dedup_test.go` e2e that pin the agreement.

**Scope-diff gate against §9's Inc 1 row + the §11.7 checkpoint, item by item.** (1) identity-domain
declares ✓ · version bump ✓ · (2) the hand-ported deletions ✓ · (3) G2 + `lint-web.go` ✓ · (4) equivalence
vectors + dedup e2e ✓. **Narrow-only, one correction to the checkpoint's count:** the checkpoint says *four*
hand-ported derivations; the live census finds **five** call sites across four files (`cmd/lattice/identity`
carries three `SHA256NanoID` calls in one helper, and both `credentials_link.go` and `cmd/facet` carry a
credentialindex probe). The set of *files* is the checkpoint's; the count is corrected, not widened.
**Explicitly NOT in this fire:** `UnlinkCredential`'s `vtx.credentialindex.*` tombstone key (§4.2 assigns it
to Inc 2 — it is a *mutation* key committed unconditioned today, and declaring it changes that op's OCC
conditioning, which is Inc 2's own increment with its own test); **G1** and the `[no-op-meta:]` vocabulary
(Inc 3); anything in Inc 2/3/4.

### 12.1 Verified touch-list (checked live at `08565480`)

| Site | What it is now | What 1b does |
|---|---|---|
| `packages/identity-domain/ddls.go:539-1499` | `identityDDLScriptTemplate`; helpers at :540-649, `execute(state, op)` at :651 | insert `derive_reads(op)` + shared normalizers before `execute`; `execute` calls the same normalizers |
| `ddls.go:680-696,715-728` | `CreateUnclaimedIdentity` normalizes email/phone/name inline, then builds three `vtx.identityindex.*` keys | the normalization moves to top-level helpers both passes call — agreement by construction, not by test |
| `ddls.go:961,1189` | `ClaimIdentity` / `CompleteCredentialLink` build `vtx.credentialindex.<sha256NanoID(op.actor)>` | derived from `op.actor`, which `derive_reads` receives |
| `packages/identity-domain/package.go:33` + `manifest.yaml:2` | `Version: "0.10.4"` in both | bump both — a same-version package edit no-ops (`lint-package-version` gates it) |
| `cmd/lattice/identity/identity.go:103,283-307` | `identityIndexProbeKeys` (3 `SHA256NanoID` calls) feeds `ContextHint.OptionalReads` | delete the helper; the op declares nothing |
| `cmd/loftspace-app/web/app.js:960-1001,1008-1023,1047` | `sha256NanoID` PCG-128 port + `identityIndexProbeKeys` + its `optionalReads` use | delete all three; **`sha256Hex` (:945) STAYS** — it hashes the client-minted claim secret, not a read key |
| `cmd/clinic-app/web/app.js:660-695,702-717,738` | the same three, byte-identical | same; `sha256Hex` (:640) stays |
| `cmd/loftspace-app/credentials_link.go:152` | one `credentialindex` entry inside `OptionalReads` | drop that entry only; the other three declared keys stay |
| `cmd/facet/credentials.go:274` | same shape | same |
| `scripts/lint-conventions.go:388-426,864-907,1307-1319` | `annotationSpans` + the `authcontext-target` default-deny gate to mirror; `git ls-files "*.go"` discovery | add the G2 Go gate in that idiom |
| `scripts/lint-facet-discovery.go:113` | the one existing script that reads `.js`/`.mjs` | precedent for `lint-web.go`'s file walk |

### 12.2 Two implementation decisions, recorded (Winston, §0)

**(a) The normalizers become shared top-level helpers, not a copy inside `derive_reads`.** §8's A4 rejects a
declarative field because "a Starlark function calling the same `crypto.sha256NanoID` builtin the main
script calls is the only shape where *these two keys agree* is true by construction." A `derive_reads` that
re-types the normalization would reintroduce exactly the divergence hazard one file lower down. So
`normalize_name` / `normalize_email` / `normalize_phone` / `identity_index_key` / `credential_index_key`
become top-level defs, and **`execute` is rewritten to call them** — the two passes then cannot disagree
without a compile error.

**(b) G2's JavaScript half fingerprints the hash-to-NanoID *conjunction*, not the function name.** A
name-only scan (`sha256NanoID`) is defeated by a rename, and an alphabet-only scan flags
`cmd/facet/web/boot.mjs:28`, which uses the same NanoID alphabet to mint a **random** device id — a
legitimate use that `// derived-key:` would mislabel. The gate therefore flags a JS/MJS file under `cmd/**`
that contains the NanoID alphabet literal **and** a SHA-256 digest call: that conjunction *is* the re-port.
Verified live: the two `app.js` files hold 2 and 4 digest calls; `boot.mjs` and `feed_source.test.mjs` hold
zero, so they pass unannotated after the deletion and the gate ships blocking over a genuinely clean tree.

### 12.3 Increment order + green checks

1. `derive_reads` + shared normalizers in `ddls.go`, `execute` re-pointed at them, both versions bumped —
   `go test ./packages/identity-domain/`
2. the equivalence vectors (the three `identityindex` keys byte-identical across `derive_reads`,
   `execute`'s own path, and `substrate.SHA256NanoID`) + the `dedup_test.go` e2e whose submitter declares
   nothing — same
3. the five submitter deletions — `go build ./...`, `go test ./cmd/...`
4. G2: the `lint-conventions.go` gate + `scripts/lint-web.go` + the Makefile/CI wiring + annotations on the
   object-id sites — `STRICT=1 go run ./scripts/lint-conventions.go`, `STRICT=1 go run ./scripts/lint-web.go`
5. full `go build ./...`, `make vet`, `golangci-lint run ./...`, every `scripts/lint-*.go` gate, `go test ./...`

### 12.4 In-scope gotchas

- **`op.payload` is a `starlarkstruct.Struct`, not a dict** (`derive_reads.go:140-154`). §4.1's illustrative
  `op["payload"].get("name")` does not run against the shipped primitive; the real access is
  `getattr(op.payload, "name", None)` / `hasattr`, exactly as `execute` already reads `p`. §4.1's example is
  corrected in the same commit so the next author does not copy a form that cannot work.
- **`nanoid` is a fail-closed stub in the pre-pass.** The normalizers must not reach it. They don't — they
  are string ops plus `crypto.sha256NanoID`, which is bound for real.
- **A derivation must not `fail()` on a payload `execute` would reject.** `derive_reads` runs *before* the
  op's validation, so raising on a missing/short name turns an `InvalidArgument` the script reports cleanly
  into a `DeriveReadsFailed` hydration fault. Every branch returns `{}` instead when the payload is not
  shaped for a key.
- **Weakest-wins makes the deletions safe in either order at runtime, but not the install.** A submitter
  that still declares the key merely collides with the derived one and keeps its own (weaker) disposition —
  harmless. The real ordering constraint is the install: the deletions must not ship to a stack still
  running 0.10.4.

### 12.5 Adjacent finds — filed now, not built here

- `UnlinkCredential`'s `vtx.credentialindex.*` **tombstone** commits unconditioned (`ddls.go:1335`;
  `applyHydratedRevisions` skips un-hydrated keys). Already owned by Inc 2 (§4.2) — named here so the
  census is honest, not re-filed as a new row.
- The **three `_test.go` object-id sites** (`cmd/loupe/objects_crypto_e2e_test.go:95`,
  `cmd/loftspace-app/objects_crypto_test.go:233,397`) are inside G2's `git ls-files "*.go"` reach, so the
  gate covers **seven** sites, not §6's "six". Tests get the annotation too rather than an exemption: a
  test is exactly where a re-port would otherwise be reintroduced.

### 12.6 Non-goals

Inc 2 / 3 / 4 in full; G1 and the `[no-op-meta:]` closed vocabulary; any change to the `derive_reads`
primitive itself, the live-read budget, or the sensitive-read tracker; any contract edit (§2.5 class (g) is
committed at `4965b28a` and this fire builds to it).

### 12.7 Review — three adversarial passes, run on a FROZEN tree

§11.6 recorded that Inc 1a's two reviewers had to be killed, one of them reporting *"the tree appears to be
mutating under me"* because the fire committed and rebased underneath them. This fire committed first
(`ec04eca9`), handed all three reviewers that SHA, and edited nothing until every one had reported. All
three completed. The practice works and should be the default.

Three lenses — correctness/equivalence, gate soundness, security/privacy. **No blocker.** The security pass
could not construct reach the previous state did not already grant, and the reason is worth stating because
it reframes the increment: `contextHint.optionalReads` is unvalidated client input that nothing scope-checks
(`internal/gateway/gateway.go` forwards it verbatim), so **any** submitter could always name these keys —
which is exactly what the five deleted derivations did. **The ceiling is unchanged; the default flipped.**

**What was actually wrong, and fixed in `fdf841a2`:**

1. **`ClaimIdentity` derives a key no submitter ever sent** — so §12's scope sentence ("the same keys the
   deleted clients computed") was false for that op. The probe was undeclared on every ClaimIdentity path,
   so the script's read-before-create branch was **dormant**: it always read absent and always took the
   plain `CreateOnly` create, which meant a credential whose index `UnlinkCredential` had tombstoned could
   not re-bind at all — the create asserted revision 0 against a key with write history. The revive branch
   `credential_index_mutation` was written for is now reachable for the first time. Intended, but it was an
   unstated, untested semantic change, and `opmetas.go` carried a live comment asserting the key is
   undeclared. Comment corrected; behaviour pinned by `TestClaimIdentity_RebindsAfterUnlink`, verified to
   fail without the derivation.
2. **The equivalence test tested nothing.** It compared two *Go* functions over an already-normalized
   string and never executed a line of Starlark; `tc.raw` was dead. It would have passed with every
   normalizer replaced by `return raw` — a green test over the exact property the increment exists to
   protect. Rewritten to drive each raw contact through a real operation; verified to fail when
   `normalize_email` stops trimming.
3. **The credentialindex half had zero coverage**, and the file's own docstring named a test that did not
   exist. Every pre-existing `CompleteCredentialLink` test declares the key itself, so weakest-wins made the
   derivation a no-op in all of them — deleting the branch left the suite green.
4. **A sixth hand-ported derivation was live.** `scripts/seed-showcase.go`, seven call sites, hashing the
   **raw** email where the script lowercases and trims — already capable of disagreeing with the key the
   operation probes. The census missed it (grep scoped to `cmd/` and `internal/`) and the gate's blanket
   `scripts/` exclusion then hid it. Deleted; the exclusion is now `scripts/lint-*` only.
5. **Both gate halves were narrower than the ban they advertised.** The Go pattern matched a
   package-qualified literal (an alias or dot-import walks past it) and the annotation used
   `annotationSpans`, whose indent scoping let one doc comment amnesty a whole function body. The web gate
   matched only the alphabet *literal*, so annotating the const blinded the file permanently — a
   reviewer-supplied bypass reusing `NANOID_ALPHABET` slipped through. Both are fixed and each hole is now
   pinned by a self-test case.
6. **`normalize_*` ran unbounded on an attacker-controlled field before `execute`'s `maxLen 200` check.**
   `MAX_CONTACT_INPUT` bounds it in the shared helper **and** in `execute`, on the raw value — capping only
   the normalizer would have resurrected the divergence this mechanism exists to end (300 leading spaces
   plus `"Ada"` is a 303-char raw input and a 3-char name).
7. **`mergeDerivedReads` could append into the envelope's own backing array**, contradicting the invariant
   its file states absolutely. Cloned. `deriveReadsOpValue` is built once rather than twice per derivation
   per OCC attempt.

**One finding rejected on inspection.** `time` was reported as an unstubbed impure module. It exposes
`rfc3339_utc` and `rfc3339_add` only — no `now()` — so binding it live is correct and stubbing it would be
cargo cult.

**§11.6's existence-oracle bullet, re-argued for ADOPTION** (it was a primitive-only argument, correctly
challenged). The fault path still carries nothing: every derivation fault is decided before any Core KV GET.
The *successful* path is the real channel — a hit emits `duplicateOf`, and the reply's revisions name the
incumbent identity plus which criterion matched. That is unchanged in kind from a submitter that declared
the key itself, and the index is deliberately global (cross-vertical dedup is what it is for). What changed
is that the platform now performs the probe for every submitter rather than only the ones that asked. The
underlying gap — **a declared read is never scope-checked against the actor's grant** — is pre-existing,
untouched here, and filed as its own row, because class (g) makes the *platform* the party exercising it.

**Named residuals, not claimed as closed:** a split alphabet literal and a two-module split still evade the
web gate — a regex gate over a bundle it cannot resolve has that limit, and pretending otherwise is worse
than recording it. `internal/gateway` and `internal/objectmanager` are submitters the Go gate does not
cover; widening to them needs annotations on legitimate sites this fire does not own, so it is a filed row.

### 12.8 CHECKPOINT — Inc 1b shipped; Inc 3 is next

**Shipped** `ec04eca9` + `fdf841a2` (2026-08-03): identity-domain declares, six client derivations deleted,
G2 blocking in both halves. Full suite green across 115 packages.

**Next: Inc 3** (§9) — `OpCeremonySpec`, Facet's mint-and-reveal executor, the `CreateUnclaimedIdentity` /
`RotateClaimKey` descriptors, and **G1** (the `[no-op-meta:]` closed vocabulary, incl. the six note
rewrites and `package_test.go:235`). Inc 2 is also unblocked by Inc 1 but sits behind its own backfill.

## 13. Increment 3 fire brief (build note, 2026-08-03)

**Scope sentence (§9 row 3, verbatim):** `OpCeremonySpec` + Facet client ceremony support +
`CreateUnclaimedIdentity` / `RotateClaimKey` descriptors + **G1** (incl. the six note rewrites +
`package_test.go`); two exemptions removed.

### 13.1 Verified touch-list (checked live at `fad2765b`)

| Site | What it is |
|---|---|
| `internal/pkgmgr/definition.go:476-504` | `OpMetaSpec` — `Presentation` (482), `Dispatch` (497) are the nil-skipped sibling pointers `Ceremony` joins |
| `internal/pkgmgr/build.go:229-264` | the op-meta emission loop; each optional sub-spec emits via `addCreate(opMetaKey+".<name>", docAspect(...))` |
| `internal/pkgmgr/build.go:569-593` / `:597-639` | `opPresentationBody` / `opDispatchBody` — the sparse-map body-builder idiom `opCeremonyBody` mirrors |
| `packages/edge-manifest/lenses.go:587-617` | `edgeCatalogTail` — projects `op.presentation.data.*` / `op.dispatch.data.*` into `manifest.op.<id>`; **the only path by which any client sees a descriptor aspect** |
| `packages/edge-manifest/composed_test.go:400-465` | a hand-copied fixture of that cypher, kept in sync manually |
| `packages/identity-domain/opmetas.go:32-` | the two shipped descriptors (`ClaimIdentity`, `RecordIdentityPII`) — the idiom to mirror |
| `packages/identity-domain/permissions.go:49,67,85,91,97` | the five `[no-op-meta:]` Notes (all five line numbers verified accurate) |
| `packages/control-authz/permissions.go:78` | the sixth Note — a shared template stamped onto every `personalLensPermissions()` op |
| `packages/identity-domain/package_test.go:214-251` | `TestPackage_CeremonyOpsStateTheirExemption` — pins the marker shape and the "exempt ⇒ no op-meta" complement |
| `scripts/lint-package-standard.go:78-80,743-770` | `exemptionMarker` + `checkS1`'s bare `strings.Contains` — G1's target |
| `scripts/lint-conventions.go:232,342-353,927-937` | `authcontext-target` — the **closed-vocabulary precedent to mirror** (open capture regex → set membership → distinct "unknown code" error) |
| `cmd/facet/web/app.js:955-978` | `opButton` — the degrade branches (`!dispatchClass`, unresolvable target) a ceremony-unsupported client extends |
| `cmd/facet/web/app.js:1928-1975` | `renderDescriptorForm` — `fieldNames` filter (1940) is where the minted-hash field is dropped |
| `cmd/facet/web/app.js:2178-2273` | `submitDescriptorForm` — payload assembly + `enqueue(...)` |
| `cmd/facet/web/app.js:592-607` | `applyOutboxFrame` — `e.state === "confirmed"` is the only accepted/rejected seam that reaches the tab |
| `cmd/facet/credentials.go:155-163` | `mintLinkSecret` — Facet's own 32-byte → hex-plaintext → sha256-hex idiom |
| `cmd/loftspace-app/web/app.js:936-948` | `showClaimSecret`/`closeClaimSecret` — the reveal precedent: a dedicated dialog, deliberately **not** a toast (a toast auto-hides and the next `toast()` clears it, either of which destroys the only way into the account) |
| `cmd/facet/web/descriptor_autofill.test.mjs:124-206` | the mirror test: a field excluded from the rendered set but present in the payload |

### 13.2 Scope-diff gate — two divergences from the ratified body, both narrow-only

**(1) `RotateClaimKey`'s target field is `identityKey`, not `targetIdentityKey`.** §4.3 names
`TargetField: "targetIdentityKey"`; the DDL branch reads `p.identityKey` (`ddls.go:1155`) and fails closed
on its absence. The design's field name is simply stale — corrected to the code.

**(2) `RotateClaimKey` ships with NO `TargetField`/`TargetType` at all** (Winston, §0 — an implementation
call the design did not reach). `resolveTargetKey` (`app.js:2134-2152`) ends in `selfAnchorKey(want)`, so a
declared `TargetType:"identity"` that resolves from no context silently substitutes **the submitting staff
member's own identity** rather than degrading. That is the exact hazard `ClaimIdentity`'s own descriptor
records at `opmetas.go:50-57` and refuses for the same reason; this op inherits the refusal. `identityKey`
instead carries `"x-entityRef":"identity"`, so the client renders a picker over the identities its mirror
holds and `submitDescriptorForm`'s pick-required guard (`app.js:2204-2206`) fails closed when it holds none
— unfillable-and-honest beats targeting the wrong person.

**(3) In-scope by necessity, not expansion: the `edgeCatalog` lens must project the ceremony columns.**
`.ceremony` is written by `build.go` but reaches a client **only** through `edgeCatalogTail`
(`lenses.go:587-617`) — without the projection the increment's own stated deliverable ("Facet client
ceremony support") has nothing to read. Same optional-and-nullable column idiom the file's doc comment
(555-567) already establishes for `presentation`/`dispatch`. Carries an `edge-manifest` version bump.

### 13.3 Increment order + green checks

- **3a — platform vocabulary.** `OpCeremonySpec` + `Ceremony` field + `opCeremonyBody` + emission +
  `edgeCatalogTail` ceremony columns + both version bumps.
  Green: `go build ./...`, `go test ./internal/pkgmgr/ ./packages/edge-manifest/`.
- **3b — descriptors + G1.** The two identity-domain descriptors, the exemption-note rewrites to the closed
  vocabulary, `lint-package-standard`'s extract-and-validate, `package_test.go` re-pinned.
  Green: `go test ./packages/identity-domain/ ./packages/control-authz/`,
  `STRICT=1 go run ./scripts/lint-package-standard.go`.
- **3c — Facet's mint-and-reveal executor.** Field removal, mint, reveal-on-`confirmed`, discard otherwise,
  degrade without support. Green: `node --test cmd/facet/web/`, `go test ./cmd/facet/`.

### 13.4 In-scope gotchas

- **A package edit needs a version bump** — a same-version install no-ops, so both `identity-domain`
  (0.11.0) and `edge-manifest` (0.14.13) bump or the change never lands on a running stack.
- `docAspect` emits `data:{}` for an all-zero non-nil spec, which is why every body builder is sparse and
  the **caller** does the nil-skip. `opCeremonyBody` follows both halves.
- `composed_test.go`'s embedded cypher copy must gain the same columns or its comparison drifts.
- `AuthContext` is **doc-comment convention, not validated Go** (`definition.go:559-568`); `"standing"` is
  correct for both ops (scope=any grants to frontOfHouse/backOfHouse/operator, `permissions.go:47-51,65-69`).
- `control-authz:78`'s Note is a **shared template** across every `personalLensPermissions()` op — one edit,
  many ops; its code is `client-agent-op`.
- Removing an exemption Note and adding an op-meta must land **together**: `package_test.go`'s complement
  asserts an exempt op carries no descriptor, so a half-edit reddens the package.

### 13.5 Non-goals

Inc 2 (`boundTo`/`UnlinkCredential`), Inc 4 (`credential` authContext, `RevealWith`, `PairedCodeField`), any
`InitiateCredentialLink`/`CompleteCredentialLink` descriptor, AI-authorability of `Ceremony`
(`capabilitymaterializer_starlark.go`'s artifact allow-lists deliberately untouched — the same posture
`Sensitive` already has), and any change to `derive_reads`.

### 13.5 Review — three adversarial passes on a frozen tree; the brief's own deviation was wrong

Security/leakage, edge-case, and acceptance-vs-ratified-scope, all read-only against a frozen
`79cbcdb4`. The security pass **refuted** eleven leak hypotheses by tracing them to code — the plaintext
never enters the payload, storage, a log, a URL, an attribute, or the outbox history; `Duplicate`→
`confirmed` is a redelivery of the identical envelope, so revealing on it is correct; and the mint/hash
is byte-identical to what `ddls.go` compares. What the three converged on instead was worth the pass:

1. **§13.2's deviation (2) was FALSE, and its fix made things worse.** The claim that `resolveTargetKey`
   falls back to the submitter's own identity does not hold — `selfAnchorKey` answers only the six anchor
   types `edgeIdentitySpec` stamps, and `identity` is not among them. The premise was inherited from
   `ClaimIdentity`'s own comment, which has been wrong since it was written, and this fire propagated it
   into a second descriptor, a test rationale, and a commit message. **Corrected in both directions:**
   `RotateClaimKey` now declares `TargetField`/`TargetType` — which is what lets `opButton`'s gate withhold
   it honestly while nothing projects an identity — and keeps the `x-entityRef` picker so it becomes
   completable the moment one does. `ClaimIdentity`'s comment is corrected at its source, conclusion intact,
   mechanism replaced.
2. **The vocabulary's own expiry property was unexercised, and one code was wrong.** `ceremony-mint` stayed
   valid in the commit that shipped `OpCeremonySpec`, and `UnlinkCredential` was filed under `lifecycle-op`
   — a code whose meaning it does not match — because the ratified set had no code for its actual blocker.
   `ceremony-mint` is now **retired** (the first real exercise of "a code dies when its mechanism ships"),
   `unprojected-input` added, and the gate now also fails an op carrying **both** a valid exemption and a
   full descriptor — the drift G1 exists to catch, previously pinned only per-package.
3. **A `Ceremony` with an empty or misspelled `MintedSecretHashField` failed OPEN**, and `build.go`'s
   comment claimed a client-side rule covered it. It does not: the client cannot tell an empty field name
   from "no ceremony", so it renders the hash as a text input. The check now lives in S1's descriptor
   completeness, where the author is; the false comment is corrected.
4. **Two writes, one survivor.** The submit had no in-flight guard, so a double-click minted twice — and
   `showModal` replaces wholesale, so the second reveal destroyed the first. `CreateUnclaimedIdentity` does
   not reject a duplicate; it creates a second identity. Guarded, and the reveal now shows every
   un-dismissed secret rather than the latest.
5. Smaller, all fixed: the `async` conversion turned every pre-enqueue throw into a silent unhandled
   rejection; a `rejected` settle left the person waiting on a modal that would never come; `signOut` did
   not purge held plaintexts; the settle ran before the outbox bookkeeping and dropped the secret before
   rendering it; an overlay click could dismiss a once-only secret; `ceremonySupported` did not check
   `TextEncoder`, which `mintSecret` uses.

**§7's missing assertion, now written.** "The minted-hash field is present in the submitted payload" had no
test — the highest-consequence coupling in the increment, since a divergence hands over a secret that does
not match the stored hash and makes the identity permanently unclaimable, silently. The write path is now a
named seam (`enqueueOperation`) and the test asserts the submitted hash **is the sha256 of the plaintext
that gets revealed**, plus that the plaintext appears nowhere in the request.

### 13.6 CHECKPOINT — Inc 3 shipped; Inc 2 is next

**Shipped** `39b77d59` + `ea73d5f1` + `79cbcdb4` + the review pass (2026-08-03): the ceremony vocabulary,
its lens columns, two descriptors, G1 as a closed *expiring* vocabulary, and Facet's mint-and-reveal
executor. Exemptions 5 → 3.

**Named residuals, filed as rows** (not claimed closed): no Facet surface offers a **standing** staff op,
so both new descriptors are unreachable until one exists — pre-existing, and the reason these ops had no
descriptor, but Inc 3 is what makes it binding; nothing projects an `identity` entity, so the picker has no
candidates; and a durably-queued ceremony write outlives the plaintext it minted when the tab reloads
offline.

**Next: Inc 2** (§4.2) — the `boundTo` link, its one-shot backfill, the per-credential Protected lens, the
`signInMethods` pane section, and `UnlinkCredential`'s descriptor, which retires the `unprojected-input`
exemption. Inc 4 stays designed-not-built: still no two-device consumer (§4.4).

## 14. Increment 2 fire brief (build note, 2026-08-03)

Inc 2 splits at a real seam: **2a is the write path + the read model**, **2b is the migration + the
surface**. The split is forced by §7's own ordering rule — *"the pane section ships only once the backfill
has run"* — so the pane, the descriptor and the exemption retirement cannot land in the same unit as the
link they read. 2a is what makes a backfill expressible at all.

### 14.1 Verified touch-list (checked live at `40d343ca`)

| Site | What it is today | 2a |
|---|---|---|
| `packages/identity-domain/ddls.go:1100-1131` (`ClaimIdentity` mutations) | writes `credentialBinding`, `state`, tombstones `claimKey`, upserts `credentialindex` + the `holdsRole` grant | + emit `boundTo` |
| `packages/identity-domain/ddls.go:1326-1355` (`CompleteCredentialLink`) | upserts `credentialindex`, appends to `credentials[]` | + emit `boundTo` |
| `packages/identity-domain/ddls.go:1441-1447` (`UnlinkCredential`) | tombstones `credentialindex`, rewrites `credentials[]` | + tombstone `boundTo` |
| `packages/identity-hygiene/ddls.go:564-570` (`MergeIdentity` credential repoint) | repoints each `credentialindex` to the primary | + repoint `boundTo` (tombstone secondary, emit primary) |
| `packages/identity-domain/ddls.go:729-766` (`derive_reads`) | declares the three `identityindex` probes + the `credentialindex` probe for Claim/Complete | + the `boundTo` key on all three ops, + `credentialindex` for `UnlinkCredential`, + `ClaimIdentity`'s `consumer_grant_key` |
| `packages/identity-domain/ddls.go:450-497` (link-type DDLs) | `indexes`, `duplicateOf` | + `boundTo` |
| `packages/identity-domain/lenses.go` | `identityCredentialsRead` (whole encrypted array in one jsonb column) | + `identityCredentialBindingsRead`, one row per link |

**`MergeIdentity` is the writer the ratified body did not name.** §4.2 lists `CompleteCredentialLink` and
`ClaimIdentity` as the paths that bind and `UnlinkCredential` as the path that unbinds. It is silent on
merge — which repoints every `credentialindex` in `cred_set` to the primary and unions the arrays
(`identity-hygiene/ddls.go:564-579`). A `boundTo` left un-repointed there would outlive its own premise:
the lens would keep projecting the merged-away secondary as the credential's owner, and the RLS anchor
would confine the row to an identity that is now `state=merged`. The link is repointed in the same batch,
by the same `cred_set` loop, under the same unconditioned-blind-Put idiom that loop already uses.

### 14.2 Two implementation decisions, recorded (Winston, §0)

**(1) `bound_at` is not a column, because the engine cannot project a relationship.** §4.2 specifies the
lens columns as `identity_key`, `credential_actor_key`, `bound_at`. The first two come from the pattern's
node endpoints; the third would have to come from the link's own `data`, and **no relationship variable is
ever bound**: `traverseRel` (`internal/refractor/ruleengine/full/executor.go:900-1000`) extends the binding
with the *neighbour node* only, and while `RelPattern.Variable` is parsed (`visitor.go:274`) nothing in the
executor writes it into a binding. So `MATCH (c)-[b:boundTo]->(u) RETURN b.data.boundAt` has no meaning
today. The link still carries `data.boundAt` — it is the provenance the graph should hold, and it is what a
projection would read once the engine can — but the 2a lens ships two columns, and the missing third is
**filed as its own row** with this lens named as its consumer, rather than papered over by re-reading the
encrypted array the link exists to replace.

**(2) The writer is an `update`, and the key joins the declared read set — two separate properties, not
one.** The first is load-bearing and was measured: with the mutation flipped to `create`, the re-bind of a
credential that was unlinked earlier is **rejected** (`TestBoundTo_RelinkRevivesTombstonedLink` fails on
outcome), because the create asserts revision 0 against a key that already has write history. So the
revive posture `credential_index_mutation` already carries is required here too.

The declaration is a *different* property, and the same falsification showed it is not what buys the
revive: with the derived key removed from `derive_reads`, that test still passes — an unconditioned Put
over a tombstone simply overwrites it. What declaring the key buys is **OCC**: the write commits
conditioned on the revision it was read at instead of last-writer-wins. It is kept anyway, on the ground
the increment chain is built on — a deterministic package-derived key is exactly Contract #2 §2.5
class (g), and a blind Put on one is the undeclared posture class (g) exists to retire — not on a
correctness claim it does not support. `optionalReads`, never `reads`: absence is the ordinary case on
every bind.

**The same three lines close a filed board row.** `ClaimIdentity`'s `consumer_grant_key` (the
`holdsRole` link it upserts unconditioned) is a class-(g) key in the *same function*, filed as its own
★★ row precisely because "browser clients cannot compute the deterministic role key a declared read would
require" — verbatim the blocker class (g) removes. It is derived here too, and the row closes.

Declaring it converts a deliberately-unconditioned write into a CAS, so the question it has to survive is
whether that can fail a ceremony whose own comment says it "must leave the person able to act at all" —
the Gateway's `ProvisionConsumerIdentity` pre-flight writes the same key, concurrently, on any
authenticated touch. It cannot: `commitPipeline` wraps hydrate → execute → validate → commit in a bounded
OCC retry that re-hydrates and re-executes on a same-key revision conflict rather than bouncing
`RevisionConflict` to the client (`internal/processor/commit_path.go`, `defaultMaxCommitAttempts = 3`). The
CAS is therefore strictly better than the blind Put: the race is absorbed instead of silently clobbering a
concurrent write. `TestClaimIdentity_ConsumerGrantAlreadyHeld_Succeeds` — which seeds the exact state the
pre-flight leaves and declares nothing about the grant key — is the regression guard, and it is green.

### 14.3 Increment order + green checks

1. `boundTo` link-type DDL + the key/mutation helpers + `derive_reads` (identity-domain), version bump.
2. Emit/tombstone at the three identity-domain paths; repoint at `MergeIdentity`, version bump.
3. `identityCredentialBindingsRead` Protected lens.
4. Tests: one row per link + RLS confinement; unlink retracts the row; a re-bind revives a tombstoned link;
   merge repoints the row to the primary; the derived key set is byte-identical to what the scripts write.

Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
`go run ./scripts/lint-package-standard.go`, `make verify-package-identity-domain`, the identity-domain /
identity-hygiene / refractor package tests.

### 14.4 Non-goals (2a)

The backfill, the `signInMethods` pane section, `UnlinkCredential`'s `OpMetaSpec`, and the
`unprojected-input` exemption retirement — all 2b, all gated on the backfill by §7. Removing the bespoke
`/api/credentials` handlers stays out of Inc 2 entirely (§4.2's closing paragraph).

### 14.5 Review — two adversarial lanes on a frozen tree; the merge was the defect

Two read-only reviewers (correctness, security) ran against the frozen build commit. Four findings landed,
all substantiated against code rather than against a comment, all fixed in the same increment.

**The merge wrote every `boundTo` edge twice — the one real defect.** `cmd/lattice`'s
`enumerateSecondaryEdges` excludes `duplicateOf` and `indexes` from the generic `edges` payload for a
structural reason: `MergeIdentity` repoints those classes in its own dedicated loops, and a class the
script handles itself must never also arrive as a generic edge. `boundTo` is a third such class and did
not get the exclusion. Every live `boundTo` edge of the secondary therefore reached the generic
link-migration loop *and* the new credential loop, putting two conflicting conditioned publishes on one
subject inside one atomic batch — and the generic loop's plain `create` on the rewritten key dies on a
`RevisionConflict` whenever that key is a tombstone the credential loop deliberately revives, which is the
unlink-then-rebind lifecycle §14.2 exists to protect. **Every merge test in the repo passes
`edges := []string{}`, including the one written for this increment; the real CLI never does.** That is
what hid it, so the fix ships with a `cmd/lattice` test that seeds a `boundTo` edge and asserts its
exclusion, verified to fail without the fix.

**Erasure did not erase it.** `ShredIdentityKey` tombstones the identity's `indexes` and `duplicateOf`
links in the shred batch, because a plaintext derivative outlives the key it describes. `boundTo` names —
in the key itself, traversably, decrypt-free — which sign-in credential belonged to which person. Both
sibling link types in identity-domain carry shred handling *and say so in their DDL text*, so the omission
was a break in an established invariant of the link plane, not a pre-existing residual. The reviewers split
on this; the security lane is right, and the correctness lane's "symmetric with `credentialindex`" reading
does not hold — `credentialindex` is a vertex and was never in that class. Enumerated both directions
(the identity is the target of every credential bound to it, and the source when it is itself a credential
of another) and tombstoned with the rest.

**Two smaller ones.** The merge's batch-size pre-flight still counted one mutation per credential while
the loop now emits three, so an over-large merge could clear the script's guard and hit the substrate's
*terminal* `BatchTooLarge` instead of the clean domain error the guard exists to produce. And the
credential loop lacked the self-loop guard the generic rekey loop 80 lines above already applies: the
primary can itself be a credential of the secondary.

**Filed, not fixed:** the lens drops any credential whose identity vertex was never provisioned — `seedNodes`
anchors on `(c:identity)` and `readNode` returns nil for a missing key, so the row silently never appears,
while `identityCredentialsRead` still lists the credential from the decrypted array. Reachable through
`lattice identity claim`'s operator-supplied `--actor`, which skips the Gateway's provisioning pre-flight.
Its own row: closing it means either provisioning in the CLI or re-anchoring the lens, and it becomes
binding in 2b (an un-listable credential is un-unlinkable).

**Checked and cleared:** the RLS anchor confines correctly and fails closed on an empty anchor list, and
the retraction path re-evaluates both endpoints; `derive_reads` is not an enumeration oracle on
`UnlinkCredential` (the hydrated index is never read, only tombstoned, and both keys are `optionalReads`
so hit and miss are indistinguishable); the CAS'd grant cannot skip the grant while still transitioning
state (one atomic batch); chain-merge U3→U2→U1 leaves exactly one live edge per credential.

### 14.6 CHECKPOINT — Inc 2a shipped; Inc 2b is next

**Shipped** `217769cc` + `9c00567f`: the `boundTo` link and its four writers, the per-credential Protected
lens, three class-(g) derived keys, and the review pass above. Merged, worktree removed — Inc 2b takes a
fresh one. Live on the dev stack: all three packages diff-applied (identity-domain 0.12.1 → 0.13.1),
`read_identity_credential_bindings` provisioned with forced RLS, Loupe rebuilt and cycled.

**Next: Inc 2b** — the one-shot backfill (without it the lens projects only bindings made after this
commit), the `signInMethods` pane section over it, `UnlinkCredential`'s `OpMetaSpec`, and the
`unprojected-input` exemption retirement. §7's ordering rule binds: the pane ships only once the backfill
has run. Note for that fire: the backfill's enumerable source is the `credentialindex` keyspace, whose
vertices already carry `{actorKey, identityKey, boundAt}` in plaintext — no decryption of the sensitive
array is needed to reconstruct the edge set.

## 15. Increment 2b-1 fire brief (build note, 2026-08-03)

Inc 2b splits again, at the ordering rule §7 states: **2b-1 is the reconciliation that makes the link
plane complete; 2b-2 is the surface that reads it** (the `signInMethods` pane section,
`UnlinkCredential`'s `OpMetaSpec`, the `unprojected-input` exemption retirement). The pane cannot ship
first — an identity bound before `217769cc` has a live credential and no edge, so the section would render
an authoritative-looking empty list for exactly the people who have most to lose by believing it.

### 15.1 Verified touch-list (checked live at `e0ac7f86`)

| Site | What it is today | 2b-1 |
|---|---|---|
| `packages/identity-domain/ddls.go:55-65` (`identity` `PermittedCommands`) | nine ops | + `ReconcileCredentialBinding` |
| `packages/identity-domain/ddls.go` (`identityDDLScript` `execute`) | one `if ot == …` branch per op | + the reconcile branch |
| `packages/identity-domain/ddls.go:729-851` (`derive_reads`) | Create / Claim+Complete / Unlink branches | + the reconcile branch (index key + link key) |
| `packages/identity-domain/permissions.go:44-99` | nine entries | + `ReconcileCredentialBinding` scope=any → `operator` |
| `packages/identity-domain/package.go:33` | `0.13.1` | version bump (a same-version edit no-ops) |
| `cmd/lattice/identity/identity.go:36-44` | `create-unclaimed`, `claim` | + `reconcile-bindings` |

### 15.2 The scope-diff gate — one deviation, narrow-only

§7 ratified *"a one-shot backfill op over the existing `credentialBinding` arrays."* This builds a
**re-runnable reconcile op over the `credentialindex` vertex**, driven by a CLI enumerator. §14.6 already
re-grounded the source: the index vertex carries `{actorKey, identityKey, boundAt}` in **plaintext**, so
the edge set reconstructs without ever decrypting a sensitive aspect. The deviation is narrowing on both
axes that matter — a smaller read (no decrypt) and a smaller authority (the op reads one key it derives
itself, rather than a person's whole bound set) — and the output edge set is identical, because
`credential_index_mutation` and `credential_bound_to_mutation` are written in the same batch by all four
2a writers.

It is named `ReconcileCredentialBinding`, not `Backfill*`: the op is permanently useful. It converges the
link plane onto the index for one credential, which is the repair verb for any future divergence, not a
verb whose meaning expires the moment it has run once.

### 15.3 Why the index vertex is the authority, and what that buys

The payload names `credentialActorKey` **and** `identityKey`, because `derive_reads` runs before hydration
and the link key needs both halves. That makes the owner client-supplied — so the script does not trust
it. It hydrates `vtx.credentialindex.<sha256NanoID(credentialActorKey)>`, requires it live, and requires
`data.identityKey` to equal the payload's; a mismatched pair is rejected rather than written. Forging an
edge therefore requires an index vertex that already says what the forgery claims, which is the edge
itself.

Three properties fall out of the same choice, none of which the encrypted array would have given:

- **A deliberately-unlinked credential is not revived.** `UnlinkCredential` tombstones the index vertex and
  the link in one batch, so a tombstoned index makes the reconcile reject. Reading the array instead would
  have re-derived the entry only if the array were also authoritative — and it is the array that
  `UnlinkCredential` rewrites last.
- **`boundAt` is the original**, read off the index, never `observedAt`. A reconcile that stamped its own
  run time would rewrite provenance to say every historical credential was bound the day the backfill ran.
- **Idempotent by construction.** The write is `credential_bound_to_mutation`'s `update` — the same revive
  posture §14.2 measured — conditioned on the revision the derived key hydrated at.

Both derived keys are `optionalReads`, never `reads`: the index can race away between the CLI's list and
the submit, and that must reject with a named domain outcome rather than fault `HydrationMiss`.

### 15.4 Increment order + green checks

1. The op: `PermittedCommands` + the `execute` branch + the `derive_reads` branch + the DDL description.
2. The permission (scope=any → `operator`; outside S1 by `userFacing`, same class as `UpdateIdentityState`).
3. Version bump.
4. The CLI driver: `lattice identity reconcile-bindings [--dry-run]` — `KVListKeysPrefix` over
   `vtx.credentialindex.` in `bootstrap.CoreKVBucket`, skip tombstoned index vertices, skip a credential
   whose live `boundTo` link is already present, submit one op per remaining credential.
5. Tests: a pre-existing index with no link yields exactly one live link with the index's `boundAt`; a
   tombstoned index rejects; an owner mismatch rejects; a self-loop rejects; a second run over an
   already-linked credential is a no-op at the CLI and a byte-identical document at the op.

Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
`go run ./scripts/lint-package-standard.go`, `make verify-package-identity-domain`, the identity-domain and
`cmd/lattice` package tests.

### 15.5 Non-goals (2b-1)

The `signInMethods` pane section, `UnlinkCredential`'s `OpMetaSpec`, and the `unprojected-input` exemption
retirement are all 2b-2. Removing the bespoke `/api/credentials` handlers stays out of Inc 2 entirely
(§4.2's closing paragraph). The filed row *"a credential whose identity vertex was never provisioned
projects no binding row"* binds on 2b-2's pane, not here — the reconcile writes the edge whether or not a
lens can anchor it.

### 15.6 Review — two adversarial lanes on a frozen tree; both found the same blocker

Two read-only reviewers (security, correctness) ran independently against the frozen build commit. They
converged, from different directions, on one blocker — which is the strongest signal the pass produced,
because neither could have been anchoring on the other.

**The op would have undone an erasure.** privacy-base's `ShredIdentityKey` tombstones every `boundTo` link
touching an erased identity, on the stated ground that the link names — in plaintext, in the key itself —
which sign-in credential belonged to which person. It does **not** tombstone the `credentialindex` vertex.
So an erased person leaves exactly the pair of facts this op was built to repair: a live index and no live
edge. The brief's own premise — *the index is the authority* — was drawn from `UnlinkCredential`, which
tombstones both in one batch, and generalized to a second retraction path that behaves differently. The
driver's test file pinned the wrong behaviour explicitly, as case D.

The correction separates two things the brief had conflated. The index is authoritative over the edge's
**content** — who owns the credential, when it was bound — and over nothing else. Whether an edge *should*
exist is answered by the edge: an existing tombstone is a retraction somebody made, and this op does not
overturn one. The writable case is the **absent** link alone, which is the only case it was ever built to
reach.

**The index was read and forgotten.** Only mutation keys carry their hydrated revision into the commit
condition, so nothing guarded the index revision the decision rested on. An `UnlinkCredential` committing
between hydrate and commit would tombstone index and link, and this batch would then publish the edge live
on top of it — and that state is unrecoverable rather than merely stale, because a later `UnlinkCredential`
rejects `not-found` once the array entry is gone. The lens would project a credential the person removed,
permanently. The index now joins the committed footprint, conditioned on the revision it was read at, so
the race sends the op back through the OCC retry and it rejects `not-bound` on re-hydration.

**A test that passed for the wrong reason.** `SelfLoopRejects` named an owner that has no index vertex, so
`not-bound` caught it and the assertion held with the self-loop guard deleted. Every rejection test now
asserts the script's own outcome word through `SubmitAndAwaitReply`, and each guard was falsified by
deleting it and watching the test go red.

**Three smaller ones, all from the correctness lane.** A merge writes an index vertex for the primary's own
implicit self-credential before reaching its self-loop guard, so a merged corpus would earn a rejection on
every run and the migration could never report clean — the driver skips that shape. The link read treated
any `KVGet` error as "no edge", silently turning an infra fault into work. And JSON mode exited 0 on a run
that rejected everything.

**Filed, not fixed:** the `credentialindex` vertex survives a shred carrying `{actorKey, identityKey}` in
plaintext — the same correlation the `boundTo` tombstone exists to destroy. This op no longer acts on it,
but the vertex is still readable, and that is an erasure gap independent of this increment. Also filed: an
identity whose only sign-in is a raw actor `ProvisionConsumerIdentity` minted has no index vertex at all,
so no reconcile reaches it and `identityCredentialBindingsRead` cannot list it — which binds on 2b-2's pane.

### 15.7 CHECKPOINT — Inc 2b-1 shipped; Inc 2b-2 is next

**Shipped** `5d464007` + `be513dbe`: the `ReconcileCredentialBinding` op, the `lattice identity
reconcile-bindings` driver, nine tests, and the review above. Worktree removed — 2b-2 takes a fresh one.
Live on the dev stack: identity-domain 0.13.1 → 0.14.0 diff-applied, the migration run, **0 → 6 `boundTo`
edges** (7 index vertices scanned, 1 tombstoned and correctly skipped), a re-run reporting 6
`alreadyLinked` / 0 submitted, and `verify-package-identity` green at 99 assertions.

**Next: Inc 2b-2** — the `signInMethods` pane section over `identityCredentialBindingsRead`,
`UnlinkCredential`'s `OpMetaSpec`, and the `unprojected-input` exemption retirement (the descriptor and the
exemption removal must land together; S1 fails an op that carries both). §7's ordering rule is now
satisfied. Note for that fire: there is **no consumer-facing `PaneSpec` today** — `staffWorklist` is offered
to `frontOfHouse` only — so the section needs a new pane offered to `consumer`, and the pane lens projects
over `holdsRole → offeredTo`. Both filed rows above bind on that pane: a person the reconcile cannot reach
sees an incomplete list, not an empty one, which is the harder failure to notice.
