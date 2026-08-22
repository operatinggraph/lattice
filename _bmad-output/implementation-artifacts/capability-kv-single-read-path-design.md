# Capability KV — one read path for the authorization surface

**Status: ✅ Winston-ratified — build-ready (in-fire, 2026-08-22).** Implementation-level decision only: no
frozen-contract change (Contract #6's key routing is restated nowhere new — it is *removed* from three
restatements and left in the one package that owns it), no architectural fork. Author: Winston (Lattice
Steward fire, 2026-08-22). Board row: `backlog/lattice.md` → "[Core] `capability-kv` has ≥8 readers, two
re-implementing the read inline".

## 1. Problem

`internal/capabilitykv` exists to be the single place that knows Contract #6 §6.1's Capability-KV key
routing and doc merge — "so a second reader can read the same projection through the same key-routing
without duplicating it" (`internal/capabilitykv/doc.go:1-7`). Three production call sites duplicate it
anyway, each with its own hardcoded key strings, its own `KVGet` loop, and (in one case) its own merge.

The routing they hardcode is not the routing the Processor applies. `capabilitykv.ClassAwarePlatformKey`
(`keys.go:58-82`) routes an actor in the **live** system-actor set to a UNION read of
`cap.<rest>` + `cap.roles.<rest>`, and **every other actor to `cap.roles.<rest>` alone**. The three inline
readers union both keys for **every** actor, unconditionally.

**The divergence is latent today, not a live over-grant — and that is the reason to close it, not a reason
to leave it.** Both rules select the same keys while the operator-role population sits exactly at its
bootstrap seed: the anchor lens projects `cap.<actorSuffix>` only for identities holding the primordial
`operator` role (`internal/bootstrap/lenses.go:100-149`), and `bootstrap.SystemActorKeys`
(`system_actors.go:35-60`) discovers membership through that same live `holdsRole → operator` predicate, so
an ordinary actor has no anchor doc to over-read. The two rules coincide by arithmetic, not by construction.

They stop coinciding the moment that population moves off its seed — a revoked operator whose anchor doc
outlives the revocation (the `emptyBehavior: delete` tombstone is decline-able: `zeroRowDeleteKey`,
`internal/refractor/pipeline/evaluate.go:1339-1350`, fails safe by *not* deleting when the adapter cannot
read its own rows back), or any delegation of operator-equivalent authority below root, which the board
already anticipates as the revive trigger on the shelved `[bootstrap] A package-plane actor can forge a
package-origin permission` row. Correctness that holds by coincidence over a population that is designed to
change is the defect; the fix is that the three sites stop restating the rule and ask the package that owns
it.

Two of the three make it load-bearing. `cmd/lattice/capability`'s `heldPermissionsForActor` and
`cmd/loupe/review.go`'s `heldPermissionsForCapabilityActor` are byte-identical copies that build
`[]pkgmgr.HeldPermission` — the bound on what a capability **grant proposal may confer**
(`internal/pkgmgr/capabilitymaterializer.go:208-236`, `requesterHolds`). Anything they over-report is
authority a proposal may hand out, and what the anchor doc carries is the root set: `CreateMetaVertex` /
`UpdateMetaVertex` / `TombstoneMetaVertex` / `Install` / `Uninstall` / `UpgradePackage`, every one at
`scope: "any"`. `HeldPermission`'s own doc comment names this hazard in terms: "whoever builds the real
caller … MUST read this projection for the actual requesting/approving actor (op.actor), fresh, every time —
a stale or wrong-actor slice defeats the entire scope check." Both callers were then built by hand, each
re-deriving the key set.

The third, `internal/aiagent/traversal.go:169-260`, is the FR19 cold-start discovery traverser — not an
authorization path — but it carries a *second* merge implementation for the same doc shape
(`mergeCapabilityDocs`), which dedups platform permissions on `{operationType, scope}` and therefore drops
`origin`, the Contract #6 §6.1 provenance field. Its own comment concedes this and warns the next caller.
It also hardcodes the `identity` actor type into both keys, so any non-`identity` actor is silently
mis-keyed.

