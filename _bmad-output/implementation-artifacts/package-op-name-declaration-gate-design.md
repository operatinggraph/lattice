# Package-owned operation names in `internal/` are declared, not bare

**Status: ✅ Winston-ratified — build-ready** (2026-08-28). No frozen contract changes; no architectural
fork. Board row: `[Tooling] internal/ hardcodes package-owned operation names, undeclared` (★★ / S,
Andrew-asked 2026-08-25), `backlog/lattice.md`.

## 1. The problem

A **package-owned** operation name — `ClaimIdentity`, `DetachObject`, `StartLoomPattern` — is declared by the
package that owns it, in its `DDLs()` under `PermittedCommands`. Engine code under `internal/` nonetheless
writes those names as bare Go string literals: to submit the op, to route inbound work on it, or to make a
core policy decision *about* it.

The third kind is the hazard. A core-owned policy set keyed on names the core does not own is a coupling
with no declared counterparty, and nothing tells a reader — or a reviewer — that it exists. The live
instance the board row names:

| symbol | file | set |
|---|---|---|
| `nfrS6Operations` | `internal/processor/nfr_s6_wire_shape.go:32` | `{ClaimIdentity, CompleteCredentialLink}` |
| `rawCredentialCarveOut` | `internal/gateway/gateway.go:312` | `{ClaimIdentity, CompleteCredentialLink}` |

Two sets, two packages, byte-identical membership, maintained independently, no gate. `rawCredentialCarveOut`
is the set of ops the Gateway must submit under the **raw** authenticated actor because their scripts hash
`op.actor` to derive a `credentialindex` key. `nfrS6Operations` is the set whose rejections must collapse to
one wire shape. An op added to the carve-out but not to the equalization set hashes a credential and then
answers with a distinguishable rejection — a live enumeration oracle. `nfrS6Operations`' own doc comment
already names this exact failure mode for a different reason ("would have silently uncovered a real
enumeration oracle without failing a single test"); the cross-package instance of it is unguarded.

## 2. What ships

Two mechanisms, one for each half of the row.

### 2.1 A default-deny declaration gate (`scripts/lint-conventions.go`)

Every package-owned operation-name string literal in a non-test `internal/**/*.go` file must carry an
`# op-name: (<category>) <why>` annotation on the line or in the comment block directly above it. An
unannotated literal fails `STRICT=1`.

**The universe is derived, never listed.** The gate reads every non-test `packages/**/*.go` file, collects
the string literals inside `PermittedCommands: []string{…}` blocks, and treats exactly that set as the
package-owned operation names. A hand-maintained list would rot the day a package adds a verb; deriving it
from the canonical declaration site means the gate's reach tracks the packages automatically. (This is why
the row's own "12 non-test engine files" estimate, made 2026-08-25, reads 15 today: a hand census misses
sites a derived one cannot.)

**Categories** — two, and the author picks one:

- **`(policy)`** — the core decides something *about* this op: a policy set, carve-out, reserved-verb list,
  or a branch keyed on the name. A `(policy)` declaration must additionally carry a **`pin=`** sub-field
  naming what keeps the set honest (the test or gate that catches drift). This is the one category that
  encodes a coupling to a package the core does not own, so it is the one that must name its guard.
- **`(submits)`** — the operation is submitted: built and published by this engine, or named in the remedy
  an operator submits on its instruction.

`<why>` is required on both, in the author's own words.

**A third category, `(routes)` — "this engine dispatches inbound work on this operation name" — was drafted
and dropped at build time (2026-08-28), and the vocabulary above is the ratified one.** The live census
carries no such site: the one that reads that way, `internal/bridge/dispatch.go`'s `replyOpReads` switch, is
assembling the reply op the Bridge itself posts, which is `(submits)`. A category with no member is
speculative API that invites miscategorization, and the processor's own routing is dynamic (through the DDL
cache), so no literal reaches it. A `(routes)` annotation therefore fails as the unknown category it is; a
router, should one ever appear, is a deliberate amendment rather than a slot left standing open for it.

**The derived universe also buys a rename detector, and it ships.** When a package retires or renames a
verb, the engine's literal silently leaves the universe — the undeclared check stops matching it, so a site
declared against a real counterparty now points at nothing and trips nothing. The declaration outliving its
subject is the only remaining trace, so an `# op-name:` annotation whose covered span names *no*
package-owned operation is itself denied. It cannot fire on a live site, because a correctly-placed
declaration always covers at least one member — which also makes it catch an annotation that drifted off its
statement (a blank line between comment and code leaves it covering nothing).

