# Multi-credential identity linking + merge credential-awareness (design)

**Status:** ✅ **RATIFIED (Andrew, 2026-07-10) — with one scope change: `UnlinkCredential` builds now
(Fire 4), not deferred-to-demand.** The A2 mis-merge elevation is consciously accepted (levers stand:
operator-only grant, `identity.rebound` audit event, revocation as the immediate cut). Demand side:
a request is filed with the Vertical PO (verticals board, PO notes 2026-07-10) to spec the
account-surface consumer ("manage sign-in methods"). The #11 §11.4 edit is committed with this
ratification, including the `identity.unbound` explicit-delete fold Fire 4 needs. Ratification DD
corrected A4's enforcement mechanism (§3.3) and the derivation-fire staleness (§3.5/§6/§9).
Designer fire (Winston, 2026-07-10) · Lattice lane (Security & trust boundary) · Filed from the
external-actor-authN ratification Q&A ([authN design §12.2](external-actor-authn-binding-design.md)).

---

## For Andrew

**What it does (two lines).** One human, N sign-in methods: a **link-another-credential flow** (a
second IdP credential binds to an already-claimed identity via a client-minted link secret — Contract
#9's Option C pattern, reused), and **merge credential-awareness** (`MergeIdentity` repoints every
credential of the losing identity to the winner and emits an `identity.rebound` event the bindings
materializer folds — closing the "a merge strands A on the merged-loser forever" hole verified in
§12.2). Plus the **provision-time identityindex probe** (first-touch hint "an account matching your
verified email may already exist"), sequenced behind the linking flow it routes into.

**No architectural fork.** The design decisions here (two-step link secret vs. dual-token endpoint;
aspect-array vs. links for the N-credential record; event reuse) are resolved in §4/§8 with the
alternatives recorded — none rises to a Gateway/D1/Vault-class fork.

**One risk elevation to weigh consciously (adversarial finding A2, §10).** A **mis-merge** (operator
merges two *different* humans) already discloses the loser's business data to the winner (the link
rekey). With credential rebind it additionally hands the loser's **login** the winner's identity —
bidirectional damage instead of unidirectional, and the old "stranded credential" failure was
accidentally fail-safe. A *correct* merge needs the rebind (it is the whole point of Gap 2), so my
recommendation is to keep it and lean on the existing levers — operator-only grant, the
`identity.rebound` audit event, and the per-credential revocation kill-switch as the immediate
recovery tool. No `UnmergeIdentity` is designed (no demand; a mis-merge is recovered by operator
surgery + revocation). Flagged so ratification weighs the elevation, not discovers it.

**Frozen-contract change: Contract #11 §11.4, three small touches, staged UNCOMMITTED in `main`.**
(1) The raw-credential carve-out generalizes from `ClaimIdentity` alone to the credential-binding op
pair; (2) the bucket's fold source gains `identity.rebound`; (3) one bullet states the N:1 shape
(many credentials → one identity; each credential still ≤ 1 identity). **Note:** the same file
already carries the per-identity-subscribe-ACL design's uncommitted hunk (its own 📐 proposal,
2026-07-10) — that hunk adds a row to the **§11.1 consumer table**, mine touch **§11.4 body text**;
disjoint, independently ratifiable; whichever ratifies first commits its hunk only.

**One discovered adjacent gap, designed in (§3.5):** under Contract #11's `opaque` derivation a
browser client **cannot compute its own derived ActorID**, so it cannot fill `authContext.target`
for *any* self-scoped op (`ClaimIdentity` included) nor declare its `credentialindex` read key. The
small fix — an authenticated Gateway `GET /v1/actor` (whoami) returning
`{actorId, resolvedActorId, credentialIndexKey}` — is a prerequisite this design supplies and the
#11 Steward fire should be aware of (§9 sequencing note).

---

## 1. Problem & intent

The credential plane is deliberately layered (authN design §3.1): an IdP account derives a
**credential identity A**; a claim binds A to a **business identity U**; per-request resolution
(`credential-bindings` bucket) makes the person act as U. Two lifecycle gaps, both verified in code
(§2), break the "one human" story the moment a second IdP or a duplicate identity appears:

- **Gap 1 — the claim is one-shot, so a second credential has no path to U.** `ClaimIdentity`
  hard-rejects a `claimed` target and a credential already in the `credentialindex`; the claim secret
  is tombstoned on first use. A person who signed in with Google yesterday and Apple today gets a
  bare, dataless A_apple — reads as "the app lost my account."
- **Gap 2 — `MergeIdentity` is credential-blind.** It rekeys the operator-declared `lnk.*` edges and
  sets `state=merged`/`mergedInto`, but the losing identity's `credentialindex` vertex, its
  `.credentialBinding` aspect, and the materialized `credential-bindings` bucket entry are all
  untouched — **the loser's credential resolves to the merged-dead identity forever**, graph-side and
  bucket-side. On the security plane a wrong-target resolution is a correctness hole today and an
  availability hole for the human (their login is stranded).
- **Gap 3 — first touch silently mints a parallel identity.** Provisioning never consults the blind
  dedup index (`vtx.identityindex.<sha256NanoID("email:"+email)>`) even when the token carries a
  verified email that would hit it — so the Scenario-B duplicate is created silently instead of the
  person being offered the claim/link path.