## 2. Grounding ledger (verified live, 2026-08-22, at `ac9e4fa`)

| # | Claim | Verified at | Note |
|---|---|---|---|
| G1 | `ReadAndMerge` is the canonical read; applies **no** gates — pure read + merge | `internal/capabilitykv/read.go:33-59` | every gate is caller-side by design |
| G2 | Class-aware routing: system → `{cap.<rest>, cap.roles.<rest>}`, ordinary → `{cap.roles.<rest>}` | `internal/capabilitykv/keys.go:58-82` | |
| G3 | System-actor set is graph-derived from a live `holdsRole → operator` predicate | `internal/bootstrap/system_actors.go:35-60` | |
| G3a | The anchor lens projects `cap.<actorSuffix>` for exactly that predicate's holders, granting the fixed root set at `scope:"any"` | `internal/bootstrap/lenses.go:100-149` | so at the bootstrap seed an ordinary actor has NO anchor doc — the divergence is latent, not live |
| G3b | The Processor resolves `SystemActorKeys` ONCE at boot and holds it for the process lifetime | `cmd/processor/main.go:128-145` | **designed-around, not a defect** — `packages/demo-operator/package.go:29-39` names the boot-snapshot dependency and routes around it with a distinct package role. Not filed. |
| G4 | Processor reads through the helper, then applies reserved-op + lane + scope gates | `internal/processor/step3_auth_capability.go:322-324`, `:630-646`, `:553-574` | |
| G5 | `gateway/rolesanchors` **already** routes through `capabilitykv.RolesKeyFromActor` + `ReadAndMerge` | `internal/gateway/rolesanchors/rolesanchors.go:103-118` | **the board row's citation has ROTTED — it is not an inline reader** |
| G6 | `internal/controlauth` reads through the helper | `internal/controlauth/checker.go:148` | |
| G7 | `aiagent` reads inline at two hardcoded keys + private merge | `internal/aiagent/traversal.go:169-205`, `:207-261` | type `identity` hardcoded |
| G8 | `cmd/lattice/capability` reads inline at `{"cap."+rest, "cap.roles."+rest}` | `cmd/lattice/capability/capability.go:283-311` | feeds `pkgmgr.HeldPermission` |
| G9 | `cmd/loupe/review.go` reads inline — byte-identical to G8 | `cmd/loupe/review.go:486-513` | feeds `pkgmgr.HeldPermission` |
| G10 | `MergeDocs` silently drops `extra`'s `ServiceAccess` + `EphemeralGrants` (`merged := *base`) | `internal/capabilitykv/read.go:67-84` | inert for today's only caller (the platform path's two keys carry neither), a landmine for the next |
| G11 | `pkgmgr` imports `bootstrap`; `bootstrap` imports neither `pkgmgr` nor `capabilitykv` | `internal/pkgmgr/bucketguard.go:7` | the shared helper can live in `pkgmgr` with no cycle. **Superseded by the build (§6, bullet 1):** `bootstrap` now imports `capabilitykv` for the anchor-key derivation. Still no cycle — `go list -deps ./internal/capabilitykv` returns only `substrate` + `substrate/keys`. |
| G12 | `cmd/lattice/query/query.go:65` reads a **caller-supplied** capability key | — | operator inspection, not an authorization-surface read — **non-goal** |
| G13 | `containsSensitiveRefLiteral` is a deliberate, documented advisory pre-flight; the MAC is the real enforcement, and its doc comment says "Do not 'harden' this into a smarter parser" | `internal/pkgmgr/capabilitymaterializer_starlark.go:42-64` | the row's absorbed observation is **already adjudicated upstream** — see §6 |

## 3. Shape

**One read path.** `internal/capabilitykv` keeps ownership of routing + merge; every production reader of
the authorization surface goes through it; the two `HeldPermission` copies become one audited helper in the
package that owns the type.

### 3.1 `capabilitykv.ReadPlatformDoc`

```go
func ReadPlatformDoc(ctx context.Context, reader KVGetter, bucket string,
    systemActorKeys []string, actor string) (*Doc, string, error)
```

Derives via `ClassAwarePlatformKey(systemActorKeys)(actor)` and delegates to `ReadAndMerge`. It is exactly
the key derivation + read step 3 performs, with the gates left to the caller (unchanged posture, G1). The
Processor's own `Authorize` is **not** rerouted through it: step 3 derives its keys through a registry of
path entries (`entry.deriveKeys`), of which the platform entry is one — folding the read into the derivation
would collapse a deliberate seam. The registry's platform entry already calls `ClassAwarePlatformKey`.

### 3.2 `pkgmgr.ReadHeldPermissions`

```go
func ReadHeldPermissions(ctx context.Context, conn HeldPermissionReader,
    systemActorKeys []string, actor string) ([]HeldPermission, error)
```

The one implementation of "what does this actor hold, as step 3 would see it": `ReadPlatformDoc` → project
`platformPermissions[]` to `HeldPermission{OperationType, Scope, Origin}`. `origin` travels with the entry,
as both copies already do (their shared comment is preserved, once). `cmd/lattice/capability` and
`cmd/loupe/review.go` call it and delete their copies.

**This narrows: an ordinary (non-system) actor's `cap.<rest>` anchor doc, if one exists, stops contributing
held permissions.** The bound is monotone — `requesterHolds`/`covers` only ever reads `held` as a positive
authority list — so a smaller set can only deny, never confer. Against the old unconditional two-key union
that holds in every direction, which is what makes the change safe to ship.

**How fresh the system-actor set is, is a per-consumer decision, and only one consumer resolves it per
read.** Resolving it costs a full core-kv `KVListKeys`, so each caller takes the platform's own posture for
its process shape: `cmd/lattice/capability` is a one-shot CLI and resolves per invocation; `cmd/loupe` and
`internal/aiagent`'s `Traverser` resolve once and hold, exactly as `cmd/processor`, `cmd/loom`, `cmd/weaver`
and `cmd/refractor` each do at start-up (G3b).

The consequence, stated plainly because the §1 revoked-operator scenario is what this fire is built on:
**a long-running Loupe does not close that window.** If an operator's role is revoked while Loupe runs and
the anchor lens's `emptyBehavior: delete` declined (§1), Loupe's memo still classifies them as a system
actor and their stale anchor grants still count as held until Loupe restarts. That is not a regression — the
helper it replaced read both keys unconditionally, so the window was equally open and unconditional — but it
is not closed either, and the fire should not be read as closing it. Two considerations decide against a TTL
here: the memo would be new state needing a lifetime designed at every boundary (standing checklist #1), and
a Loupe that re-derived membership faster than the Processor does would disagree with the authority it is
previewing. Consistency with the Processor's own boot snapshot is the more defensible posture. A consumer
that genuinely needs revocation-latency guarantees is a design question, not a memo tweak.

### 3.3 `MergeDocs` becomes total over the `Doc` shape

`ServiceAccess` and `EphemeralGrants` union (concatenate) like `PlatformPermissions`, instead of being
inherited from `base` alone (G10). Inert for the platform path — neither `cap.<actor>` nor
`cap.roles.<actor>` projects those fields — and it removes the silent-drop landmine for the ephemeral and
service paths, whose single-key reads never exercise the merge today. A test pins totality: an `extra` doc
with every field populated loses nothing.

### 3.4 `aiagent` onto the shared path

`ReadCapability` calls `ReadPlatformDoc`; `mergeCapabilityDocs`, `mergeStrings`, `mergePlatformPermissions`
are deleted. The agent then sees exactly the grant set step 3 would honor — including `origin`, which its
own dedup was dropping. Behavioral deltas, both intended: duplicate `{op, scope}` entries are no longer
collapsed (they carry distinct provenance, so collapsing them was the bug), and a non-system actor no longer
sees the anchor doc.

`Traverser` needs the system-actor set. It takes it at construction (from `bootstrap.SystemActorKeys`, the
same way `cmd/processor`, `cmd/loom`, `cmd/weaver`, `cmd/refractor` each obtain it once at start-up) rather
than re-listing core-kv per read.

### 3.5 The gate

`scripts/lint-capability-kv-readers.go` — a new `STRICT`-aware convention gate wired into
`lint-conventions`' family, carrying **two** checks over every non-test Go file outside
`internal/capabilitykv`:

1. **Key derivation.** Building a capability *actor* key from a literal prefix — `"cap."`, `"cap.roles."`,
   `"cap.identity."` — in a concatenation or format string. This is the check that matches the defect class:
   every duplicate this fire found restated the routing, and one of them (`internal/bootstrap`) never went
   near a `KVGet` on the bucket at all. It deliberately does not cover the disjoint families that are their
   own read paths: `cap.svc.`, `cap.ephemeral.`, `cap-read.`, `cap.role-by-operation.`.
2. **Bucket read.** A `KVGet`-family call whose own bucket argument names the capability bucket. Narrower in
   reach — it resolves the argument by name rather than by data flow, so a bucket carried under a name it
   does not know escapes it — and the gate's own residual note states that bound rather than implying
   coverage it does not have.

A violation of either fails unless the file is on a named allowlist carrying its reason in source. Three
entries: `cmd/lattice/query` (operator inspection of a caller-supplied key, G12);
`internal/refractor/pipeline/evaluate.go`, scoped to the one file rather than the subtree (the *producer*
side's `capabilityKeyForActor`, a documented deliberate duplicate that exists to break an import cycle —
the key-derivation check is what makes this exemption load-bearing at all); and `scripts/`. This is the
twice-seen-class mechanization the improvement loop calls for: the class has now been minted twice in the
same bucket (`aiagent`, and the `cmd/lattice`+`cmd/loupe` pair), so the check becomes a lint rather than a
dossier line.

The `scripts/` entry was adjudicated when the gate's first run surfaced six `//go:build ignore`
seed/verify harnesses reading the bucket directly (`scripts/seed-edge-demo.go`, `scripts/seed-showcase.go`,
`scripts/verify-{claim-ceremony,erasure-ceremony,loupe-operator-tier,real-actor-write-auth}.go`):
**`scripts/` is allowlisted as a class.** Those tools inspect raw projection state and never authorize
against it — the same standing as the `_test.go` exemption, and the same rationale §7 already gives for
excluding test reads. The entry's reason states the boundary: a script that needs to know what an actor
*holds* goes through `pkgmgr.ReadHeldPermissions` like every other consumer.

## 4. Increment order + green checks

| Inc | Change | Green check |
|---|---|---|
| 1 | `capabilitykv.ReadPlatformDoc` + `MergeDocs` totality (§3.1, §3.3) | `go test ./internal/capabilitykv/... ./internal/processor/...` |
| 2 | `pkgmgr.ReadHeldPermissions`; `cmd/lattice/capability` + `cmd/loupe/review.go` onto it (§3.2) | `go test ./internal/pkgmgr/... ./cmd/lattice/... ./cmd/loupe/...` |
| 3 | `aiagent` onto the shared path (§3.4) | `go test ./internal/aiagent/... ./examples/...` |
| 4 | `scripts/lint-capability-kv-readers.go` (§3.5) | `STRICT=1 go run ./scripts/lint-capability-kv-readers.go` |

Full bar before merge: `go build ./...` · `make vet` · `golangci-lint run ./...` ·
`STRICT=1 go run ./scripts/lint-conventions.go` · `go run scripts/lint-board.go` ·
`go test ./... -p 4` **with `POSTGRES_TEST_DSN` exported** (REMOTE.md §3 — a suite without it is falsely
green).

Review depth: **full 3-layer adversarial** — capability-plane change, regardless of size (`agents/steward/SKILL.md` §4).

## 5. In-scope gotchas

Standing checklist (`agents/fire-brief-template.md`) — the four that bite here:

- **#2 every census is a premise** — G5 is a *rotted* citation caught by re-verification; the same
  discipline applies to the reader census itself, re-run at build time.
- **#3 a negative test needs its positive vector proven first** — the narrowing in §3.2 must be pinned by a
  test that fails before the change (an ordinary actor with a seeded anchor doc over-reports today).
- **#4 a demoted mechanism needs EVERY obligation enumerated** — `mergeCapabilityDocs` is *replaced*, not
  deleted: its dedup, its field coverage, and its `origin` posture each get accounted for (§3.4), not just
  the first one found.
- **#6 precedent may carry debt** — `cmd/loupe/review.go` is the mirror of `cmd/lattice/capability`; both
  carry the same defect. Copying either would have propagated it a third time.

Processor dossier (`docs/components/processor.md`), applicable entries:

- **A gate's negative test must first prove its positive vector reaches the gate** — seen three times, all
  in one item; a fourth sighting in a *different* item mechanizes it. This fire's §3.2 test is exactly that
  shape, so state the positive vector explicitly.
- **A tombstone retains the prior document, so a reader that does not filter `isDeleted` sees a revoked
  declaration as live** — relevant to why the anchor key's presence is not evidence of current membership.

Pkgmgr dossier: **canonicalName and the instance key segment are different namespaces** — the
`"cap." + rest` concatenations in G8/G9 are the same conflation in miniature (a key *segment* built by string
surgery rather than by the owning derivation).

## 6. Adjacent finds

- **The board row's `gateway/rolesanchors` citation is false at head** (G5). It is not filed anywhere as a
  defect — it is a design-doc citation that rotted, corrected here and in the row at close.
- **`containsSensitiveRefLiteral`** (G13) — the row says it absorbs this "same root cause, a syntactic proxy
  for a semantic property". **Adjudicated: no change.** The proxy is deliberate, documented, and
  *deliberately not the enforcement point*; `sensitive-ref-mac-provenance-design.md` §7 states the lint stays
  "advisory pre-flight for what it can't see" precisely because a computed value defeats any static scan, and
  the `internal/vault.RefMACPurpose` MAC verified through the bridge's `decryptref` RPC is what actually
  rejects a forged ref. Its doc comment says in terms: "Do not 'harden' this into a smarter parser." Nothing
  to build; the row's clause is answered, not deferred.
- **`cmd/lattice/query/query.go:65`** (G12) — a bare read of a caller-supplied capability key. Operator
  inspection, the Loupe-class exception; allowlisted by the §3.5 gate with that reason recorded, not changed.
- **A fourth restatement of the anchor key derivation, in `internal/bootstrap`** — `primordial.go`'s
  `capabilityKeyForIdentity` builds `"cap.identity." + id` and reads it through a `js.KeyValue` handle, a
  shape the bucket-argument check cannot see at all. Surfaced by review, **fixed in this fire**: routed
  through `capabilitykv.CapabilityKeyFromActor` (`capabilitykv` is a leaf, so there is no cycle), which is
  also what makes the §3.5 key-derivation check clean.
- **`internal/controlauth/checker.go` composes its own key derivation with `ReadAndMerge`** rather than
  calling `ReadPlatformDoc`. **Adjudicated: correct as it stands, no change.** It branches on
  `RbacRolesActive`, and `ReadPlatformDoc` deliberately does not take that flag (§3.1) — controlauth's
  derivation is not a restatement of the routing, it is a second, flag-aware routing that legitimately
  belongs to it, and it already reads and merges through the shared package.
- **The Processor's boot-time `SystemActorKeys` snapshot over a live graph predicate** (G3b) — an identity
  granted the primordial `operator` role after the Processor boots gets an anchor doc the running Processor
  will not route to it until restart. **Adjudicated: not a defect, nothing filed.** It is a named, designed-
  around constraint: `cmd/processor/main.go:138` records the once-at-boot decision and why the *opposite*
  choice (an rbac-install probe latched at startup) was the bug that blocked capability mode, and
  `packages/demo-operator/package.go:29-39` shows the platform routing around it deliberately — a package
  role distinct from the primordial one, precisely so a post-boot grant "authorizes immediately". Recorded
  here so a later fire does not re-derive it as a finding.

## 7. Non-goals

- The Processor's step-3 registry and its gates (reserved-op, lane, scope) — unchanged.
- Contract #6 — unchanged; this fire deletes restatements of it, adds none.
- The producer side (`internal/refractor/capabilityenv`, `pipeline/evaluate.go:1352-1361`) — out of scope;
  `capabilityKeyForActor` there is a documented deliberate duplicate that exists to break an import cycle.
- Test-file reads of the bucket (42 files) — verification reads, not authorization reads.
- The `cap-read.*`, `cap.svc.*`, `cap.ephemeral.*` families' own read paths — single-key, already canonical.

## 8. Build note / checkpoint

*(amended in the same commit as each increment)*

**2026-08-22 · fire branch `claude/great-lamport-ndovi4` — all four increments built, three cold adversarial
reviews run, item CLOSED.**

Deviations from §3, each argued above where it lands:

- `aiagent.ReadCapability` takes the full `vtx.<type>.<id>` actor key rather than the bare id §3.4 assumed.
  Prefixing internally would have preserved the hardcoded `identity` type the section calls out; every
  caller already held the full key.
- `MergeDocs`' "base wins" for the identity/provenance scalars is equivalent to the merge it replaced **only
  because `ClassAwarePlatformKey` returns `[anchor, roles]` in that order**, making the anchor the base. The
  equivalence now rests on key order rather than on named parameters, and `MergeDocs`' comment states that
  invariant so a later change to the key order cannot silently flip which projection's `key`/`projectedAt`
  reach the auth trace. `ProjectedFromRevisions` changed from anchor-only to a merge of both — strictly more
  provenance, no grant impact, no consumer.
- **`cmd/refractor` never loaded the primordial identifier table**, discovered while fixing the CLI's half of
  the same class. Fixing it re-arms a *second* consumer that had been silently dead:
  `bootstrap.CapabilityReadLensID`, empty in every shipped refractor, is the `RuleID` of the cap-read
  `keyshredded.NullifyTarget` (`cmd/refractor/main.go:626`) — so a `ShredIdentityKey`'s cap-read
  nullification returned `ErrRuleNotRegistered`, nak-looped to the redelivery cap, and gave up. It runs now,
  and the privacy-critical `PauseRule` branch (`keyshredded/manager.go:391-396`) becomes reachable for the
  first time. Shipped deliberately: the prior state was shred residue, and the newly-live behavior is
  ratified, tested code — not a new mechanism this fire invented.
- The §3.5 gate grew a second, primary check (key derivation) after review showed the bucket-argument check
  mechanized the wrong half of the class.

What review found, and what it cost:

- **The narrowing left `lattice capability review approve` unable to see any system actor at all.**
  `bootstrap.RoleOperatorID` is populated by `bootstrap.Load`, which `cmd/lattice` performs only under its
  `bootstrap` subcommand — so in the shipped CLI `SystemActorKeys` filtered on an empty role id, matched
  nothing, and returned an empty set with no error. Every actor routed as ordinary. Fail-closed, but a real
  regression against the unconditional union it replaced, and invisible to the new tests because they call
  `testutil.EnsurePrimordials` — they loaded the file the binary never loads. Fixed on both halves:
  `SystemActorKeys` now refuses an unloaded identifier table loudly instead of returning an empty set, and
  the capability subcommand loads the identifiers the way `cmd/lattice/bootstrap` does.
- **§3.2's safety argument was falsified by the code under it** — it claimed the set is "resolved fresh at
  each read", which is true only of the one-shot CLI. §3.2 is rewritten above: the conclusion (monotone,
  narrower-or-equal in every direction) survives, the reason did not, and the Loupe staleness window is now
  stated rather than implied away.
- The FR19 north-star test had been kept green by declaring the cold-start agent root-equivalent, which
  retired the very branch this fire narrows. Reseeded onto the ordinary-actor route.
- Three reviewers independently mutation-tested the new tests: reverting the class-aware routing fails five
  tests across five packages, reverting `MergeDocs` totality fails the totality test, re-introducing the
  `{op, scope}` dedup fails the origin test. No vacuous pins.