**Required sub-fields have precedent in this file.** `# read-posture: (e)` already requires the annotation
to name `relation=` and record `epoch=` (`checkReadPosture`); `pin=` is the same idiom.

**There is deliberately no exemption category**, mirroring `authCtxTargetShapes`, whose absence of one is
also deliberate. The live census is 30 literals across 15 files and every one is a real operation name —
zero coincidental collisions — so an escape hatch today would buy nothing but a lazy out. A genuine
collision later is a deliberate amendment, made then.

**Scope excludes `cmd/**` and `internal/spike/`.** A `cmd/` binary submitting operations is its whole job,
and the row's hazard is engine policy. That exclusion is a division of labor rather than a gap: the `cmd/`
tier is already governed — `lint-app-op-descriptors.go` ratchets each vertical app's distinct hardcoded
op-literal count against a pinned per-app ceiling, and `lint-facet-discovery.go` bans op literals outright
in `cmd/facet` beyond five ceremony ops. `internal/**` was the last ungoverned tier. `internal/spike/` holds
standalone benchmark harnesses (already excluded from this file's embedded-NATS gate for the same reason);
its one hit is a literal `PermittedCommands` fixture — a declaration, not a use.

### 2.2 A containment invariant for the carve-out pair

The drift between §1's two sets is a semantic invariant over two Go symbols, so it ships as a **test**, not
as text-matching in a lint:

> `rawCredentialCarveOut ⊆ nfrS6Operations`

Containment, not equality, is the sound direction. An op in the carve-out sees the raw credential and hashes
it, so its rejections distinguish a bound credential from an unbound one and it **must** be equalized.
Equalizing an op that takes no raw credential is merely conservative and costs nothing, so the reverse
inclusion is not required. `internal/gateway` already imports `internal/processor` (`gateway.go:31`), so the
processor exports a predicate over its set and the gateway-side test asserts containment.

This test is what the two sites' `pin=` sub-fields name.

## 3. What the gate deliberately does not do

It does not consolidate duplicated literals. `internal/privacyworker` and `internal/refractor/keyshredded`
both submit `RecordShredFinalization`, deliberately — they are two independent consumers of one event, and
the second's own comments say so. Forcing them onto a shared constant would couple two components the design
keeps apart. The categories make an intentional duplication *legible* (`(submits)` on both) rather than
demanding it be removed; only a `(policy)` coupling has to name a guard.

## 4. Green bar

- `STRICT=1 go run ./scripts/lint-conventions.go` — clean, with the new check live and every `internal/`
  site annotated.
- The gate's self-test covers the deny path **and** proves its positive vector reaches the gate (the
  processor dossier's third entry: a negative test that passes because the input never arrived pins nothing).
- `go test ./internal/gateway/... ./internal/processor/...` green, containment test included, and the
  containment test fails when the carve-out is mutated to hold an unequalized op.
- `go build ./...`, `make vet`, `golangci-lint run ./...` clean.

---

## `op-name` gate fire brief (build note, 2026-08-28)

**1. Scope sentence.** Default-deny the bare package-owned operation-name literal in non-test `internal/`
engine files, author declares the category (§2.1), and pin the `rawCredentialCarveOut` ⊆ `nfrS6Operations`
containment the row names as the live drift hazard (§2.2). Green bar: §4.

**2. Verified touch-list** (live census, 30 literals / 15 files, `git ls-files` at `24c020b`):

- `scripts/lint-conventions.go` — the gate. Registration `:1317-1321` (the `postureScoped` block);
  annotation regex block `:405-417`; shape vocab `:650-681`; `annotationSpans` `:745`; `checkReadPosture`
  `:2048`; `checkAuthContextTarget` `:2090`; self-test `:2620`; header contract `:83-109`.
- Annotation sites (one comment line each):
  `internal/bootstrap/strandedretire.go:93,104` · `internal/bridge/dispatch.go:87,91` ·
  `internal/gateway/gateway.go:298,306,706` · `internal/loom/engine.go:24,25,28` ·
  `internal/objectmanager/cascade.go:216` · `internal/pkgmgr/installer.go:1193,1194` ·
  `internal/pkgmgr/opmetaretirement.go:178` · `internal/privacyworker/manager.go:84,85` ·
  `internal/processor/commit_path.go:430` · `internal/processor/nfr_s6_wire_shape.go:33,34` ·
  `internal/processor/step3_auth_capability.go:479,480` ·
  `internal/refractor/classkeyshredded/manager.go:524` · `internal/refractor/keyshredded/manager.go:482` ·
  `internal/weaver/strategist.go:36,37,38,39,522,524`.
- `internal/processor/nfr_s6_wire_shape.go` — export the set predicate for §2.2.
- `internal/gateway/*_test.go` — the containment test.

`internal/spike/starlark/api_ergonomics.go:124` is in the raw census and **out of scope** (§2.1).

**3. Precedents to mirror.** `checkAuthContextTarget` (`:2090`) — default-deny + author-declares-shape +
unknown-shape rejection + required `<why>`, the closest shape by construction. `checkReadPosture` (`:2048`)
— the required sub-field idiom (`relation=`/`epoch=`) that `pin=` copies. `permissionOperationType`
(`:405`) — precedent for this file already parsing package sources for declarations. `platformCmds`
(`:900`) — precedent for a path-scoped exclusion set.

**4. Increment order.**
1. Gate: regex + vocab + `checkOpName` + registration + header contract + self-test cases (deny, accept,
   unknown category, missing `<why>`, missing `pin=`, and the positive-vector proof).
   Green: `go run ./scripts/lint-conventions.go` self-test passes; the run now reports the 30 sites.
2. Annotate the 15 files. Green: `STRICT=1 go run ./scripts/lint-conventions.go` clean.
3. Containment: export the processor predicate, add the gateway test. Green:
   `go test ./internal/gateway/... ./internal/processor/...`, plus a revert-proof (mutate the carve-out,
   watch it fail).
4. Full gates (§4).

**5. In-scope gotchas.**
- **CLAUDE.md no-changelog** — annotations describe what the literal *is*, never that a gate was added.
- **Processor dossier** — *"A gate's negative test must first prove its positive vector reaches the gate"*
  (third sighting): two taxonomy gates shipped with tests that passed by planting input the Processor
  refuses upstream. The self-test must prove an unannotated literal is *seen* before asserting it is denied.
- **Processor dossier** — *"A silently-rejected op logs at Info"*: not load-bearing here (no runtime path).
- **Standing checklist #2** — every census is a premise: the 30/15 counts above are live-derived at
  `24c020b`, not copied from the row's 2026-08-25 estimate of 12 (which was already stale).
- **Standing checklist #3** — the containment test is proven by mutating the carve-out and watching it fail.
- **Standing checklist #6** — precedent may carry debt: `checkAuthContextTarget` is verified against the
  read-posture contract it claims to mirror, not copied on trust.
- No `packages/` content changes, so no manifest version bump is in play.

**6. Adjacent finds.**

- `internal/privacyworker` / `internal/refractor/keyshredded` both submit `RecordShredFinalization` —
  verified **deliberate** (two independent consumers of one event, documented at
  `keyshredded/manager.go:10,79,273`), not a defect; §3. No row.
- **A live defect, found while categorizing `commit_path.go:430` — FIXED in this fire (`b8ecdff`), not
  filed.** The claim-attempts counter's two emission legs disagreed about their subject: the rejection leg
  (`handleStubFailure`) keys on `isNFRS6Operation` — the whole equalized set — while the post-commit success
  leg keyed on a bare `"ClaimIdentity"` literal. `CompleteCredentialLink` is in that set, so its failures
  were counted and its successes dropped. Because every member of the set answers its caller with one fixed
  wire shape *by construction*, this counter is an operator's only view of what the operation did: the leg
  asymmetry made a working credential-link flow read as one that never succeeds, and hid a real failure
  spike behind a baseline that was already total failure. Both legs now key on the set;
  `docs/observability/health-kv-schema.md` moved with the emission (its row said "per `ClaimIdentity` call",
  which never described the rejection leg either, and its outcome enum was missing `internal-fault`). The
  fix *retires* one of the 29 sites rather than annotating it — the gate's first application finding a
  defect in the mechanism it governs.

**7. Non-goals.** `cmd/**`. Kernel-seeded verbs (`InstallPackage`/`UpgradePackage`/`UninstallPackage`) are
core-owned, not declared by any package's `PermittedCommands`, and so fall outside the derived universe by
construction — `privilegedLaneAllowlist` and `isPackageLifecycleOp` are therefore untouched. Consolidating
duplicated literals onto shared constants (§3). Changing any op's runtime behavior.