Intent: close all three with the **smallest extensions of the shipped claim-flow machinery** — the
same client-minted-secret pattern, the same index vertices, the same event-fold materializer — no new
buckets, no new engines, no new key shapes.

## 2. Grounding — what exists, precisely (verified 2026-07-10)

- **The one-shot gates.** `packages/identity-domain/ddls.go`: `ClaimIdentity` rejects
  `state != "unclaimed"` (578–585) and an existing `vtx.credentialindex.<sha256NanoID(actor)>`
  (588–591, `credential-already-bound`); on success writes the `.credentialBinding` aspect
  `{actorKey, boundAt}` (620–623), the credentialindex vertex `{actorKey, identityKey, boundAt}`
  (629–633), the `holdsRole → consumer` link, tombstones `.claimKey`, and emits
  `identity.claimed {identityKey, actorKey}` (640–643). Tests prove the block both ways
  (`claim_test.go:359, 498` — `credential-already-bound` on a second claim).
- **The three representations of a binding.** (a) the U-side `.credentialBinding` aspect —
  **singular**, sensitivity-classed (Vault-encrypted), **read by nothing at runtime** (grep: no
  `cmd/`/`internal/` reader; it is the graph-side record); (b) the `vtx.credentialindex.<hash(A)>`
  vertex — **per-credential** (N-capable by construction), read only by the identity-domain scripts'
  dedup, by derived known-key; (c) the gateway-owned `credential-bindings` NATS-KV bucket — keyed by
  raw A, folded by the **single** materializer `internal/gateway/credential_bindings_materializer.go`
  from `identity.claimed` **only** (119–123; comment at 106: *"no unbind path in this refinement's
  scope"*), read by `credentialbinding.Resolver` at the Gateway + every app read boundary.
- **Merge.** `packages/identity-hygiene/ddls.go` `MergeIdentity` (292–348): edge rekey from the
  **operator-declared** `edges` payload (fed by the `duplicateCandidates` lens's
  `secondaryInbound/OutboundEdges` columns), state/mergedInto, ACR over `{name,email,phone}` only.
  The credentialindex is a *vertex*, structurally excluded from `edges` (the 6-segment `lnk.` shape
  gate, 242). Sole event: `identity.merged` — **zero consumers** (grep). No merge test touches
  credentials.
- **Provisioning.** `ProvisionConsumerIdentity` (ddls.go:484–550): idempotent, creates the bare
  identity **at the actor key** (`state=claimed`, `holdsRole → consumer`) — a Scenario-B identity's
  key *is* its credential ("self-bound by construction"); **no** credentialindex entry, **no**
  `.credentialBinding` aspect, no bucket entry (resolution miss ⇒ act as A = U, deny-safe and
  correct). Gateway dispatch declares `Reads=[consumerRoleKey]`, `OptionalReads=[actorID]`
  (`gateway.go:460–463`).
- **Resolution + carve-out.** `gateway.go:219–232` (`resolveActor`, miss/error ⇒ raw A);
  the `ClaimIdentity` raw-credential carve-out at 209/390–393. The dev-only e2e posture aside, writes
  enter via the Gateway only (#75: vertical apps transport-denied on `core-operations`).
- **The parallel in-flight design checked.** `per-identity-nats-subscribe-acl-design.md` (📐,
  same-day) consumes A→U resolution through the same `credentialbinding` seam and **explicitly
  anticipates this item**: *"a later merge/rebind mechanism slots in without touching the ACL"* — a
  live connection's grant drifts only until disconnect, same bound as revocation. No seam collision;
  this design must only keep the bucket the single resolution surface (it does).
- **Vendor grounding.** No new external-technology choice is made here (the IdP-side posture —
  broker-first, Keycloak First-Broker-Login — was vendor-corroborated in the authN design §12.2 and
  is unchanged). No new `docs/vendors.md` row needed.

## 3. The shape

### 3.1 The N-credential record — extend the aspect, not a new relation

`.credentialBinding.data` gains a `credentials` array; existing fields keep their meaning
(first-bound credential, the Contract-#9 record):

```json
{ "class": "credentialBinding",
  "data": { "actorKey": "vtx.identity.<A1>", "boundAt": "...",
            "credentials": [ {"actorKey": "vtx.identity.<A1>", "boundAt": "..."},
                             {"actorKey": "vtx.identity.<A2>", "boundAt": "..."} ] } }
```

- `ClaimIdentity` starts writing `credentials: [{actorKey, boundAt}]` alongside the existing fields.
- Every reader (the two scripts below) falls back to `[{actorKey, boundAt}]` when the array is
  absent (pre-existing claimed identities; there is no production deployment, so this fallback *is*
  the migration).
- The array's job is **U-side enumeration for the merge script** — one exact, dispatch-known declared
  read. The runtime resolution table stays the bucket; the per-credential graph truth stays the
  credentialindex vertices. (Why not `lnk.identity.<A>.credentialFor.identity.<U>` links, the
  Contract-#1-pure shape? See §8 — rejected: it adds a fourth representation of the same fact while
  the aspect must stay anyway for Contract #9, and the merge would still need the index-repoint +
  event machinery links don't provide.)

### 3.2 Gap 1 — the link flow: two ops, Contract #9's Option C reused

Both live in the existing `identity` DDL script (`packages/identity-domain`), mirroring the
claim pair. The proof of control of **both** identities is in-graph (a client-minted secret hashed
onto U), never a trust-the-payload assertion (§8 records the rejected dual-token-endpoint variant).

**`InitiateCredentialLink {linkKeyHash, linkKeyAlgo?="sha256"}`** — *"as U: arm a link secret."*
- Permission: `{Scope: self, GrantsTo: [consumer]}` — submitted through the normal resolved path
  (`env.Actor` = U, `authContext.target` = U from whoami §3.5).
- Preconditions (failures collapse to one generic code, §3.4): target exists, not tombstoned,
  `state == "claimed"` (an `unclaimed` identity uses `ClaimIdentity`; `merged` refuses).
- Writes `.linkKey` aspect `{hash, algo}` — create **or overwrite** (re-initiating rotates a lost
  secret; the exact posture of Contract #9's `.claimKey` + R4's rotate, self-service because the
  actor *is* the identity here). Sensitivity-classed like `.claimKey`. No event (nothing consumes
  it). Reply: `{primaryKey: U}`.
- Declared reads: `U`, `U.state`; optionalReads: `U.linkKey`.

**`CompleteCredentialLink {targetIdentityKey, linkKey}`** — *"as the new credential: prove the
secret, bind."*
- Permission: `{Scope: self, GrantsTo: [consumer]}` — submitted as the **raw** credential A2
  (`authContext.target` = A2): the Gateway's raw-credential carve-out extends from `{ClaimIdentity}`
  to `{ClaimIdentity, CompleteCredentialLink}` (the dedup hashes `op.actor` and must see the
  credential — the same reason, contract-stated, §5). A2 already holds `consumer` from first-touch
  provisioning.
- Preconditions: U exists, `state == "claimed"`, `.linkKey` present + constant-time hash match,
  `vtx.credentialindex.<sha256NanoID(op.actor)>` **absent** (the same one-credential-≤-one-identity
  guard as the claim — #11 §11.4's dedup invariant holds for links too). **Enforcement honesty
  (finding A4):** the `state[credIndexKey]` check is dispatcher-declaration-driven (an omitted
  optionalRead reads as "absent" — same as the claim, `ddls.go:650–653` / `claim_test.go:123–129`);
  the **load-bearing** double-bind stop is the `create` of the index vertex below, which is
  CreateOnly at commit (`step8_commit.go:181–182`) and conflicts if the credential is already bound
  — fail-closed independent of any declaration. The declared guard exists for the friendly generic
  error; the dispatcher should declare it, but security never rests on that.
- Mutations: create `vtx.credentialindex.<hash(A2)>` `{actorKey: A2, identityKey: U, boundAt}`
  (CreateOnly — the structural dedup backstop); append `{actorKey: A2, boundAt}` to
  `U.credentialBinding.credentials` (creating the aspect if U never had one — the Scenario-B case,
  where U's implicit self-credential stays implicit); **tombstone `.linkKey`** (single-use).
- Event: **`identity.claimed {identityKey: U, actorKey: A2}`** — deliberately the existing class:
  the semantic ("this credential is now bound to this identity") and the payload are identical, so
  the shipped materializer folds it with **zero changes** and Contract #11 §11.4's fold description
  stays true. (§8 records the rejected `identity.linked` variant.)
- Declared reads: `U`, `U.state`; **optionalReads**: `U.linkKey`, `U.credentialBinding`, the
  caller's own `credentialindex` key (from whoami §3.5) — all three are legitimately absent in
  reachable states (never-armed, Scenario-B, unbound), and absence must produce the generic failure
  or the create path, never a hydration fault. Declaring sensitive aspects has direct precedent —
  every `ClaimIdentity` dispatcher declares `.claimKey`.

**The FE flow** (one page session): signed in as U → `InitiateCredentialLink` (FE mints `s`
client-side, submits the hash, keeps `s` in memory — never transits Lattice, Contract #9's
invariant) → "sign in with your other provider" re-auth → now bearing A2's token, submit
`CompleteCredentialLink{U, s}` → whoami confirms `resolvedActorId == U`. The secret lives minutes in
one device's memory; the cross-device variant (initiate on laptop, complete on phone) works by the
same out-of-band handoff discipline as the claim link (§11.1a of the claim-flow design).

### 3.3 Gap 2 — merge credential-awareness

`MergeIdentity` (identity-hygiene) gains, atomically with the existing merge commit:

- **New reads** (both exact, dispatch-known — declared as **optionalReads**, finding A6: a
  never-claimed or Scenario-B identity has no `credentialBinding` aspect, and a required read's
  absence is a hydration fault that would block exactly the staff-duplicate merges Gap 2 targets):
  `secondary.credentialBinding`, `primary.credentialBinding`.
- **Credential set** = secondary's `credentials` array (fallback `[{actorKey, boundAt}]`; empty if
  the aspect is absent — a never-claimed staff-created secondary folds nothing). **Trust basis for
  array-driven repoint (finding A4 — mechanism corrected at ratification DD):** aspect-class write
  gating is **not** the protection: an empty `permittedCommands` is *unrestricted*, not locked
  (`step6_validate.go:129` skips the check for an empty list; the DDL text itself says "intentionally
  empty so any identity-anchored writer is allowed"). The array's real trust basis is the
  **package-install boundary**: only the claim/link/merge scripts emit mutations for this aspect,
  the same grade as the index vertices and the `identity.claimed` event feed itself — all equally
  writable by a malicious installed package (the platform's stated DDL-trust boundary, the same
  posture the sensitive-param-egress ref-trust decision rests on). Reading the array adds no new
  writer surface.
- **Plus the implicit self-credential:** the secondary **key itself** joins the set (with
  `boundAt = mergedAt`). A Scenario-B secondary *is* its own credential — without this, its future
  logins resolve-miss to the merged-dead vertex, the exact hole being closed; and recording it in
  primary's array keeps a **later chain-merge** of primary carrying it along (the array is the full
  "resolves to me" set, so no entry is ever orphaned by a second-generation merge). For a
  staff-created secondary the entry is inert-but-correct (only a dev `nanoid`-mode token could ever
  present that key — and post-merge it *should* act as primary).
- **Mutations, per credential A in the set:** **unconditioned `update`** of
  `vtx.credentialindex.<sha256NanoID(A)>` → `{actorKey: A, identityKey: primary, boundAt}`. Named
  precisely (finding A3 — "upsert" is not a mutation verb; the vocabulary is
  `create`/`update`/`tombstone`, `starlark_runner.go:336` / `step6_validate.go:90`): `create` is
  CreateOnly and would `RevisionConflict` on every already-bound credential's existing vertex, so
  the verb is `update`, which for an undeclared-read key commits as a **blind Put** (nil
  `ExpectedRevision`, `commit_path.go:553–576`) — overwriting the existing vertices and creating
  the absent self-credential one. That unconditioned write is structurally race-free here:
  same-secondary merges serialize per-subject, and a credential can appear in at most one
  identity's array (the dedup invariant), so no concurrent writer targets the same index key.
  Then: union the set into `primary.credentialBinding.credentials` (creating the aspect if primary
  never had one); **tombstone `secondary.credentialBinding`**. Computed-key *writes* are the claim
  script's own idiom (the index vertex has always been written at a derived key); no unannotated
  `kv.Read` is added — the script never reads the index vertices, it overwrites them from the
  declared-read array (read-posture clean).
- **Events, per credential A in the set:**
  `identity.rebound {actorKey: A, identityKey: primary, previousIdentityKey: secondary}`.
- **Materializer:** `credentialBindingsHandler` gains one case — `identity.rebound` folds exactly
  like `identity.claimed` (`KVPut(bucket, actorKey, {identityKey})`). Single writer, durable
  consumer, stream-ordered — a rebound after a claim folds last and wins; a from-scratch bucket
  rebuild replays in stream order and converges. (Write guard, named precisely: the bucket is
  gateway-owned NATS-KV written by **one** materializer via plain `KVPut` — last-writer-wins is safe
  under the single ordered writer; there is no cross-instance race to guard.)
- **Retraction check (run, not assumed):** merge is a **single-key overwrite** per credential
  (bucket entry and index vertex repoint in place — auto-retracting); **no row-set shrink exists in
  this design's build scope**, so no missing-Delete over-grant window. The one genuine
  key-disappears case — removing a credential — is `UnlinkCredential` (**Fire 4**, §8), whose fold is
  an **explicit bucket `KVDelete`** — the one genuine row-set shrink in this plane, never covered by
  overwrite-by-reprojection.
- **Staleness window:** until the materializer folds the rebound, A still resolves to the merged
  secondary — whose links were rekeyed away and whose state ops fail closed (`FR4` post-merge
  redirect test), so the window degrades to less-reach, never wrong-reach; self-heals on fold. Same
  M3 CDC-lag class the claim already accepted. Live NATS connections under the subscribe-ACL design
  drift the same way until disconnect (its §"resolution drift" — already bounded there). The §12.1
  future resolver TTL-cache must invalidate on `identity.rebound` — recorded here as a constraint on
  that cache, not built now.

### 3.4 Gap 3 — the provision-time identityindex probe

**Build-time correction (Fire 3, 2026-07-11):** the mechanism below as originally drafted routed the
hint through `ProvisionConsumerIdentity`'s op reply, on the premise that "the response schema (closed,
per-op) gains the optional field" was package work, not a contract change. That premise is **false**:
Contract #2 §2.7 closes the script-return `response` schema to exactly `primaryKey` **at the Processor
level**, platform-wide, not per-op — `internal/processor/starlark_runner.go`'s `parseResponse` fails
closed (`ScriptFailed`/`InvalidReturnShape`) on any other key, and `primaryKey` itself must be a
committed mutation key (§2.7's own text: "This makes the synchronous reply incapable of carrying
arbitrary or sensitive data"). The already-provisioned no-op branch also commits nothing, so it
structurally cannot even smuggle the hint via `primaryKey`. Widening §2.7 to carry a read-derived
boolean would reopen exactly the class of leak Story 1.5.7 froze it to prevent — not a narrow,
uncontroversial edit. Built instead, with **no contract change**: a small P5-clean lens
(`identityIndexHint`, `packages/identity-domain/lenses.go`) projects live `identityindex` vertices
into their own NATS-KV bucket (`identity-index-hint`); `GET /v1/actor?probe=1` reads it **directly**
(`internal/gateway/identityindexhint`, mirroring `credentialbinding`'s resolver pattern) — the same P5
read seam every lens consumer already uses, never through an operation reply. `ProvisionConsumerIdentity`
is untouched by this fire (no `contactIndexKeys` payload field, no response-schema change); the probe is
fully decoupled from provisioning. The paragraph below is superseded where it describes routing through
the op; retained for the intent/outcome, which is unchanged.

~~`ProvisionConsumerIdentity` gains an optional payload field `contactIndexKeys:
["vtx.identityindex.<hash>", …]`~~, which the **Gateway computes exclusively from *verified* token
claims** (`email` with `email_verified == true`; phone TBD, not built this fire) via
`substrate.SHA256NanoID("email:" + normalizedEmail)` — never from client-supplied input. The lens read
answers with one boolean: `existingIdentityHint = true` iff any probed index vertex exists and its
`identityKey != actor's own ActorID` (§4's original "target_actor_key" — the raw, pre-resolution actor).
The hint is **on-demand, not standing**: `GET /v1/actor?probe=1` (§3.5) additionally performs the lens
read (no op re-submission, so the `provisioned` fast-path set is irrelevant to it) — the FE calls it
once when rendering its account/linking screen. The FE routes a hit: *"a record matching your verified
email may exist — have a claim code? or sign in with your original provider and link this one,"* and
suppresses it once `resolvedActorId != actorId` (already linked/claimed) — an FE-side concern, not yet
built (no FE consumer this fire; see §9 Fire 3 scope).

- **P2/P5 clean:** the Gateway never reads Core KV — the identityindex existence check is a Refractor
  lens projection (`identityIndexHint`), and the Gateway reads that lens's NATS-KV read-model bucket
  directly, the same P5 seam every lens consumer uses; it computes only the lookup hash. **No PII
  persists or transits:** the lookup key is the same one-way hash already used as the identityindex
  vertex key; nothing new enters `core-operations` (the probe submits no operation at all).
- **Enumeration posture:** the boolean is an existence oracle **scoped to emails the caller provably
  controls** (verified claim of their own token) — the same answer front-desk staff would give them;
  arbitrary-email probing is structurally impossible since the Gateway never hashes client input.
  The hint returns no identity key, no name, no state.
- **Honest limits:** the probe only hits identities that have identityindex entries (staff-created
  via `CreateUnclaimedIdentity`; Scenario-B bare identities have none), and email matching is a
  heuristic (Apple Hide-My-Email defeats it; different emails per provider defeat it) — the complete
  mechanism remains explicit linking + after-the-fact merge, which is exactly what Gaps 1–2 provide.
  Sequenced **behind** the link flow (a probe whose hit has no flow to act on is dead scaffolding).

### 3.5 The whoami surface (the adjacent gap, closed here)

Authenticated `GET /v1/actor` on the Gateway:

```json
{ "actorId": "vtx.identity.<A>", "resolvedActorId": "vtx.identity.<U-or-A>",
  "credentialIndexKey": "vtx.credentialindex.<sha256NanoID(A)>", "existingIdentityHint": false }
```

- Runs the existing authenticate → provision-if-needed → resolve pipeline synchronously (the
  provisioning is idempotent; this is the natural "first authenticated call" for a fresh FE session)
  and computes `credentialIndexKey` server-side. `existingIdentityHint` is populated only on
  `?probe=1` (which forces the op submit past the in-memory provisioned set, §3.4); the plain call
  omits it.
- **Why it's load-bearing beyond convenience:** under #11 `opaque` binding the ActorID is
  `SHA256NanoID("idpsub:…")` — a browser cannot compute it from its own token, so without whoami no
  FE can fill `authContext.target` for **any** self-scoped op (`ClaimIdentity`,
  `InitiateCredentialLink`, `CompleteCredentialLink`) or declare the `credentialindex` dedup read.
  The dev `nanoid` mode masks this (sub *is* the id) — but the #11 derivation fire **shipped without
  whoami** (`9812231`, 2026-07-10): opaque-mode derivation is live for any configured external IdP
  source, so the gap is real now. whoami builds in Fire 2 (§9).
- Read-only at the platform level (the one write it can trigger is the shipped idempotent
  provisioning op, P2-clean); no Core-KV read (resolution is the gateway-owned bucket; the hint is
  relayed from the op reply).

### 3.6 What does NOT change (verified)

- **`ClaimIdentity` mechanics** (Contract #9): byte-identical flow; only the additive `credentials`
  array in the aspect it writes (whose schema #9 never specified — verified; only `.claimKey`'s is
  spelled).
- **Resolution, revocation, RLS:** the resolver still does one bucket `Get` keyed by raw A;
  revocation still keys pre-resolution A per credential (revoking the lost phone's IdP credential
  cuts that credential only — with N credentials that per-credential granularity is finally
  meaningful); the RLS var still carries the resolved actor. No surface learns anything new.
- **`duplicateCandidates` / dedup-over-ciphertext:** untouched — that lens's Vault-inertness is its
  own board row (Privacy/Vault). This design's §3.4 probe is the *blind-index equality* half working
  as designed (deterministic hash vertices, unaffected by Vault); the Levenshtein half stays with
  that row.
- **Subscribe-ACL / Edge sync plane:** consumes resolution through the same seam; no ACL change
  (its design already states rebind slots in transparently).

## 4. Decisions resolved (would-be open questions)

1. **Two-step link secret over a dual-token Gateway endpoint** — the proof of dual control stays
   in-graph (hash on U, verified constant-time in the script), enforceable by the Processor
   regardless of which door submitted the op; a dual-token endpoint would rest the entire
   account-takeover boundary on one Gateway handler constructing a payload honestly. §8 has the full
   comparison.
2. **Aspect-array over binding links** — smallest extension; the aspect must exist anyway; links
   would be a fourth representation and would tempt the merge's edge-rekey to "handle" bindings
   without the index/bucket halves, leaving the same stranding bug wearing a new shape.
3. **Reuse `identity.claimed` for link-completion; new `identity.rebound` for merge** — a
   link-completion *is* the same fact the materializer folds (credential → identity, new binding); a
   merge-rebind is a different fact (existing binding repointed, no claim occurred) that audit
   consumers must be able to distinguish — and it needs `previousIdentityKey`. One new event class,
   one new fold case, no phantom claims in the audit stream.
4. **The secondary key itself rebinds on merge** — closes the Scenario-B stranding and the
   chain-merge orphan (§3.3); inert for staff-created secondaries.
5. **`.linkKey` has no expiry** — mirrors `.claimKey` exactly (no TTL primitive on aspects; single
   use; rotate by re-initiating; holder is the same human's own device). An armed-but-unused linkKey
   is the same risk grade as an unclaimed identity's claimKey — accepted there, accepted here.
6. **Generic failure code** — all `CompleteCredentialLink` failures collapse to one code
   (`LinkKeyInvalid`), NFR-S6 anti-enumeration, exactly like `ClaimKeyInvalid`; specifics via
   Health KV.

## 5. Contract surface

**Contract #11 §11.4 — the actual edit, staged UNCOMMITTED in `main`** (three touches):

1. Fold source: *"materialized into the `credential-bindings` bucket from the `identity.claimed`
   event"* → *"…from the `identity.claimed` and `identity.rebound` events, and an `identity.unbound`
   event (credential unlink) folds as an explicit bucket-key delete"* (the delete clause added at
   ratification when Fire 4 moved into scope).
2. Carve-out: *"`ClaimIdentity` operations are always submitted with the raw credential actor"* →
   the credential-binding op pair (`ClaimIdentity`, `CompleteCredentialLink`), same rationale
   (the dedup hashes `op.actor`; a resolved actor would let a bound person chain-claim/chain-link).
3. One added bullet: an identity may be bound by **multiple** credentials (each credential still
   resolves to at most one identity — the dedup guard is per-credential and unchanged); a merge
   repoints the loser's credentials via `identity.rebound`.

*(Coexistence note: the file already carries the subscribe-ACL design's uncommitted §11.1 hunk —
disjoint, independently ratifiable.)*

**Build-to (no change), verified:** **#9** — claim mechanics untouched; the `.credentialBinding`
schema was never contract-specified; the link secret honors every #9 invariant (client-minted,
hash-only storage, never persisted or replied, single-use, generic failures). **#1** — no new key
shapes (existing `identityindex`/`credentialindex` classes; `.linkKey` is an ordinary 4-segment
aspect; no new links). **#2** — new ops are ordinary package operationTypes with closed response
schemas; the provisioning response's optional `existingIdentityHint` is per-op schema, package-level.
**#6** — two new package permissions (`scope: self, GrantsTo: [consumer]`), the ordinary idiom.
**#75/natsperm** — no new subjects, buckets, or publish grants (whoami is an HTTP surface on the
existing Gateway).

## 6. Reconciliation with the existing mental model

- **"Didn't we already handle this?"** The A→U resolution plane (R1) and first-touch provisioning
  shipped — for **one** credential, bound **once**, never re-pointed. §12.2 of the authN design
  identified both gaps during ratification Q&A and filed this row; this design is that item, not a
  redo of R1.
- **"Doesn't the claim flow already bind credentials?"** Yes — exactly once, by design (the one-shot
  is the anti-takeover property, kept intact: `CompleteCredentialLink` binds only with a fresh
  in-graph secret armed by U itself; nothing weakens `ClaimIdentity`'s gates).
- **"Doesn't revocation cover the lost-credential case?"** For **cutting** a credential, yes
  (per-credential kill-switch, per-request) — revocation stays the security cut; `UnlinkCredential`
  (Fire 4) is the account-hygiene remove. What revocation cannot do is *add* a credential (Gap 1)
  or *re-target* one (Gap 2).
- **Does this introduce new state?** One aspect field (`credentials` array), one aspect class
  (`.linkKey`, the `.claimKey` twin), one event class (`identity.rebound`), one materializer fold
  case, one Gateway HTTP endpoint. No new bucket, lens, vertex type, link type, or engine surface.
- **Does anything duplicate an in-flight design?** Checked (§2): the subscribe-ACL design shares the
  resolution seam read-side only and pre-declared compatibility; the #11 derivation fire (Steward,
  ratified) changes how A is *computed* upstream of everything here — both compose, neither
  collides. The derivation fire shipped without whoami (`9812231`) — it now builds here, Fire 2 (§9).

## 7. Migration & test strategy

**Migration:** additive everywhere. Pre-existing claimed identities lack the `credentials` array —
every reader falls back to the singular fields; no backfill (no production deployment; dev stacks
re-bootstrap). The materializer change is fold-forward (old events replay identically).

**Package tests** (`packages/identity-domain`): initiate-on-unclaimed / merged / tombstoned →
generic fail; initiate re-arm overwrites; complete happy path (index vertex + array append +
linkKey tombstoned + `identity.claimed` emitted); complete with wrong secret / spent secret /
already-bound credential (`credential-already-bound` parity) / unclaimed target → generic fail;
claim writes the array; complete-on-Scenario-B identity creates the aspect.
(`packages/identity-hygiene`): merge with 1 and N credentials — index vertices repointed, primary
array unioned (incl. the secondary self-credential entry), secondary aspect tombstoned, one
`identity.rebound` per credential; merge of a never-claimed secondary — no credential ops; chain
merge (U3→U2→U1) — U3's credential ends at U1.

**Materializer unit** (`internal/gateway`): `identity.rebound` folds; claimed-then-rebound converges
to the rebound target; unknown events still ignored.

**E2E (ephemeral stack, rides the capability lane):** sign in A1 → claim U → whoami shows
`resolvedActorId = U` → initiate → re-auth as A2 → complete → whoami as A2 shows U → merge U2→U1 →
A_2's next request resolves U1. Probe arc: staff-create with email → fresh token with same verified
email → whoami `existingIdentityHint = true`.

**Gates:** standard set + `make verify-package-identity-domain` + the identity-hygiene package
verify (both packages' DDL/permissions change).

## 8. Risks & alternatives considered

- **Rejected — dual-token single-request linking** (Authorization: A2's token + an `X-Link-Token`
  for A1; Gateway verifies both, submits the bind). Fewer steps, but the *entire* dual-control proof
  becomes a payload field one Gateway handler is trusted to have constructed — un-verifiable
  in-graph, and a second submitting door (or a Gateway bug) turns it into an account-takeover
  primitive. The two-step keeps the proof where every other claim-plane proof lives: hashed state on
  U, verified in the script. (It also needs no new endpoint at all.)
- **Rejected — binding links (`lnk.identity.<A>.credentialFor.identity.<U>`)** — see §4.2.
- **Rejected — reusing `identity.claimed` for merge-rebinds** (zero materializer delta): phantom
  claims in the audit stream and no `previousIdentityKey`; a 3-line fold case is the honest price.
- **`UnlinkCredential` — in scope (Fire 4; ratification moved it from deferred-to-demand to build,
  Andrew 2026-07-10):** `{Scope: self}` as U, payload names the credential to remove; tombstones its
  index vertex, removes it from the array, emits `identity.unbound {actorKey, identityKey}`; the
  materializer gains the **explicit bucket `KVDelete`** fold. **Self-lockout guard:** removing the
  **last** entry in the credential set fails with the generic code — an identity must keep ≥ 1
  sign-in path (the emergency cut for a compromised sole credential stays operator revocation, which
  needs no self-session). A Scenario-B implicit self-credential is not an array entry and is not
  unlinkable — it *is* the identity. The FE consumer is the Vertical PO's filed request
  ("manage sign-in methods", verticals board).
- **Rejected — probing from the Gateway directly** (a Core-KV `Get` on the index vertex): the
  Gateway is not in P5's inspector-exception set and must not become a Core-KV reader; declared
  optionalReads put the read where every write-path read belongs (Processor-side). Costs one
  idempotent op round-trip on a path that already runs it.
- **Account-takeover surface (the design's own red-team, §10):** binding requires an armed,
  unexpired-in-practice, single-use secret minted by U's own session — an attacker needs the
  plaintext from the victim's device *and* must win the race to spend it; failures are generic;
  rotation is self-service. Social-engineering ("read me your link code") remains the irreducible
  residual of *any* linking flow — same grade as the claim brochure, mitigated by the same
  out-of-band discipline.
- **Merge as a grant-transfer — the honest blast radius (finding A2).** For a *correct* merge the
  rebind is the point (operator-adjudicated "same human" keeps their login). For a **mis-merge** the
  damage becomes bidirectional: pre-design, a wrong merge disclosed the loser's business data to the
  winner (link rekey) while the loser's login merely stranded (accidentally fail-safe); post-design
  the loser's login also **acts as the winner**. Not the same grade as the link rekey — an
  authn-identity handover. Accepted with eyes open because the levers exist: operator-only grant,
  `identity.rebound` in the audit stream, revocation as the immediate cut, operator surgery as the
  recovery (no `UnmergeIdentity` — dead scaffolding until real demand). Elevated to the For-Andrew
  block for ratification.
- **Crypto-shred interplay:** shredding U makes `.credentialBinding` unreadable but bucket entries
  (A→U) survive — resolution then lands on a shredded/tombstoned identity whose reads deny. Correct
  direction (deny), but the forget-flow's "walk the discoverable identity-set" (claim-flow §11.2)
  should also revoke the bound credentials — one line added to that walk when `UnlinkCredential`
  lands (Fire 4); until then revocation-by-operator covers it.

## 9. Decomposition for the Steward (four fires)

1. **Fire 1 (S–M) — merge credential-awareness.** `packages/identity-hygiene` (reads, credential-set
   fold incl. self-credential, index upserts, array union, aspect tombstone, `identity.rebound`) +
   the materializer fold case (`internal/gateway`) + §7 package/materializer tests. Ships alone —
   fixes today's single-credential stranding with no dependency on Fire 2 (the array fallback covers
   the current world).
2. **Fire 2 (M) — the link flow.** `packages/identity-domain` (`InitiateCredentialLink`,
   `CompleteCredentialLink`, `.linkKey` sensitive DDL, `credentials` array in the claim script) +
   `internal/gateway` (carve-out set extension, `GET /v1/actor` whoami) + §7 tests + the link e2e.
   **Sequencing note:** the #11 derivation fire shipped first (`9812231`, 2026-07-10) without
   whoami — Fire 2 builds it; it is the missing client-side enabler for every self-scoped op under
   the now-live opaque derivation (§3.5).
3. **Fire 3 (S) — SHIPPED (2026-07-11).** The provision-time probe, built as a P5-clean lens read
   instead of an op-reply field (§3.4 build-note — Contract #2 §2.7's closed response schema forbids
   the originally-drafted shape; no contract change was needed for the corrected shape). Backend only:
   `packages/identity-domain` (`identityIndexHint` lens), `internal/gateway/identityindexhint`
   (resolver) + `internal/gateway/auth` (verified email/email_verified claim capture) +
   `internal/gateway` whoami `?probe=1` + tests. `ProvisionConsumerIdentity` untouched. No FE consumer
   yet (the link flow's account/linking screen, §3.4 — Vertical PO's filed request, verticals board).
4. **Fire 4 (S) — `UnlinkCredential` + the bucket-delete fold** (§8: last-credential lockout guard,
   index-vertex tombstone, array removal, `identity.unbound` → materializer `KVDelete`) + tests
   (happy path incl. the bucket delete; last-credential refusal; unlink-then-relink round-trip;
   never-linked credential → generic fail). Sequenced after Fire 2 (it edits the array the link flow
   maintains and shares whoami). The FE consumer is the Vertical PO's filed request.

## 10. Adversarial pass (security plane — run twice)

The first pass produced T1–T8 (below). A dedicated read-only adversarial sub-agent then reviewed
the drafted doc against the code and surfaced **A1–A6**, all folded in before filing: **A1** — the
§5 contract edit was described as staged before it existed (now actually staged; the coexistence
note corrected to §11.1-table vs §11.4-body); **A2** (MAJOR) — the mis-merge blast-radius elevation
(→ For-Andrew block + §8); **A3** — "upsert" is not a mutation verb; the repoint is an
**unconditioned `update`/blind Put**, and `create` would RevisionConflict on every bound credential
(→ §3.3, named semantics); **A4** — the `state[]` dedup guard is declaration-driven and fail-open
as a *guard*; the load-bearing stop is the CreateOnly `create`, and the merge's array-driven
repoint rests on the aspect class admitting no generic writes (→ §3.2/§3.3, enforcement honesty);
**A5** — the hint was one-shot-at-first-touch and unreachable for returning sessions (→ §3.4/§3.5,
on-demand `?probe=1` + no-op-branch response); **A6** — the merge's `credentialBinding` reads must
be optionalReads or never-claimed merges hydration-fault (→ §3.3). The sub-agent's failed attacks
(Vault-classed aspects DO decrypt at script hydrate like `.claimKey`; scope-self cannot arm a
victim's linkKey; `resolveActor` cannot chain; the rebound fold converges) are the design's
survived claims.

- **T1 — bind my credential to a victim's U?** Needs the linkKey plaintext; only U's own
  authenticated session can arm one; storage is hash-only; single-use; generic failures defeat
  probing. Residual: plaintext theft from the victim's device/session — the claim flow's accepted
  grade.
- **T2 — victim armed a link, attacker completes first?** The plaintext never transits Lattice; the
  attacker must obtain it out-of-band (T1 residual). Rotation (re-initiate) invalidates a suspected
  leak; tombstone-on-use closes the race after any completion.
- **T3 — chain-claim via resolution?** The carve-out keeps `op.actor` = raw credential for both
  binding ops; the per-credential dedup guard (`credentialindex` existence) holds for links exactly
  as for claims — one credential, at most one identity, contract-stated (#11 §11.4).
- **T4 — merge as privilege escalation?** Operator-gated, same trust grade as the merge's existing
  link-rekey power (§8); no self-service path re-targets a binding.
- **T5 — defaults fail closed?** No `.linkKey` ⇒ complete fails; `state != claimed` ⇒ both ops fail;
  absent array ⇒ singular fallback, absent aspect ⇒ merge folds nothing; materializer ignores
  unknown events; resolution miss ⇒ act as A. No omission grants anything.
- **T6 — the probe as an oracle?** Scoped to the caller's own verified email (Gateway hashes
  verified claims only); boolean-only; no arbitrary-input path exists (§3.4).
- **T7 — event replay / fold ordering?** Single durable ordered consumer; upserts converge in
  stream order; full-replay rebuild converges (§3.3). The rebound fold is idempotent.
- **T8 — retraction audit?** Every repoint in build scope is single-key overwrite (auto-retracting);
  the only row-set shrink (unlink, Fire 4) carries its explicit `KVDelete` fold — no silent
  "reprojection retracts it" claim anywhere in this design.

## 11. Companion doc/board updates made in this fire

- `docs/contracts/11-external-actor-authn.md` — the §5 edit, staged **UNCOMMITTED** (alongside the
  subscribe-ACL design's disjoint §11.1 hunk).
- `_bmad-output/planning-artifacts/backlog/lattice.md` — the row → 📐 awaiting-Andrew, linking here.
- `external-actor-authn-binding-design.md` — untouched (its §12.2 already points at this row; this
  doc is the design of record for the item).
