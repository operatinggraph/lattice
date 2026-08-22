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

That set is not static. `bootstrap.SystemActorKeys` (`system_actors.go:35-60`) discovers it from the graph
— identities holding the primordial `operator` role through a live `holdsRole` link — so membership changes
with any `AssignRole` / revocation, and the anchor doc's presence for a given actor is a projection-timing
property, not a fixed one. A reader that decides routing by string concatenation instead of by that
predicate is answering a different question from the one step 3 answers.

Two of the three sites make that divergence load-bearing. `cmd/lattice/capability`'s
`heldPermissionsForActor` and `cmd/loupe/review.go`'s `heldPermissionsForCapabilityActor` are byte-identical
copies that build `[]pkgmgr.HeldPermission` — the bound on what a capability **grant proposal may confer**
(`internal/pkgmgr/capabilitymaterializer.go:208-236`, `requesterHolds`). Every permission they over-report
is a permission a proposal may hand out. `HeldPermission`'s own doc comment names this exact hazard:
"whoever builds the real caller … MUST read this projection for the actual requesting/approving actor
(op.actor), fresh, every time — a stale or wrong-actor slice defeats the entire scope check." Both callers
then got built by hand, each re-deriving the key set.

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
| G3 | System-actor set is graph-derived and dynamic (live `holdsRole → operator`) | `internal/bootstrap/system_actors.go:35-60` | not a fixed kernel list |
| G4 | Processor reads through the helper, then applies reserved-op + lane + scope gates | `internal/processor/step3_auth_capability.go:322-324`, `:630-646`, `:553-574` | |
| G5 | `gateway/rolesanchors` **already** routes through `capabilitykv.RolesKeyFromActor` + `ReadAndMerge` | `internal/gateway/rolesanchors/rolesanchors.go:103-118` | **the board row's citation has ROTTED — it is not an inline reader** |
| G6 | `internal/controlauth` reads through the helper | `internal/controlauth/checker.go:148` | |
| G7 | `aiagent` reads inline at two hardcoded keys + private merge | `internal/aiagent/traversal.go:169-205`, `:207-261` | type `identity` hardcoded |
| G8 | `cmd/lattice/capability` reads inline at `{"cap."+rest, "cap.roles."+rest}` | `cmd/lattice/capability/capability.go:283-311` | feeds `pkgmgr.HeldPermission` |
| G9 | `cmd/loupe/review.go` reads inline — byte-identical to G8 | `cmd/loupe/review.go:486-513` | feeds `pkgmgr.HeldPermission` |
| G10 | `MergeDocs` silently drops `extra`'s `ServiceAccess` + `EphemeralGrants` (`merged := *base`) | `internal/capabilitykv/read.go:67-84` | inert for today's only caller (the platform path's two keys carry neither), a landmine for the next |
| G11 | `pkgmgr` imports `bootstrap`; `bootstrap` imports neither `pkgmgr` nor `capabilitykv` | `internal/pkgmgr/bucketguard.go:7` | the shared helper can live in `pkgmgr` with no cycle |
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

**This narrows.** An ordinary (non-system) actor's `cap.<rest>` anchor doc, if one exists, stops
contributing held permissions — matching what step 3 would honor. The direction is fail-closed: a proposal
can only confer less than before, never more.

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
`lint-conventions`' family: a non-test Go file outside `internal/capabilitykv` that names the capability
bucket (`bootstrap.CapabilityKVBucket` / `"capability-kv"`) **and** performs a `KVGet`/`KVGetMulti` on it
fails, unless it is on a named allowlist with a stated reason. Allowlist v1: `cmd/lattice/query` (operator
inspection of a caller-supplied key, G12) and `internal/refractor` (the *producer* side). This is the
twice-seen-class mechanization the improvement loop calls for: the class has now been minted twice in the
same bucket (`aiagent`, and the `cmd/lattice`+`cmd/loupe` pair), so the check becomes a lint rather than a
dossier line.

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

## 7. Non-goals

- The Processor's step-3 registry and its gates (reserved-op, lane, scope) — unchanged.
- Contract #6 — unchanged; this fire deletes restatements of it, adds none.
- The producer side (`internal/refractor/capabilityenv`, `pipeline/evaluate.go:1352-1361`) — out of scope;
  `capabilityKeyForActor` there is a documented deliberate duplicate that exists to break an import cycle.
- Test-file reads of the bucket (42 files) — verification reads, not authorization reads.
- The `cap-read.*`, `cap.svc.*`, `cap.ephemeral.*` families' own read paths — single-key, already canonical.

## 8. Build note / checkpoint

*(amended in the same commit as each increment)*

- **2026-08-22 · fire branch `claude/great-lamport-ndovi4`** — brief compiled and ratified; increments not
  yet started.
