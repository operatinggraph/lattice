# The credential-binding plane — one lifecycle for "credential A signs in as person U"

**Status: 📐 awaiting-Andrew (ratification)** · Designer fire 2026-08-03 · owners: privacy-base,
identity-domain, identity-hygiene, edge-manifest, Gateway · Size **M–L** (Inc 1 S–M · Inc 2 M · Inc 3a XS) ·
Imp **★★** · adversarial pass run and folded (§12)

## Ratification (ratify session 2026-08-06)

**Status: Inc 2 + Inc 3a ✅ SHIPPED (2026-08-08, build note §13) · Inc 1 🗄️ HELD AND REDIRECTED (Andrew) ·
Inc 3b deferred.** Read this section before the body: §7's fork is superseded, §3's mechanism moves to
a different plane, and §4.3's instruction was corrected at build (a dated strike stands in place).

**Increment 1 is held, and the fork is dissolved rather than answered.** §7 asked *"what does
`ShredIdentityKey` erase?"* and offered A (PII-only) / B (full unbind) / C (cascade). Andrew rejected the
question's framing: **an op named for shredding a key should shred the key, and a "forget me" request is an
orchestrated multi-op process — a Loom pattern, with the Weaver for convergence where needed.** Three
grounded facts make that unarguable, and all three are live today:

- The op is **already five jobs in one atomic batch** (`shred_identity_key.go:315-355`): it marks `piiKey`
  shredded, then runs three paged enumerations (`MAX_INDEXES_PAGES = 64` and siblings at `:179/:205/:233`)
  tombstoning every owned `identityindex` vertex plus its `indexes` link, every `duplicateOf` link and
  every `boundTo` link, then emits `privacy.keyShredded`. Increment 1 proposed adding three more.
- **It can refuse to erase.** The pre-flight cap at `:321-330` fails with `ShredBatchTooLarge` above 999
  mutations, so a sufficiently-connected person **cannot be erased at all**. A right-to-erasure obligation
  that can be declined because the subject has too many links is not a guarantee; it is a size limit on who
  may be forgotten.
- **A pattern instance already exists, hand-rolled.** `RecordShredFinalization{identityKey, step:
  vaultKeyDestroyed|projectionsNullified}` (`:93`, handler `:356`) is called by `internal/privacyworker`
  after `Vault.ShredKey` and by `internal/refractor/keyshredded` after its nullify targets succeed; a
  `shredStatus` lens projects progress, and `:292-293` resets it because *"a (re-)shred starts a NEW
  finalization cycle"*. Durable step state, multiple actors, a progress projection, cycle semantics — every
  element of a pattern instance except the orchestrator.

**What that changes for §7.** The question becomes *which steps belong in the erasure pattern*, and B's
intent survives intact as the step list. **Option C is unblocked rather than refused**: the anti-cascade
argument (`retention-class-key-custody-design.md` §8.6, and §9.1 of the design it replaced) is against an
**unattested in-batch walk** whose enumeration completeness *is* the guarantee. Under a pattern each step
attests what it covered and a residue lens detects an identity whose key is shredded but whose
`boundTo`/`credentialindex`/index rows are still live — incompleteness becomes detectable and repairable,
which is the platform's own detect-and-recover doctrine rather than a weaker promise. C's remaining
preconditions are (i) `credentialindex` reachability made structural, and (ii) a way for a key to actually
leave the keyspace — a tombstoned `lnk.identity.<credId>.boundTo.identity.<uId>` still names both parties
**in its own key**, so erasure-completeness may be the real revive trigger for the shelved hard-delete verb.
All of this is `erasure-orchestration-design.md`.

**Increments 2 and 3a are ratified as they stand** — both are op-local and have nothing to do with erasure.
Inc 2 is not hygiene: §4.3's `MergeIdentity` case is a **live shipped defect**, verified this session at
`packages/identity-hygiene/ddls.go:595-618` — the `credentialindex` write sits unconditionally at the top
of the loop while the guard that skips the self-`boundTo` link `continue`s below it, so when the primary is
itself a credential of the secondary the merge writes a self-index, and `ClaimIdentity` reads exactly that
vertex as `credential-already-bound`, permanently blocking that identity's own future claim. Inc 3b
(`identityIdpBindingsRead`) stays designed-not-built behind the first configured external key source.

**Two corrections folded at ratification.** §5.2 labels Inc 3a *"(built)"* — it is not; `panes.go:77` still
ships the old false-completeness string, and nothing in this design has shipped. And §7.3's blocker cites
the in-flight subject-anchored design, which Andrew held and replaced the same session; the anti-cascade
argument survives verbatim in `retention-class-key-custody-design.md` §8.6, so the blocker holds and only
the citation moves.

**§6.2's contract sharpening: its stated blocker is gone, and it is still not staged.** It was held because
Contract #3 carried the subject-anchored proposal and "one diff must mean one decision" — that proposal was
reverted this session, so the file is clean. It stays unstaged deliberately: the sentence imposes a MUST on
authors that Inc 2 is what satisfies, so it should ride the Inc-2 **build** fire, where text and behavior
land together, rather than sit in the contract asserting a rule no script yet follows. Frozen-contract
commits are Andrew's regardless of the delegation covering these increments.

---

## For Andrew

**What it does.** The fact *"credential A signs in as person U"* is carried in **four** places, written by
four ops that keep them in step on the bind paths and by `UnlinkCredential` on the unbind path. Two other
paths do not: **a crypto-shred retracts one of the four**, and **first-touch provisioning writes none of
them**. This design gives the plane a single lifecycle — erase it completely, refuse to write half of it,
and stop the read path making a completeness claim it cannot support. It folds the three filed rows
(*shred leaves the index vertex*, *a credential whose identity vertex was never provisioned*, *a provisioned
raw actor is unlistable*) into one item, because each is the same plane failing at a different boundary and
fixing them separately would settle the same modelling question three times, differently.

**One architectural fork — §7.** *What does `ShredIdentityKey(U)` erase: U's **PII**, or the **person**?*
The shipped behaviour answers neither: it tombstones the `boundTo` edges (so U's own sign-in-methods pane
goes empty) while the `credentialindex` vertex stays live and the Gateway's operational seam keeps resolving
A → U (so U keeps signing in). **My recommendation: option B** — a shred fully unbinds every credential
(index, edge, operational seam), which is Increment 1. **Option C** — additionally cascading the key
destruction to each bound credential identity, the only thing that makes the erasure guarantee *true* for an
IdP-backed person — is designed in §7.3 and **deliberately not recommended for ratification yet**: the
adversarial pass proved my own `boundTo` enumeration incomplete in two shapes, which is precisely the
argument the in-flight [subject-anchored-sensitive-aspects](subject-anchored-sensitive-aspects-design.md)
design §9.1 makes against any shred cascade. Ratifying C today would ratify two contradictory positions on
the same mechanism. §7.4 names the precondition that would make C ratifiable.

**No frozen-contract change is staged**, and one is deliberately **held rather than written** — §6.2.
Contract #3 §3.5's judgement that *"a dangling link is low-harm … an absent endpoint reads as nothing"* has a
named exception this design proves, and the proposed one-paragraph sharpening is written verbatim in §6.2.
I did **not** apply it: `docs/contracts/03-mutation-batch-event-list.md` already carries another design's
uncommitted proposal (and `01`, `02`, `06` carry two more). Two proposals in one file's diff makes both
ambiguous at ratification. Apply mine when you reach it, or tell me to stage it once §3.10 lands.

**What is deliberately not built.** The `identityIdpBindingsRead` Secure Lens (§5.3, Inc 3b) — its entire
population is unreachable until a configured external key source exists, proven end to end in §5.3.
Designed, sequenced, not built.

**One thing the review found that nobody had filed**, and which is folded into Inc 2 rather than filed
again: `MergeIdentity` writes a **self-referential `credentialindex`** for the primary
(`identity-hygiene/ddls.go:596-618` — the index write is appended *before* the self-loop `continue`). That
single vertex (a) is unreachable by any decrypt-free erasure enumeration, (b) permanently blocks that person
from ever claiming another identity (`credential-already-bound`, **G13**), and (c) is why
`lattice identity reconcile-bindings` carries a bespoke skip. Three symptoms, two lines.

---

## 1. Problem + intent

Three rows on `backlog/lattice.md`, all ★★, all filed out of the client-ceremony build (§15.6/§16.2/§16.6 of
[client-ceremony-op-descriptors-design.md](client-ceremony-op-descriptors-design.md)):

| Row | What it says |
|---|---|
| `[privacy-base] A shred erases the credential→person link but leaves the index vertex naming both` | `ShredIdentityKey` tombstones every `boundTo` link because it names in plaintext which credential belonged to whom; `vtx.credentialindex.<hash>` carries the same `{actorKey, identityKey}` and is in no erasure set. |
| `[identity-domain] A credential whose identity vertex was never provisioned projects no binding row` | `identityCredentialBindingsRead` anchors on `(c:identity)`; reachable via `lattice identity claim --actor`, which skips the Gateway's provisioning pre-flight. |
| `[identity-domain] A provisioned raw actor has no credentialindex, so its sign-in method is unlistable` | `ProvisionConsumerIdentity` writes the identity vertex and `.idpBinding` only. An incomplete list is harder to notice than an empty one. |

They read as three unrelated holes. They are one: **the plane that carries "A signs in as U" has four
representations and no lifecycle that covers all four.**

### 1.1 The four representations, and who writes them

| # | Representation | Where | What it is for |
|---|---|---|---|
| 1 | `vtx.identity.<U>.credentialBinding` — a **sensitive** aspect holding `{actorKey, boundAt, credentials[]}` | Core KV, encrypted under U's DEK | the person's own record of their bound set; the source `identityCredentialsRead` decrypts |
| 2 | `vtx.credentialindex.<sha256NanoID(A)>` — `{actorKey, identityKey, boundAt}`, **plaintext** | Core KV | the one-credential-≤-one-identity guard (`ClaimIdentity` rejects `credential-already-bound` off it) and `ReconcileCredentialBinding`'s authority |
| 3 | `lnk.identity.<A>.boundTo.identity.<U>` | Core KV (graph) | the **only projectable** form — `identityCredentialBindingsRead` fans one row per edge |
| 4 | `credential-bindings` KV, key `A` → `{identityKey: U}` | operational KV, **outside** Core KV (P1) | the Gateway's and every app read boundary's live A → U resolution (`internal/gateway/credentialbinding`) |

**Bind** paths write 1+2+3 in one atomic batch and emit the event that folds 4:

- `ClaimIdentity` (`packages/identity-domain/ddls.go:1209-1247`) — 1, 2 (`credential_index_mutation`),
  3 (`credential_bound_to_mutation`), and `identity.claimed` → 4.
- `CompleteCredentialLink` — the same three plus the array append, `identity.claimed` → 4.
- `MergeIdentity` (`packages/identity-hygiene/ddls.go:595-623`) — repoints 2 and 3 per credential, emits
  `identity.rebound` → 4.
- `ReconcileCredentialBinding` (shipped `5d464007`) — converges 3 onto 2 for one credential.

**Unbind** retracts all four: `UnlinkCredential` tombstones 2 (`ddls.go:1554`) and 3, rewrites 1, and emits
`identity.unbound` — which `credential_bindings_materializer.go:143-153` folds as the plane's one explicit
row-set shrink, deleting 4.

### 1.2 The two paths that do not

**Erasure.** `ShredIdentityKey` (`packages/privacy-base/shred_identity_key.go:349-351`) tombstones **3
only**. 2 stays live in Core KV, in plaintext, naming both endpoints. 4 keeps resolving A → U — so an
erased person keeps signing in, keeps U's grants, and `GET /v1/actor` keeps answering
`resolvedActorId: U`, while their own sign-in-methods pane (shipped `ec87b8f4`, confirmed live in §17.2 of
the client-ceremony doc) renders **empty**, because the only projectable representation is the one that was
retracted. That is not a partial erasure; it is an erasure that removed the one representation the *person*
could see and left the two the *platform* acts on.

**First touch.** `ProvisionConsumerIdentity` (`ddls.go:1111-1140`) writes the identity vertex, `.state`, the
`holdsRole` grant and — in opaque mode only — the sensitive `.idpBinding`. None of 1–4. That is correct
(there is no second vertex to bind; the person *is* the credential), and it is why the pane is empty for
every self-serve consumer who never claims a staff-created identity. In production under Contract #11's
`opaque` binding that is the **default** signup shape, not an edge case.

### 1.3 And one write that emits half the plane without checking it can

`ClaimIdentity` and `CompleteCredentialLink` emit representation 3 with the source endpoint set to
`op.actor` — and **never read or validate `op.actor`'s own vertex**. On the Gateway path the vertex usually
exists, because `provisionActorIfNeeded` commits `ProvisionConsumerIdentity` ahead of the op (this is also
how Facet's freshly-minted device credential `a2Key` comes to exist — `cmd/facet/credentials.go:136-169`).
"Usually" is doing real work in that sentence and §4.4 pays for it. Off the Gateway —
`lattice identity claim --actor`, or any direct NATS submitter — the vertex need not exist at all, and the
write commits a `boundTo` link whose source vertex does not. The lens then anchors on `(c:identity)` and
yields nothing, silently.

---

## 2. Grounding ledger

Every load-bearing claim, pinned to the code that **does** the thing. Rows marked ⟳ were corrected by the
adversarial pass (§12) — the original citation was a comment, a wrong line, or a misquote.

| # | Claim | Evidence |
|---|---|---|
| G1 | The shred tombstones `boundTo` and nothing else on this plane | `privacy-base/shred_identity_key.go:317-351` — the `idx_hits`/`dup_hits`/`bound_hits` loops; no `credentialindex` key is derived anywhere in the script |
| G2 | A `tombstone` **preserves the prior body**; an `update` with `isDeleted: True` replaces it | `internal/processor/step8_commit.go:414-421` — `m.Op == "tombstone"` copies `prior` whole; `update` starts from an empty map |
| G3 ⟳ | `kv.Links` delivers tombstoned links, and the page limit counts them | `internal/processor/starlark_kv.go:304-336` — `connLinkLister.ListLinks` runs `KVListKeysFilter` over the Core KV bucket (a tombstone is a soft PUT, so the key is still listed), GETs each, and `parseLinkDoc` (`:341-347`) carries `IsDeleted` through without filtering. The limit is applied at the **key** level, before any `isDeleted` test |
| G4 | `crypto.sha256NanoID` is available to **every** DDL script | `internal/processor/starlark_builtins.go:114-126`; bound for every compiled script (`compiled_script.go:146`) and for `derive_reads` (`derive_reads.go:116`) |
| G5 ⟳ | Step 7 validates an event class's **shape** only — no ownership, no registration check | `internal/processor/step7_events.go:62-67`; independently `docs/contracts/03-mutation-batch-event-list.md:160` — *"The Processor does not resolve an event-type DDL or schema-validate `event.data` at commit."* |
| G6 ⟳ | Cross-package emission of an `identity.*` event is shipped precedent | `packages/identity-hygiene/ddls.go:671-682` emits `identity.rebound` and `identity.merged`; identity-hygiene owns neither the `identity` domain nor those classes, and no `identity.*` class is registered as an event-type DDL anywhere in `packages/**` |
| G7 ⟳ | The materializer folds `identity.unbound` as a **delete**, keyed on `payload.actorKey` alone | `internal/gateway/credential_bindings_materializer.go:129-162`; the sole `KVDelete` is `:143-153`, and deleting an absent row is a documented no-op |
| G8 | The platform performs **no** link-endpoint validation; it is the script's duty | `docs/contracts/03-mutation-batch-event-list.md:84-88` and `:158`; `grep -n "sourceVertex" internal/processor/step6_validate.go` returns nothing |
| G9 | Re-anchoring the bindings lens on the owner does not help | `internal/refractor/ruleengine/full/executor.go` — `traverseRel` extends a binding with the **neighbour node**, so `c` must resolve either way |
| G10 | `UNION` is refused by the full engine's visitor | `internal/refractor/ruleengine/full/visitor.go:71-73` — `v.fail("UNION is not supported")` |
| G11 | `derive_reads` output merges into the declared set for **every** dispatcher; an envelope declaration wins, and an `egressReads` collision hard-errors | `internal/processor/derive_reads.go:285-306`. No dispatcher declares `op.actor` for these ops — `cmd/facet/claim.go:121`, `cmd/facet/credentials.go:159-167`, `cmd/loftspace-app/credentials_link.go:134-156`, `cmd/lattice/identity/identity.go:225-232` |
| G12 ⟳ | No lens projects `credentialindex`; the two non-package readers are whoami's key **computation** and the reconcile CLI, which tests `IsDeleted` | `grep -rn "credentialindex"` over `internal/`, `cmd/`, `packages/*/lenses.go`; `whoami.go:102` computes and never reads; `cmd/lattice/identity/reconcile.go:28` (`credentialIndexDoc.IsDeleted`) |
| G13 | Writing a `credentialindex` for an actor **blocks that actor from ever claiming** | `packages/identity-domain/ddls.go:1177-1181` — `ClaimIdentity` rejects `credential-already-bound` when `credential_index_key(op.actor)` is live, and the Gateway provisions A before A claims U |
| G14 ⟳ | The self-loop `boundTo` shape is already refused in code | `packages/identity-hygiene/ddls.go:618` — the `continue`; the reason is stated in the comment at `:611-614`. Used in §5.1 as precedent by analogy, not as direct evidence for a raw actor |
| G15 | `.idpBinding` is encrypted under **A's** DEK, and `ShredIdentityKey(U)` never touches A's key | `internal/processor/step65_encrypt.go:68,87` — `Encrypt(ctx, vertexKey, …)` keys off the aspect's own parent vertex; the shred's only piiKey write is `identity_key + ".piiKey"` (`shred_identity_key.go:278`). A live operator read path exists: `cmd/loupe/vault.go:118` |
| G16 | Contract #11 `opaque` mode derives `A = SHA256NanoID("idpsub:…")` and stores the raw `(iss, sub)` **only** in `.idpBinding`; the dev signer mints no `iss` | `docs/contracts/11-external-actor-authn.md:80-112`; `internal/appsession/signer.go:64-72` |
| G17 | **No DDL registers class `credentialindex`**, so step 6's `permittedCommands` block is never reached and privacy-base may write the vertex | `internal/processor/step6_validate.go:156-197` (`resolveGoverningDDL` misses → Contract #1 §1.5 permissive default); identity-domain declares `identity, ssn, dob, name, email, phone, claimKey, linkKey, credentialBinding, idpBinding, indexes, duplicateOf, boundTo` and no index class |
| G18 | An event's `targetKey` is script-settable via `data.targetKey`, else defaults **positionally** to `result.Mutations[i].Key` | `internal/processor/step7_events.go:45-50, 72-80` |
| G19 | `UnlinkCredential` retracts the index with the **`tombstone` verb**, so its body survives in plaintext | `packages/identity-domain/ddls.go:1554` + **G2** |
| G20 | `kv.Links` charges `1 + clamped limit` per page — the *limit*, not the number of links returned | `internal/processor/starlark_kv.go:207, 222`; `DefaultLiveReadBudget = 60_000` (`live_read_budget.go:26`) |
| G21 | The three derivations of the index key hash the **same** input (the full `vtx.identity.<id>` key) | `packages/identity-domain/ddls.go:764-765`; `packages/identity-hygiene/ddls.go:596`; `internal/gateway/auth/auth.go:216-217,451` → `whoami.go:102`. `linkDocToStarlark` yields `sourceVertex` in the same form (`starlark_kv.go:285,331`) |

---

## 3. Increment 1 — the erasure reaches the whole plane — 🗄️ SUPERSEDED, DO NOT BUILD FROM THIS SECTION

> **Superseded 2026-08-06 (Andrew's redirect — see the Ratification block at the top).** Everything below in
> §3 specifies the **wrong mechanism**: it makes `ShredIdentityKey` erase *more* inside its own atomic
> batch. Andrew held exactly that: an op named for shredding a key shreds the key, and "forget me" is an
> orchestrated process. **The live design is
> [`erasure-orchestration-design.md`](erasure-orchestration-design.md)**, where the op narrows to one
> mutation, a Loom pattern owns the ordered spine, and a Weaver target over a residue lens owns the
> convergent tail with a seal as the guarantee.
>
> §3's *diagnosis* survives and is worth reading — the four representations, which paths write which, and
> why a half-erased plane is incoherent (§1.1–§1.3). Its *prescription* does not. Read it as the problem
> statement it is, never as build instructions. §3.2's "why `update`-with-empty-data rather than
> `tombstone`" reasoning is carried into the replacement design; §3.4's plane-state table is still the
> clearest statement of what must end up true.

**Owner: privacy-base.** Size **S–M**. This is the option-B half of the §7 fork: the half that is
live-correct today regardless of how the fork resolves, and the half that makes the shred internally
consistent with itself.

### 3.1 The shape

`ShredIdentityKey` already enumerates every `boundTo` link touching the identity, in both directions, under
the sanctioned class-(e) posture. Everything below happens inside that existing enumeration — no new read
class, no new declared key, no dispatcher change.

1. **One pass, two slices.** `collect_bound_to_direction` returns `(live, all)` rather than gaining a
   *sibling* collector. Per **G20** `kv.Links` charges the clamped page **limit**, not the number of links
   returned, so a second walk of both directions would double the worst-case charge to 65,792 against a
   60,000 live-read budget and pre-empt the script's own named `IdentityBoundToFanoutTooLarge` with an
   opaque `kv.Links: live-read budget exceeded`. The `all` slice is what reaches a credential
   `UnlinkCredential` already unbound, whose index is still live-or-body-bearing. Per **G3** the pager
   already delivers and already charges for tombstoned entries, so the page caps and the fanout bound are
   genuinely unchanged.

2. **Derive one index key per distinct source.** `credential_index_key(lk.sourceVertex)` —
   `"vtx.credentialindex." + crypto.sha256NanoID(lk.sourceVertex)` (**G4**, **G21**), deduped in a dict.
   `lk.sourceVertex` is the credential on an inbound link and the identity itself on an outbound one.

3. **Read, guard, scrub.** Per derived key, a class-(e) per-candidate follow-up `kv.Read` — the identical
   posture `collect_owned_indexes`'s loop already uses for a data-derived index key
   (`shred_identity_key.go:334-338`). Then:

   - **skip an absent one;**
   - **skip one whose body is already empty** (`data == {}` or absent) — this is the idempotent-re-shred
     test, and it is deliberately **not** `isDeleted`. Per **G19** `UnlinkCredential` retracts the index with
     the `tombstone` verb, which preserves `{actorKey, identityKey, boundAt}` whole; skipping on `isDeleted`
     would walk straight past the plaintext pair the erasure exists to destroy, for the entire
     already-unbound population;
   - **skip one that names neither side of this identity.** The guard is two clauses, not one:
     `data.identityKey == identity_key` (inbound — A is a credential of U) **or**
     `data.actorKey == identity_key` (outbound — U is itself a credential of somebody). A single
     `identityKey` clause makes the outbound arm dead code, because an outbound index's `identityKey` is by
     construction the *other* identity. The case the guard exists to refuse survives both clauses: a
     credential unbound from U and re-bound to V has a live index `{actorKey: A, identityKey: V}` with
     `A ≠ U` and `V ≠ U`, and is correctly left alone;
   - otherwise emit

     ```
     {"op": "update", "key": cred_index_key, "expectedRevision": existing.revision,
      "document": {"class": "credentialindex", "isDeleted": True, "data": {}}}
     ```

     **`expectedRevision` is required, not decorative.** `kv.Read` is a lazy read and is explicitly *"NOT a
     serialization point"* (`starlark_kv.go:146-147`); the shred's batch otherwise contends only
     `U.piiKey`, which a concurrent `ClaimIdentity` by A touches not at all. Unconditioned, the sequence
     *shred reads `{A,U}` → A claims W, reviving the index to `{A,W}` → shred blind-Puts a scrub* **erases a
     live, unrelated binding** and leaves A bound-by-edge-and-bucket to W with a dead index that
     `ReconcileCredentialBinding` will refuse to revive. Every enumeration race the script accepts today
     loses in the *under*-erase direction; this is the first one whose loser is a third party, so it takes
     the OCC condition and the ordinary commit-path retry. `.revision` is exposed on the read
     (`starlark_kv.go:280-287`).

4. **Emit `identity.unbound` per scrubbed index — not per live edge.** The event carries
   `{identityKey, actorKey, targetKey}` read off the index body, so the outbound arm emits
   `{identityKey: P, actorKey: U}` and the inbound arm `{identityKey: U, actorKey: A}`. Keying emission to
   *live edges* would strand bucket 4 for exactly the shapes step 3 exists to reach (an unbound-but-live
   index; a merged-away identity whose outbound edge is already tombstoned) — leaving an erased person still
   resolving to their owner, which §3.3 shows is worse than doing neither half. The materializer deletes on
   `payload.actorKey` alone and no-ops on an absent row (**G7**), so over-emission is safe and
   under-emission is not. `targetKey` is set **explicitly**: per **G18** the positional default would stamp
   events 1..N with whatever `identityindex`/`indexes`/`duplicateOf` mutation happened to sit at that index.

5. **The caps.** `total_muts` gains `len(scrubbed)`. The event list is bounded by the same set and must be
   counted against `substrate.MaxBatchMessages` (`internal/substrate/batch.go:203,282`), which the current
   999-mutation pre-flight does not cover — the pre-flight becomes `mutations + events`.

### 3.2 Why `update`-with-empty-data rather than `tombstone`

Per **G2** the `tombstone` verb carries the prior document over whole, so a plain tombstone would leave
`{actorKey, identityKey, boundAt}` readable in the body — the exact plaintext pair the erasure is for. An
`update` carrying `isDeleted: True` and `data: {}` replaces the body. It is also the shape the shred already
uses for an `identityindex` vertex; the difference is that that call re-supplies `idx_data`, which it can
afford because an `identityindex` body is `{contactType, identityKey}` with the contact plaintext already
absent from it. A `credentialindex` body is the one place on this plane where a **plaintext, dereferenceable
vertex key** sits under a hashed key, so preserving it defeats the hashing.

**What survives the scrub, stated plainly.** `preserveImmutableFields` (`step8_commit.go:439,457-470`) keeps
the creation triplet: `createdBy` names A alone (redundant with the key, which is `sha256NanoID(A)`), but
`createdByOp` points at an op-tracker record whose `data.mutationKeys` (`step8_commit.go:138-151`) lists
`vtx.identity.<U>.credentialBinding` and `lnk.identity.<A>.boundTo.identity.<U>` — the pair, in plaintext.
That record carries a 24 h TTL (`step8_commit.go:86`), so the residual is bounded rather than permanent, but
it is a residual and this design does not close it.

**What this does not buy.** A Lattice tombstone is a soft PUT: the key survives, and
`lnk.identity.<A>.boundTo.identity.<U>` **encodes the pair in the key itself**. At the *substrate* level —
reading the KV bucket or JetStream history directly — the correlation survives every erasure the platform can
currently perform, before and after this increment. That residual is the `Hard-delete mutation verb` row's
territory (a real `DEL`) and the `Keyed identity-index hashes (HMAC)` row's, both shelved on a production
threat model. The guarantee this increment restores is the **live-graph** one the shred's own comment
claims: *no live key answers the question*.

### 3.3 What Inc 1 changes for a shredded person

| representation | today | after Inc 1 |
|---|---|---|
| 1 `U.credentialBinding` | live ciphertext, undecryptable once the DEK dies | unchanged — rendered **undecryptable**, never retracted (§7.2 does not claim otherwise) |
| 2 `credentialindex` | **live**, `{A, U}` plaintext (or tombstoned-with-body after an unlink) | tombstoned, body scrubbed, OCC-conditioned |
| 3 `boundTo` | tombstoned | tombstoned (unchanged) |
| 4 `credential-bindings` | **live**, A → U | deleted, on both arms |
| A's next sign-in | acts as **U**, with U's grants | acts as **A** (the documented deny-safe fallback) |
| A's ability to bind elsewhere | refused (`credential-already-bound`) | permitted — the same state `UnlinkCredential` leaves |

Rows 2 and 4 must move **together**. Tombstoning the index alone would free A to claim a different identity
while bucket 4 still resolved A → U; emitting `identity.unbound` alone would drop the resolution while the
index still pinned A to an erased person. Either half on its own is worse than neither — which is why §3.1(4)
keys emission to the scrubbed set rather than to live edges.

### 3.4 Plane states, and the algorithm on each

| state | outcome |
|---|---|
| never bound | no edge, no index — no-op |
| bound | index derived (inbound), `identityKey == U` → scrubbed, `identity.unbound{U, A}` |
| unbound then re-bound to the same identity | live index `{A, U}`, live edge → scrubbed |
| unbound then re-bound to a **different** identity | live index `{A, V}`, tombstoned A→U edge → **skipped** (this is the guard's whole job) |
| unbound, never re-bound | index tombstoned **with body** → the empty-body test lets it through → scrubbed |
| merged, survivor shredded | live L→S edge, `index(L).identityKey == S` → scrubbed |
| merged, merged-away side shredded | outbound edge, `index(S).actorKey == S` → scrubbed via the second guard clause, `identity.unbound{P, S}` emitted |
| `MergeIdentity`'s self-index (`{P, P}`, **no edge**) | unreachable — closed at the source by Inc 2, §4.3 |
| shredded then re-shredded | every index already has an empty body → all skipped; byte-identical no-op |

### 3.5 Test strategy

Package tests in `packages/privacy-base` driven through the real Processor: a shred with two bound
credentials scrubs both index bodies and emits two `identity.unbound` events with explicit `targetKey`s; an
**unbound** credential's `tombstone`-preserved body is scrubbed (falsified by reverting the predicate to
`isDeleted` and watching it go red); a credential re-bound to a second identity keeps its index (falsified by
deleting the guard clause); a merged-away identity's own index is scrubbed **via the outbound arm** and its
bucket row dropped (falsified by deleting the `actorKey` clause); a second shred is a byte-identical no-op;
the mutation+event cap counts both. Plus an `internal/gateway` test that the materializer's fold deletes the
bucket row on a shred-emitted `identity.unbound` — the transport end to end, not asserted from the event body
alone. `make test-crypto-shred` is the increment's own e2e gate.

---

## 4. Increment 2 — the endpoint the script never validated

**Owner: identity-domain + identity-hygiene + `cmd/lattice` (+ a Gateway fail-closed).** Size **M**.

### 4.1 The shape

Contract #3 §3.5 is explicit (**G8**): the Processor performs *no* endpoint resolution, and
*"a `create` on a link key … that must guarantee its endpoints … exist declares those vertices in
`contextHint.reads`; … the script validates each … before emitting the mutation."* `ClaimIdentity` and
`CompleteCredentialLink` must guarantee it — their emitted edge is the sole input to a projection whose
product claim is completeness — and they do not. This increment discharges the duty the contract already
assigns, so **no contract change is required** (see §6.2 for the wording sharpening recommended anyway).

- `derive_reads` gains `op.actor` under **`optionalReads`** on both branches. Per **G11** it reaches every
  dispatcher — browser, Facet, CLI, Gateway — with no client change, and no dispatcher declares it already.
  `optionalReads`, never `reads`: absence must be *rejectable by the script with a named outcome*, not a
  `HydrationMiss` fault that reads as a wiring error.
- `execute` gains, before the mutation list is built:
  `if not vertex_alive(state, op.actor): fail_claim("credential-not-provisioned")` — the same
  `ClaimKeyInvalid` outcome family the branch already uses, so `opwire.go:187`'s generic mapping is
  unchanged and no new error code enters the wire.
- `cmd/lattice/identity` gains `lattice identity provision --actor <A>`, submitting
  `ProvisionConsumerIdentity{targetActorKey, consumerRoleKey}` under the CLI's own credential (which already
  carries operator authority for `create-unclaimed`), mirroring the Gateway's pre-flight at the authority
  level the op requires. The rejection message names it.

### 4.2 Why reject rather than create the vertex

Minting an identity vertex is `ProvisionConsumerIdentity`'s job, and that op is granted `scope=any` to
`identityProvisioner`/`operator` precisely because minting identities is privileged. Letting `ClaimIdentity`
mint one would make **a claim secret sufficient to create an arbitrary `vtx.identity.<NanoID>`**, with a
caller-chosen key, under a `scope=self` grant — a strictly larger write authority than the ceremony was ever
granted — and it would produce a *half-provisioned* identity (no `.state`, no `holdsRole`) that nothing else
in the corpus produces. Rejecting is also the fail-closed direction on a boundary where omission currently
succeeds silently.

### 4.3 The `MergeIdentity` self-index, closed at the source

`packages/identity-hygiene/ddls.go:595-618` appends the `credentialindex` write **before** the self-loop
`continue`, so a merge where the primary is itself a credential of the secondary mints
`vtx.credentialindex.<sha(P)>` = `{actorKey: P, identityKey: P}` with **no `boundTo` edge**. Three
consequences, all live: it is unreachable by any decrypt-free erasure enumeration (§3.4's one open row); by
**G13** it permanently rejects that person's every future `ClaimIdentity` with `credential-already-bound`;
and it is the reason `lattice identity reconcile-bindings` carries a bespoke skip for the merged shape
(client-ceremony §15.6).

**~~The fix is to move the index write below the self-loop test.~~ Corrected at build, 2026-08-08: moving
the write is necessary but NOT sufficient, and on its own does not deliver §4.7's requirement that the merge
"leaves that person able to claim."** The index the merge would rewrite is **already live** — the primary is
a credential of the secondary, so `vtx.credentialindex.<sha(P)>` = `{actorKey: P, identityKey: S}` exists
from the original bind. Omitting the write leaves that vertex standing, and **G13**'s guard reads liveness,
not contents: the person stays refused `credential-already-bound`, now by a row pointing at a merged-away
identity. The retraction has to be a **tombstone** in that branch, which is exactly what the script already
does to the `boundTo` edge two lines above and for the same reason — the merge dissolves the premise that P
is a credential of anything. A tombstone over a key that never held a live value costs nothing, the
tolerance the surrounding comment already establishes for the edge.

A migration is **not** required to make Inc 1 correct (a stranded self-index is inert once the person is
erased), but the CLI's skip and any live self-index should be listed by a `--dry-run` reconcile pass.

### 4.4 The precondition Inc 2 makes load-bearing — and the Gateway change that pays for it

§1.3's "usually". `provisionActorIfNeeded` (`internal/gateway/gateway.go:647-712`) returns **nothing** and
swallows every failure — an unconfigured `gatewayActorKey`/`consumerRoleKey` (`:648`, a silent no-op for any
embedding that skips `ConfigureProvisioning`), a marshal/NanoID failure (`:659-668`), a submit error
including a deadline on the shared request context (`:700-705`), and a non-Accepted reply (`:706-710`). In
every one the request **proceeds to the claim**. Today the claim commits, writing the dangling edge this
design exists to close. After the guard it hard-rejects, and `ClaimKeyInvalid` is deliberately generic for
anti-enumeration reasons, so a transient provisioning hiccup would surface to the person as *"invalid claim
key"* — and Facet/LoftSpace retry only on `isTransientAuthLag`
(`AuthDenied/{NoCapabilityEntry,OperationNotPermitted}`), so the ceremony breaks out immediately
(`cmd/facet/claim.go:135`).

So Inc 2 also makes the pre-flight honest: **`provisionActorIfNeeded` returns an error, and the write path
(`gateway.go:484`) fails the request `503` when it cannot establish the actor.** `whoami` (`:95`) keeps
ignoring it — a best-effort identity probe should not 503 — and a 503 is exactly what every client already
retries. This is the fail-closed point; the script guard is the backstop for submitters that never go
through the Gateway at all.

One case is correctly **permanent**, not transient: a **tombstoned** actor vertex takes
`ProvisionConsumerIdentity`'s deliberate `tombstoned stays tombstoned` no-op (`ddls.go:1081-1082`), the
Gateway records the actor as provisioned (`gateway.go:711`) and never retries in-process. A revoked
credential must not be able to claim, so the rejection is right — but it needs the DDL description and the
Gateway's log to say *revoked*, not *invalid claim key*.

### 4.5 Migration + blast radius

Zero **data** migration: §16.6 measured the live corpus at 6 `boundTo` edges, 6 rows in
`read_identity_credential_bindings`, 6 rows in `read_identity_credentials` — every credential vertex exists.

The cost is **test fixtures, and it is about twice what the filed rows implied**. The adversarial pass
counted, per package:

| package | `ClaimIdentity` | `CompleteCredentialLink` |
|---|---|---|
| `packages/identity-domain` | 68 | 38 |
| `packages/identity-hygiene` | 6 | 1 |
| `internal/refractor` (4 e2e files) | **36** | — |
| `cmd/loftspace-app/credentials_link_test.go` | — | **21** |
| `internal/gateway/gateway_test.go` | 5 | 5 |
| `internal/processor`, `internal/pkgmgr`, `internal/bypass`, `internal/substrate`, `cmd/lattice`, `cmd/facet` | 15 | 1 |

Concretely falsified rather than inferred:
`internal/refractor/refractor_claim_batch_real_op_e2e_test.go:169` builds a consumer actor key, seeds it
**only** into the capability KV (`:185`), submits the real `ClaimIdentity` (`:232`) and requires
`OutcomeAccepted` (`:248`). Under Inc 2 that fails, in four sibling e2e files.

And the helper does not transfer: `seedDirectIdentity` (`packages/identity-domain/testhelpers_test.go:409`)
is an **unexported `_test.go`** helper, unusable from the eight other affected packages, and it force-writes
a `.state` aspect a credential actor never otherwise has — so it is not the minimal seed the migration
wants. Each package seeds its own bare `vtx.identity.<A>`; the shared shape, if one is wanted, belongs in
`internal/testutil`.

**Inc 2's gate is `go test ./...`, not a three-package list.** This is a change to a write path eight
packages exercise.

### 4.6 Why there is no lint gate here

The standing rule is that a design establishing a convention ships the gate that enforces it. This design
establishes none. Contract #3 §3.5 does not say *every* link emission must validate its endpoints — it says
one that **must guarantee** them does, and it explicitly tolerates the dangling case as low-harm. A lint that
default-denied the bare link-emission idiom would contradict the frozen contract, not enforce it, and would
default-deny roughly every link write in `packages/**`. The enforceable rule here is the contract's own; the
honest fix is to sharpen its sentence so the next author sees the exception, which is §6.2.

### 4.7 Test strategy

`ClaimIdentity` and `CompleteCredentialLink` with an unprovisioned actor reject `ClaimKeyInvalid:
credential-not-provisioned` (asserted through `SubmitAndAwaitReply` on the script's own outcome word, and
falsified by deleting the guard); with a tombstoned actor vertex, likewise; `derive_reads_test.go` gains the
actor key on both branches; a Gateway test that a failed pre-flight 503s the write and does **not** 503
whoami; an identity-hygiene test that a primary-as-credential merge writes no self-index and leaves that
person able to claim; a `cmd/lattice` test that `provision` then `claim` succeeds where `claim` alone now
rejects.

---

## 5. Increment 3 — the read path stops claiming completeness it cannot support

### 5.1 The modelling question the build note deferred here

§16.2 (3) of the client-ceremony doc left the Designer one question: *is a self-binding representable?* The
answer is **no, and it must stay no** — two independently sufficient reasons, both grounded:

- **G14** — `MergeIdentity` already refuses to write a self-loop `boundTo`, with the reason written down: the
  row would list *a person as their own sign-in method* and become an `UnlinkCredential` target that can
  never succeed, because the key is not an array entry.
- **G13** — writing the *index* half is worse than useless: it permanently blocks that person from claiming.
  §4.3 is the live proof, in shipped code, that this is not a hypothetical.

So a raw actor's sign-in method is **not a credential binding** and must never be modelled as one. What it
actually is: an **IdP binding** (`.idpBinding = {iss, sub}`, opaque mode only) or, under Contract #11's
dev-only `nanoid` pin, nothing at all.

### 5.2 Inc 3a — stop asserting a falsehood

**Owner: edge-manifest, not Facet.** Size **XS**. The pane's empty state is a *descriptor field*:
`emptyCopy` on the `signInMethods` `PaneSpec` (`packages/edge-manifest/panes.go:44-91`, the string at
`:77` — *"No sign-in methods on record."*), rendered generically by `paneSectionHTML`
(`cmd/facet/web/app.js:1408-1415`). So this is package work with a version bump and
`make verify-package-edge-manifest`, and there is nothing to branch in Facet.

**It is a copy change, not a conditional.** The distinguishing signal does not exist and is not cheap to
create: `internal/appsession/session.go:430-458` resolves a bound credential up to its owner at login and
`:512-515` **drops** the `actorId != resolvedActorId` distinction; `/api/whoami` (`:578-610`) exposes
`identityId` only, and `manifest.me` (`packages/edge-manifest/lenses.go:499-530`) carries
`identityKey`/`claimed`, neither of which separates "raw actor" from "person". Inventing that signal to
choose between two strings is a poor trade.

So the fix is a string that is **true on every input**: *"No additional sign-in methods are bound to this
account."* It is true for a raw actor (their sign-in is the account itself), true for a claimed person during
projection lag, and true for the genuine zero case — where the current copy is an authoritative-sounding
claim that the person has no way to sign in, made to a person who is signed in. This covers every
environment we run, because every dev and demo consumer is nanoid-mode.

### 5.3 Inc 3b — `identityIdpBindingsRead` (designed, not built)

For an opaque-mode person the honest row is *"Signed in via `<issuer>`"*, and the plane has a place for it:
`identityCredentialBindingsRead` already carries a constant `row_kind` column that exists precisely so a
descriptor gates by declaration rather than by type accident. A second row kind, `idpBinding`, belongs in
that table — and cannot go in it. Two disjoint row shapes in one relation is a `UNION`, refused by the
visitor (**G10**), and a second lens into one table is the shared-keyspace shape that fails closed today. So
the shape is a **sibling lens and a sibling pane section**, mirroring `identityCredentialsRead`:

```
CanonicalName: "identityIdpBindingsRead", Adapter: "postgres",
Table: "read_identity_idp_bindings", IntoKey: ["identity_id"], Protected: true,
Columns: [entity_key text, identity_key text, binding jsonb, row_kind text],
SecureColumns: [{Column: "binding", IdentityKeyColumn: "identity_key"}]

MATCH (u:identity)
WHERE u.idpBinding.data <> null
RETURN nanoIdFromKey(u.key) AS identity_id,
       u.key                AS entity_key,
       u.key                AS identity_key,
       u.idpBinding.data    AS binding,
       "idpBinding"         AS row_kind,
       [nanoIdFromKey(u.key)] AS authz_anchors
```

Its own table, never `read_identity_credentials` — sharing that one would be exactly the shared-keyspace
shape this avoids. The spec validates: `validateSecureColumns`
(`internal/refractor/lens/corekv_source.go:848-902`) requires `Protected` (`:852`), forbids
`grantTable`/`projectionKind` (`:855`, `:858`), and requires both `binding` and `identity_key` among the
declared columns (`:890`, `:896`); a bare string literal in `RETURN` is already proven by
`"credentialBinding" AS row_kind`; `nanoIdFromKey` is used by both shipped specs. No op is offered on the
row — an IdP binding is not unlinkable, and `row_kind` gates that by declaration.

**One honest limitation.** The row's RLS anchor is the vertex carrying `.idpBinding` — the actor itself —
while `lattice.actor_id` is the **resolved** identity (`gateway.go:502-505`). So this row is visible exactly
to a **never-claiming raw actor**, which is the population it is for, and a claimed person will *not* see an
`idpBinding` row for the credential they claimed with. Listing a *credential's* IdP provenance to its owner
is a different feature and is not designed here.

**Deliberately not built.** Apply the dead-scaffolding test: the consumer exists (the pane shipped and is
confirmed live), the security is not stubbed — but the **population is empty and unreachable**, proven end to
end: `.idpBinding` is written only when `idpIssuer` is non-nil (`ddls.go:1129-1137`), which
`provisionActorIfNeeded` supplies from the token's `iss` (`gateway.go:655-658`), which the dev signer never
sets (**G16**). No environment we run can produce a row, and the sibling board row `[appsession] The
production IdP posture cannot open a session` is shelved on exactly the *"first real-IdP deployment"*
trigger. Ratify the design; sequence the build behind that trigger.

---

## 6. Contract surface

### 6.1 Built to, unchanged

- **Contract #1 §1.1** — the `boundTo` direction is unchanged; Inc 1 adds no link and Inc 2 adds no key
  shape. §1.5's permissive default is what admits the scrub (**G17**).
- **Contract #2 §2.5** — Inc 1's new reads are class **(e)** (per-candidate follow-up off a bounded
  `kv.Links` enumeration already declared in `ContextHint.Enumerations` by every `ShredIdentityKey`
  dispatcher, no new relation); Inc 2's is class **(g)**. Both stay inside the shipped posture. Note that
  `docs/contracts/02` currently carries the
  [declared-read-scope-authorization](declared-read-scope-authorization-design.md) design's uncommitted §2.5
  proposal — Inc 2's derived key is **the actor's own vertex**, the most trivially in-scope read a read-scope
  check could face, so the two compose. (`01` and `06` carry two further designs' diffs; the tree has four
  uncommitted contract files and none of them is mine.)
- **Contract #3 §3.10** — untouched. Inc 1 writes no sensitive aspect and does not decrypt one; the erasure
  stays decrypt-free, which is what lets it run in the shred's own commit.
- **Contract #11 §11.4** — the credential → business resolution seam is *retracted* by Inc 1 through the
  contract's own `identity.unbound` shrink, never bypassed.

### 6.2 The one sharpening I recommend, and deliberately did not stage

`docs/contracts/03-mutation-batch-event-list.md:88` currently reads:

> The Processor performs **no** independent step-6 endpoint/host resolution and emits no dangling-reference
> error code. A dangling link is low-harm — readers filter `isDeleted`, and an absent endpoint reads as
> nothing — and convergence gaps are the Weaver's detect-and-recover domain, not a fail-closed platform
> reject.

The judgement is right and has a named exception this design proves: **"reads as nothing" is the harm** when
the reader is a projection whose product claim is completeness. Proposed addition, one sentence at the end of
that paragraph:

> "Reads as nothing" is low-harm only for a reader that treats absence as absence. A script whose emitted
> link is the sole input to a projection presented as a **complete** list (a person's bound sign-in
> methods, an account's granted roles) MUST validate its endpoints under this section — for that reader an
> absent endpoint is a silent under-report, not a null.

**Held, not staged**, per the For-Andrew note: that file already carries the subject-anchored design's
uncommitted §3.10/§3.11 proposal, and one diff must mean one decision. Nothing here depends on it — Inc 2
discharges a duty §3.5 already assigns.

---

## 7. The fork — what does `ShredIdentityKey` erase?

### 7.1 The question

`ShredIdentityKey` never tombstones the identity — state stays `claimed`, roles stay granted. So the account
survives a shred. But the shred *does* retract the graph edge that says who signs in to that account, and
does not retract the operational seam that makes the sign-in work. Three internally-consistent answers exist;
the shipped behaviour is none of them.

### 7.2 The options

**A — PII-only; the binding is account structure and survives.** Then the current `boundTo` tombstone is the
defect: it is not a PII derivative the way `identityindex` (a hash of a contact) and `duplicateOf` (evidence
from comparing contacts) are — it derives from the claim ceremony, not from anything erased. Under A the fix
is to **remove** the `boundTo` tombstone, leave the index alone, and mark the filed privacy row WONTFIX.
Cheapest, and it restores the person's own sign-in list.

**B — the shred fully unbinds every credential.** Index scrub-tombstoned, edge tombstoned (as today),
operational seam dropped via `identity.unbound`. The person can no longer sign in as U. Representations 2, 3
and 4 are retracted and 1 is rendered undecryptable — *not* retracted, which §3.3 states rather than eliding.
**This is Increment 1, and it is what I ask you to ratify.**

**C — B, plus cascade the key destruction to every bound credential identity.** For each credential A of U,
also mark `A.piiKey` shredded and emit `privacy.keyShredded{A}`, destroying `.idpBinding` — the raw
`(iss, sub)` that is the person's real-world identifier.

### 7.3 Why C is where the guarantee lives — and why I am not asking you to ratify it

Under **A** and **B** alike, an operator who has erased U can still read `A.idpBinding` (encrypted under
**A's** key, which `ShredIdentityKey(U)` never touches — **G15**, with a live operator read path at
`cmd/loupe/vault.go:118`) and learn A ↔ U from the operational bucket or the tombstoned edge key. The raw
`(iss, sub)` is the real-world IdP account (**G16**). So for any IdP-backed person — *every* production
consumer under Contract #11's opaque binding — the right-to-erasure guarantee is false today and stays false
under A and B. That is the case for C, and I think it is correct.

It is not ratifiable yet, for a reason this design's own review supplied. The in-flight subject-anchored
design §9.1 rejects a shred cascade categorically: *"the cascade must enumerate 'vertices subject to this
identity' — a link walk, on the erasure path, whose completeness **is** the erasure guarantee. A missed edge
is a silent, permanent right-to-erasure failure with a success signal on it."* I drafted C arguing the
`boundTo` enumeration is bounded and therefore safe — and the adversarial pass then found **two** shapes my
enumeration missed (an outbound arm the guard silently disabled; `MergeIdentity`'s edgeless self-index). The
counter-argument landed on my own facts. Asking you to ratify C now would be asking you to ratify two
contradictory positions on the same mechanism in the same week.

### 7.4 The precondition, and Inc 4

C becomes ratifiable when the plane's completeness is **mechanism**-guaranteed rather than shape-guaranteed —
concretely, when every `credentialindex` is reachable from the identity by a walk that cannot silently miss
one. Inc 2 closes the two live producers of unreachable indexes (§4.1 for the dangling-endpoint case, §4.3
for the merge case); what remains is to make reachability *structural* rather than *currently-true*, and the
§9 alternative — giving `credentialindex` an `indexes` link to its owner, exactly as `identityindex` has —
is how that would be done. **Sequence: Inc 1 → Inc 2 → re-open C with the `indexes`-link question, behind
the first configured external key source** (the same trigger the appsession and Facet-IdP rows carry, since
`.idpBinding` cannot exist before it). If you prefer A as the end state, Inc 1 falls away and the boundTo
tombstone is removed instead; that is a one-line change and the design is written so you can say so.

---

## 8. Reconciliation with the existing mental model

**"Didn't we already fix this — twice?"** `ReconcileCredentialBinding` (`5d464007`) converged representation
3 onto 2 and took the live corpus 0 → 6 edges; `identityCredentialBindingsRead` + the `signInMethods` pane
(`ec87b8f4`) made the plane visible. Both are **bind-path** work. Neither touched the erasure path or the
provisioning path, and the pane made both gaps *observable* for the first time — which is why all three rows
were filed out of that build rather than before it.

**"Doesn't the reconcile already repair a shred?"** No — it was explicitly taught not to. §15.6 of the
client-ceremony doc records the adversarial finding that `ReconcileCredentialBinding` *would have undone an
erasure*, and the correction that an existing tombstone is a retraction nobody overturns. Inc 1 works with
that rule: it retracts more, never revives. It does, however, expire that guard's stated *reason* — the
comment at `ddls.go:1605-1612` asserts as fact that the shred tombstones only the link. The guard stays
correct (a tombstoned index now hits `not-bound`); Inc 1 owns updating the comment.

**"Does Inc 1 introduce new state?"** No. It writes no key that does not already exist, adds no bucket, no
event class, no lens, no op. It scrubs two things and emits an event three other ops already emit.

**"Is four representations itself the bug?"** Worth naming: the honest long-term simplification is fewer. 2
exists only for a uniqueness guard the graph could answer with a bounded inbound `kv.Links` on `boundTo`, and
4 exists only because the Gateway needs an O(1) point lookup outside Core KV. Collapsing 2 into 3 would
delete this whole class of drift, and it is explicitly **not** this design: it rewrites the
`credential-already-bound` guard on the hot claim path, invalidates `ReconcileCredentialBinding`'s authority
model, and migrates every index vertex — for a defect three increments close. A note, not a row.

**"Does this contradict P1/P2/P5?"** No. Inc 1 mutates only through an operation (P2) and reads only through
declared/class-(e) posture; the operational `credential-bindings` bucket stays outside Core KV and is
retracted through its own event fold, never written directly (P1). Inc 3b's read path is a lens projection
(P5), and the reason it is package work rather than a platform gap is checked, not assumed: the projection is
expressible by the shipped engine — one aspect, one row per anchor, no fan-out — unlike the `credentials[]`
array inside the same lens's ciphertext, which is not.

---

## 9. Risks + alternatives considered

| Alternative | Why not — and where it is *not* dismissed |
|---|---|
| **Give `credentialindex` an `indexes` link to its owner**, so `collect_owned_indexes` reaches it with no derivation | This is the one alternative the review strengthened rather than weakened. It makes the shred's reach **mechanism**-derived instead of derived-from-`boundTo`, which is exactly the property §7.4 names as C's precondition. Its cost is a new link per credential written by four ops, a backfill, and a class-preserving change to the shred's `identityindex` tombstone loop. Not in Inc 1 — the derivation reaches the same set today and Inc 1 is the security fix that should not wait — but it is the designed answer to §7.4, not a rejected option. |
| **Have the shred decrypt `U.credentialBinding.credentials[]`** to enumerate | Abandons the decrypt-free posture that lets the erasure run inside the shred's own commit with no ordering window against `Vault.ShredKey` — the property `shred_identity_key.go:60-71` is built around. |
| **Plain `tombstone` on the index** | **G2**: preserves `{actorKey, identityKey}`. One extra word buys the scrub. |
| **Skip an already-`isDeleted` index** (the draft's idempotence test) | **G19**: `UnlinkCredential` tombstones body-preserving, so this walks past the plaintext pair for the whole unbound population. The test is the empty body. |
| **Emit `identity.unbound` per live edge** (the draft's shape) | Strands bucket 4 for exactly the shapes the derivation exists to reach. Emit per scrubbed index. |
| **A single `identityKey` ownership clause** (the draft's shape) | Makes the outbound arm dead code — an outbound index's `identityKey` is by construction the *other* identity. |
| **`ClaimIdentity` creates the missing actor vertex** | §4.2 — a claim secret would suffice to mint an arbitrary identity key under a `scope=self` grant. |
| **Re-anchor `identityCredentialBindingsRead` on the owner** | **G9** — `traverseRel` reads the neighbour node either way. |
| **A self-loop `boundTo`, or a self-`credentialindex`, for a raw actor** | **G14** / **G13** — refused in code, and §4.3 is the live proof of the damage. |
| **A conditional third empty-state case in Facet** | §5.2 — the signal is destroyed in `appsession` before the browser sees it; a true-on-every-input string costs nothing. |
| **Build Inc 3b now** | §5.3 — zero rows in every environment we run, proven end to end. |
| **Split the three rows into three fires** | Each answers the same modelling question; answering it three times separately is how a plane acquires a fourth representation. |

**Risks.**

- **Inc 1 changes the observable behaviour of an irreversible op.** After it, shredding an identity signs its
  people out. That is the intent of an erasure, but it is a behaviour change on a destructive verb; it
  belongs in the `ShredIdentityKey` DDL description and in Loupe's submit confirmation.
- **The sign-out has one orphaning consequence the plane does not track.**
  `internal/gateway/natsauth/natsauth.go:268-303` templates a connection's NATS subject permissions off the
  **resolved** identity. Dropping the bucket row confines A's next connection to A's own namespace, so any
  Personal-Lens durable or edge-sync subject registered under U's namespace becomes unreachable but not
  deregistered (existing connections keep their JWT until `maxTTL`). Defensible for an erasure; it must be
  said out loud in the DDL description, and it is the second reason the edge-sync orphan-expiry row matters.
- **Inc 1's derivation is complete only while the plane is complete.** §3.4's one open row. Inc 2 closes both
  live producers; a fresh environment is closed by construction. Stated rather than assumed, because it is a
  guarantee that holds by the corpus's shape — and §7.4 turns that into the precondition for C.
- **Inc 2 is the increment most likely to eat a fire on its own** (the fixture migration, plus a Gateway
  change). The order below puts it second so Inc 1's security fix is not held behind it.
- **`events.identity.>` has exactly one consumer today, not structurally.** `internal/loom/engine.go:316-326`
  attaches a durable per pattern `completionDomain`, defaulting to `subjectType`; no shipped pattern uses
  `identity`. Inc 1's extra events would ack as no-ops if one ever did, but the claim is true-today.

---

## 10. Decomposition for the Steward

Each increment independently shippable and green.

| Inc | Scope | Gates |
|---|---|---|
| **1** | privacy-base: the one-pass live+all collector, the derived + two-clause-guarded + empty-body-tested + OCC-conditioned index scrub, `identity.unbound` per scrubbed index with explicit `targetKey`, the mutation+event cap, the DDL description (the sign-out and the natsauth consequence), version bump; the stale `ReconcileCredentialBinding` comment. Tests per §3.5. | `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`, `go run ./scripts/lint-package-standard.go`, **`make test-crypto-shred`**, `go test ./packages/privacy-base/... ./packages/identity-domain/... ./internal/gateway/...` |
| **2** | identity-domain: `derive_reads` + the endpoint guard on both branches, DDL description, version bump. identity-hygiene: the self-index fix (§4.3) + version bump. Gateway: `provisionActorIfNeeded` returns an error; the write path 503s, whoami does not. `cmd/lattice/identity provision`. The fixture migration. Tests per §4.7. | the above + **`make verify-package-identity`** (note: *not* `verify-package-identity-domain`) and **`go test ./...`** — eight packages exercise this path (§4.5) |
| **3a** | edge-manifest: the `signInMethods` `emptyCopy` string + version bump. | `make verify-package-edge-manifest`, `go test ./packages/edge-manifest/... ./cmd/facet/...` |
| **4** | *(gated on §7.4)* the C cascade + the `indexes`-link reachability change. | — |

There is **no** `make verify-package-privacy-base` target; `make test-crypto-shred` (`Makefile:1816-1824`) is
the shred's real gate.

**Build order against the other in-flight design.** Credential Inc 1 → Inc 2 first (both pure `packages/`
work the subject-anchored design never touches), then subject-anchored Inc 1 → 2 → 3, with its **Inc 4 last**
— that increment rewrites `ShredIdentityKey`'s piiKey mutation into two and re-derives the same batch-cap
arithmetic Inc 1 touches, so landing it last means that line is edited once instead of twice. Stage the §6.2
Contract #3 sharpening only after the subject-anchored §3.10 diff is committed.

Live verification for Inc 1 on the dev stack: diff-apply privacy-base, shred a throwaway identity holding a
bound credential, and confirm — the index vertex tombstoned with an **empty body**, the
`credential-bindings` bucket row gone, and the credential's next `GET /v1/actor` answering
`resolvedActorId == actorId`.

---

## 11. Open items this design deliberately leaves

- The op-tracker residual (§3.2, `createdByOp` → `mutationKeys` naming the pair, 24 h TTL). Bounded, not
  closed; it belongs with the hard-delete row, not here.
- Listing a *credential's* IdP provenance to the person who claimed with it (§5.3's limitation).
- Collapsing representation 2 into 3 (§8). A real simplification, deliberately out of scope.

---

## 12. Adversarial review — run on the frozen draft, folded

Two read-only reviewers (security lane; correctness/edge-case lane) ran independently against the frozen
draft. They **converged, from different directions, on the same three blockers** — the strongest signal the
pass produces, since neither could have anchored on the other.

**Three blockers, all in Increment 1, all the same failure class: a rule stated in one clause that needed
three.**

1. **The ownership guard disabled the arm it was meant to serve.** The draft said *"skip one whose
   `data.identityKey != identity_key`"* — but an outbound index's `identityKey` is by construction the
   *other* identity, so the outbound arm derived keys it could never act on, and §3.4's own test for it was
   unsatisfiable. Fixed as a two-clause guard (`identityKey ==` **or** `actorKey ==`), which still refuses
   the re-bound-elsewhere case the guard exists for.
2. **The idempotence test was `isDeleted`, and `UnlinkCredential` tombstones body-preserving.** So the whole
   already-unbound population kept `{actorKey, identityKey}` in plaintext through an erasure that reported
   success — the filed row's exact defect, for a different population. The draft cited **G2** to justify its
   *own* write shape and then failed to apply the same fact to the tombstones it walked past. Fixed: the test
   is the empty body.
3. **`identity.unbound` was keyed to live edges**, which strands bucket 4 for exactly the shapes the
   tombstoned-inclusive enumeration exists to reach — the "half of the plane, which is worse than neither"
   state §3.3 names as a rule and the draft then shipped. Fixed: emit per scrubbed index.

**Four majors.** The scrub was an **unconditioned blind Put over a lazily-read key**, whose loser in a race
with a concurrent `ClaimIdentity` is a *third party's* live binding — now `expectedRevision`-conditioned.
Inc 2's premise *"on the Gateway path the vertex always exists"* was **false**: `provisionActorIfNeeded` is
void and swallows four failure paths, so Inc 2 would have turned a transient into a user-visible *"invalid
claim key"* — now paid for by making the pre-flight return an error and the write path 503 (§4.4). The
fixture estimate was **half** the blast radius and named a helper that is an unexported `_test.go` function
in one of nine affected packages; four `internal/refractor` e2e files were falsified concretely (§4.5), and
the gate is now `go test ./...`. And `MergeIdentity` was found to be a **live producer** of the very
unreachable self-index the draft claimed was closed — now Inc 2, §4.3, with three symptoms it explains.

**Inc 3a was mis-owned, and its quoted string was wrong.** `emptyCopy` is an edge-manifest descriptor field,
not a Facet branch; and the conditional the draft proposed cannot be written at all, because `appsession`
destroys the raw-actor/person distinction before the browser sees it. Rewritten as a package-side copy change
that is true on every input (§5.2).

**The fork changed.** The correctness lane surfaced that the in-flight subject-anchored design §9.1 rejects a
shred cascade categorically, on completeness grounds — and blockers 1 and 4 are that argument landing on this
design's own enumeration. The recommendation moved from **C** to **B**, with C retained as the end state and
§7.4 naming the precondition that would make it ratifiable.

**Ledger corrections.** Six rows (**G3, G5, G6, G7, G12, G14**) cited a doc comment, a misquote, or a line
that had drifted; all are re-pinned to the code that does the thing, and five new rows (**G17–G21**) record
mechanisms the draft had assumed — that no DDL owns class `credentialindex` (so step 6 permits the scrub),
that `targetKey` is script-settable, that `UnlinkCredential` uses the `tombstone` verb, that `kv.Links`
charges the page *limit*, and that all three index-key derivations hash the same input.

**Claims that survived the attack, recorded so they are not re-litigated:** the whole `identity.unbound`
transport end to end (step 7 shape-only validation → `events.identity.unbound` → the materializer's
allow-list and `KVDelete`); that nothing rejects privacy-base writing a `credentialindex`-class vertex; that
`update` with `data: {}` genuinely scrubs; that `.idpBinding` survives `ShredIdentityKey(U)` under A's DEK
with a live operator read path; that no dispatcher declares `op.actor` in `reads` or `egressReads`; that
`identityCredentialBindingsRead` and `identityCredentialsRead` both project nothing for a raw actor; that
§5.3's lens spec would parse and validate; and that `.idpBinding` is unreachable in every environment we run.

The pre-build gate this design set for itself is therefore **discharged**, not deferred.

---

## 13. Build note — Inc 2 + Inc 3a (2026-08-08)

**Shipped.** Inc 3a `6c4ca0e4` · Inc 2 `b71be85d`. Gates: `go build ./...`, `make vet`,
`golangci-lint`, `STRICT=1 lint-conventions`, `lint-package-standard`, `lint-package-version`,
`gofmt`, and `go test ./... -p 4` (CI's parallelism) green.

**Deviations from the ratified body**, both recorded where they stand:

- **§4.3 — the self-index fix is a tombstone, not a moved write.** Struck and rewritten in §4.3
  with the state table that falsifies the original instruction: the index is already live pointing
  at the secondary, so omitting the write preserves the very refusal §4.7 requires the merge to
  clear.
- **§4.7's Gateway test split in two.** The design named one test ("a failed pre-flight 503s the
  write and does not 503 whoami"). Shipped as four, because the pre-flight has three distinct
  failure shapes and one of them must NOT 503: submit error, non-accepted reply, and the
  never-configured no-op an embedding relies on.

**Not deviations, but worth naming.** `testutil.SeedCredentialActor` is the shared minimal seed
§4.5 said belonged in `internal/testutil` rather than the unexported `seedDirectIdentity`; it seeds
the vertex only. The fixture blast radius landed well under §4.5's count — the real-op submitters
are a small subset of the reference count, most of which are mock pipelines or assertions on op
names.

**Falsification.** Every guard test was re-run with its guard disabled and the derive_reads test
with the derivation deleted; all failed as required.

**Live verification.** Both packages diff-applied onto the running stack (identity-domain
0.20.1→0.20.2, identity-hygiene 0.5.0→0.5.1), `bin/gateway` rebuilt and cycled,
`make verify-package-identity` (99 assertions) and `make verify-package-edge-manifest` (88) green.
The ceremony was then driven end to end through the real Processor: `lattice identity provision`
minted the credential vertex, `.state` and the consumer grant, and the subsequent `claim` committed
the `boundTo` edge and the `credentialindex` — the positive path the guard must not break.

The **refusal** path is proven in package tests against a real Processor and a real script, each
falsified by disabling its guard, rather than live: an actor with no vertex also has no capability
entry, so on a live stack it is refused at step 3 before the script runs, and manufacturing the
residue shape (capability without vertex) would mean writing broken state onto the shared stack.
The Gateway's 503 arm is likewise covered by tests, not by a live exercise.
