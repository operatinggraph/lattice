# Grant-edge provenance — the `grantedBy` link declares its origin, so a forged grant is drift

**Status: ✅ Winston-ratified — build-ready** · Winston (Steward, Lattice), 2026-08-25
**Board row:** `planning-artifacts/backlog/lattice.md` → *Security & trust boundary* →
"[rbac] A `grantedBy` link carries no provenance, so a forged grant edge reconciles clean" (★★ / M).
**Predecessor:** `grant-provenance-runtime-permission-minting-design.md` (Andrew-ratified 2026-08-13) —
this extends its §6 invariant from the permission **vertex** to the grant **edge**.

## 0. Why this is a Steward item, not a designer pass

The row carried `📐 needs designer pass · no-pattern: provenance on a grantedBy link`. That stamp fails the
honest-gate test (`backlog/lattice.md` "Two filing gates" (2)): the named absent pattern is not absent.

- Link documents already carry an arbitrary `data` map, through the same builders the vertex plane uses —
  `docLink` (`internal/pkgmgr/build.go:1054-1066`), `MakeLinkEnvelope`
  (`internal/bootstrap/primordial.go:779-783`), rbac-domain's `make_link` / `revive_link`
  (`packages/rbac-domain/ddls.go:143-161`). Every grant-edge writer passes `{}` today; none is unable to.
- The provenance *stamp* is shipped and ratified on the permission vertex (`data.origin` =
  `package` | `runtime`, `internal/pkgmgr/build.go:379-380`, `packages/rbac-domain/ddls.go:323`).
- The *reconciliation* machinery is shipped: five provenance classes, a kernel constant key set, a
  drift/notice split and a CI gate (`internal/pkgmgr/permissionreconcile.go`, `make
  verify-permission-provenance`, `.github/workflows/ci.yml:312-326`).

Applying an established, ratified pattern to one more object is execution
(`agents/steward/SKILL.md` §2.5). The row is re-stamped as steward work by this design.

## 1. The defect, stated exactly

Authorization travels the edge, not the vertex. Step 3 reads `cap.roles.<actor>`, which the
`capabilityRoles` lens builds by walking
`(identity)-[:holdsRole]->(role)<-[:grantedBy]-(perm:permission)`
(`packages/rbac-domain/lenses.go:91-103`). A permission vertex that no role points at confers nothing; a
`grantedBy` edge onto an existing high-value permission confers everything that permission names.

`GrantPermission` accepts any live permission key and any live role key with no manifest check
(`packages/rbac-domain/ddls.go:399-421`), and the edge it writes carries an empty body. So the cheapest
escalation forges **no vertex at all** — it mints one edge — and the permission reconciler is blind to it
**by construction**: it enumerates `vtx.permission.*` only, and says so
(`internal/pkgmgr/permissionreconcile.go:249-254`: *"`grantedBy` links are not reconciled … Link provenance
carries no stamp to reconcile against; no ratified pattern exists to close this yet."*). That comment is
the item. This design falsifies it and it is rewritten in the same change.

## 2. Grounding ledger

| # | Fact | Citation |
|---|---|---|
| G1 | Four writers of `lnk.permission.<id>.grantedBy.role.<id>` exist, and only four. Kernel seed (meta + install perms → operator role); package install; runtime `GrantPermission`; runtime `RevokePermission` (tombstone). | `internal/bootstrap/primordial.go:776-798`; `internal/pkgmgr/build.go:390-399`; `packages/rbac-domain/ddls.go:399-421`; `:422-435` |
| G2 | All four write `data: {}` / `nil` on the edge. No writer stamps anything. | `primordial.go:783`; `build.go:396-398` (`docLink(..., nil)`); `ddls.go:174-175` (`grant_link` hardcodes `{}`) |
| G3 | A package's `declaredKeys` manifest record already contains **`lnk.*` keys as well as `vtx.*`** — `addCreate` appends every written key. So the declared side for a package-authored grant edge exists today, unused. | `internal/pkgmgr/build.go:71`, snapshot at `:430` |
| G4 | The kernel's six grant edges are derivable from the same six bootstrap globals the vertex plane already resolves: for each kernel permission id, `lnk.permission.<id>.grantedBy.role.<RoleOperatorID>`. | `primordial.go:776-798`; `permissionreconcile.go:736-750` |
| G5 | `revive_link` re-grants a tombstoned edge as an `update`, carrying the same body the create would. A stamp added to the create arm must be added to the revive arm or a re-grant launders the stamp off. | `packages/rbac-domain/ddls.go:149-175` |
| G6 | The `capabilityRoles` Cypher projects vertex properties only; it binds no relationship variable. Projecting a link's `data.origin` is not expressible in the shipped spec. | `packages/rbac-domain/lenses.go:91-103` |
| G7 | The gate's honest anchor is the compiled package registry, not Core KV — `checkRegistryAnchor` already runs that pass for vertices. | `scripts/verify-permission-provenance.go:160`, `:221-288` |

## 3. The shape

**The invariant, extended.** *Every grant edge declares its origin, and an edge whose origin cannot be
accounted for is drift.* Three authoring channels, three accountable classes, mirroring the vertex plane
one-for-one:

| class | how it is recognized | verdict |
|---|---|---|
| `kernel` | key ∈ the six derived kernel grant-link keys (G4) | clean |
| `package` | `data.origin == "package"`, `data.declaredBy` names an installed package whose `declaredKeys` contains this link key, and the key derives from that package's own `(permission, role)` pair | clean; any of the three failing is drift |
| `runtime` | `data.origin == "runtime"` | **notice** — the ratified second grant channel (Branch A), inventory only |
| `unstamped` | no origin, key IS in some installed package's `declaredKeys` | notice — pre-stamp install, healed by upgrading that package |
| `unrecognized` | no origin and undeclared, **or** an origin that is neither `package` nor `runtime` | **drift** |

The `unstamped` / `unrecognized` split is the load-bearing one, exactly as on the vertex plane: without it,
deleting one JSON field is the cheapest way to make a forged edge reconcile as a legacy install.

**What this fire ships is DETECTION, and that is the whole of it.** Step 3 is not changed; no grant is
refused on link origin. Two grounded reasons, not timidity:

1. **It is not expressible.** The lens cannot project a relationship property (G6). Enforcement would
   need anchor derivation to carry link bodies — a genuinely new Refractor mechanism, and the one thing
   here that *would* be a designer pass.
2. **It is the predecessor's own sequence.** The vertex plane shipped provenance first
   (`f464c7a5`) and the reserved-set refusal second, for the migration reason that binds here too: every
   grant edge alive today is unstamped, so a refusal keyed on the stamp would fail closed across the whole
   live grant set on the deploy that shipped it.

**`holdsRole` is deliberately not included, and this is not an omission.** The other half of the traversal
is minted by the same helper, but it has **no declared side to reconcile against**: role assignment is
inherently a runtime act (`AssignRole`), so a legitimate `holdsRole` edge is undeclared by every installed
package. Reconciling it against `declaredKeys` would classify the normal case as drift — the pattern does
not transfer, and stretching it would be substituting an adjacent mechanism. The kernel's one seeded
`holdsRole` edge (`primordial.go:800-805`) is already covered by `verify-kernel`.

## 4. Contract surface

**None. No `docs/contracts/*` edit is required or prepared, and that is a decision, not an oversight.**
Contract #6 §6.1 (`docs/contracts/06-capability-kv.md:84-113`) governs what a permission vertex's origin
*confers*; this fire adds no normative authorization rule, changes no projected capability-doc field
(§6.4's table is untouched — the lens projects nothing new), and refuses nothing. It is an auditor's
reconciliation over a stamp the contract already requires of "any future runtime grant channel" (`:113`).
Enforcement keyed on edge origin *would* need a §6.1 clause; that lands with the mechanism, not before it.

## 5. Decomposition — one fire, three increments

**Inc 1 — stamp the two authoring channels** *(mechanical)*. Package install's grant edge carries
`{origin: "package", declaredBy: <def.Name>}` (`build.go:396-398`), mirroring its own vertex at `:379-380`.
`grant_link` takes a `data` argument; `GrantPermission` passes `{origin: "runtime"}`, `AssignRole` keeps
`{}` (§3's `holdsRole` exclusion). Both the create and the **revive** arm carry it (G5). rbac-domain's
package version bumps in lockstep. **Bootstrap is NOT touched** — the kernel class is recognized by key
set, as the vertex plane already does it.
*Green:* `go test ./internal/pkgmgr/... ./packages/rbac-domain/...`; `DIFF_BASE=<base> go run
./scripts/lint-package-version.go`.

**Inc 2 — the link reconciler** *(posture-changing: new classification plane over the capability graph)*.
`LiveGrantLink`, `GrantProvenance`'s five classes, `ReconcileGrantLinks`, `kernelGrantLinkKeys` derived
from the same bootstrap globals; enumeration of `lnk.permission.` folded into `gatherPermissionInputs`;
results carried on `PermissionReconciliation` as their own slices. The `:249-254` non-goal comment is
rewritten in this increment's commit — it is the falsified claim.
*Green:* `go test ./internal/pkgmgr/...`, with each class proven by reverting its arm.

**Inc 3 — the gate** *(mechanical)*. `scripts/verify-permission-provenance.go` reports link findings and
exits non-zero on link drift; `checkRegistryAnchor` gains the link pass.
*Green:* `make verify-permission-provenance` against a live stack; planted forgery reds, removal greens.

**What the registry anchor can and cannot pin (settled at build, 2026-08-25).** The permission-side
anchor is exact: a declared grant edge's `lnk.permission.<permID>` must name a permission the package's
compiled Definition derives, and the edge count per permission must equal that spec's `len(GrantsTo)`.
**The role side is not pinnable from source** — a `PermissionSpec` names its target by *canonical name*
(`packages/rbac-domain/permissions.go:31`, `GrantsTo: []string{"operator"}`), which `cmd/lattice-pkg`
resolves to a role id at install time, so the binary never holds the id the edge points at. The anchor
therefore verifies *which permission* a declared edge may grant and *how many* edges may exist, and
accepts whichever role the declared key names. Resolving role ids from Core KV would put the writer on
both sides of the comparison — the echo-not-a-check class in `docs/components/pkgmgr.md`'s dossier — so
it is stated as a residual rather than closed with a false anchor.

**Review depth:** capability-plane, so **full 3-layer adversarial** over the item's whole diff at close,
plus a full pass on Inc 2.

## 6. Test strategy

- Unit, `internal/pkgmgr/permissionreconcile_test.go`: one test per class, each proven by reverting its
  classification arm; the `unstamped`-vs-`unrecognized` split proven with the field-deletion vector.
- The forged-edge vector end to end: a live `grantedBy` edge onto a kernel permission, authored by neither
  install nor `GrantPermission`, is drift.
- The launder vector: revoke then re-grant a package-origin edge; the revived edge must not come back
  unstamped (G5).
- Determinism: findings sorted, mirroring `TestReconcilePermissions_DeterministicOrder`.

---

### Fire brief (build note, 2026-08-25)

**1. Scope sentence (verbatim, §5).** *Stamp `origin` on the `grantedBy` grant edge at both authoring
channels, extend the live-vs-declared permission reconciler and its CI gate to the link plane, and rewrite
the non-goal comment this design falsifies. Detection only; no enforcement, no `holdsRole`, no contract
edit.*

**2. Verified touch-list (re-checked live this fire, all citations hold):**
- `internal/pkgmgr/build.go:390-399` — install's grant-link loop; `:379-380` the vertex stamp to mirror;
  `:71` `addCreate`; `:1054-1066` `docLink`.
- `packages/rbac-domain/ddls.go:143-147` `make_link`, `:149-161` `revive_link`, `:164-175` `grant_link`,
  `:376` `AssignRole` call site, `:414` `GrantPermission` call site.
- `packages/rbac-domain/package.go:31` — `Version: "0.3.6"`.
- `internal/pkgmgr/permissionreconcile.go:20-23` origin consts, `:25-66` the five classes, `:69-75`
  `LivePermission`, `:236-254` the non-goal comment, `:284-305` `classifyLivePermission`, `:330-430`
  `ReconcilePermissions`, `:527` `kvGetMultiChunked`, `:576-687` `gatherPermissionInputs`, `:695-706`
  `LoadPermissionReconciliation`, `:708-750` the kernel key set.
- `internal/bootstrap/primordial.go:776-798` — the six kernel grant edges; `RoleOperatorID`.
- `scripts/verify-permission-provenance.go:160` `checkRegistryAnchor`, `:221-288` `main`.
- `Makefile:633-635`; `.github/workflows/ci.yml:312-326`.

**3. Precedents to mirror:** the vertex plane, at every point — `build.go:379-380` for the stamp,
`classifyLivePermission` for the classes, `kernelPermissionKeys` for the kernel set,
`gatherPermissionInputs` for the enumeration, `checkRegistryAnchor` for the anchor. Nothing here is
greenfield.

**4. Increment order + green checks:** §5, in order; then `go build ./...`, `make vet`,
`golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`, `go run ./scripts/lint-board.go`,
`make verify-kernel`, full `go test ./...` with `POSTGRES_TEST_DSN` set.

**5. In-scope gotchas.** `packages/` content edits must bump the manifest version AND the mirroring
`Version` constant (CLAUDE.md) — Inc 1 is exactly such an edit. The Postgres-gated tests skip silently
without `POSTGRES_TEST_DSN` (`agents/steward/REMOTE.md` §3) — a green suite without it is false.
Build-tagged harnesses do not compile under `go test ./...`; `internal/pkgmgr`'s interfaces are reached by
`make test-control-plane-authz`. Dossier entries carried in from `docs/components/pkgmgr.md`, verbatim in
force for this fire: *a check whose declared side is read from the same store as the writer is an echo, not
a check* (→ Inc 3's registry anchor is the answer, and the Core-KV pass must not be sold as more than it
is); *one fact computed twice, owned by nobody* (→ the kernel grant-link key set must derive from
bootstrap's globals, never a second literal); *a guard protecting what a consumer reads must enumerate all
conditions* (→ every live edge lands in exactly one class); *a security-plane skip guard keying on
tombstone-state alone* (→ the `isDeleted` filter is not the classification). From the standing checklist:
**#4 removal needs a transport and an observer** — `RevokePermission` tombstones, so the reconciler must
filter `isDeleted` and the revive arm must re-stamp; **#6 precedent may carry debt** — verify the vertex
plane's own residuals (its doc comment names three) before copying its posture wholesale.

**6. Adjacent finds, and what this run did with each.**
- *The four-writer census (G1) and the declaredKeys shape (G3)* were re-run live and matched.
- *The version gate's directory-level trigger.* `scripts/lint-package-version.go`'s `walkGeneratorDir`
  treats any non-comment `.go` change under `internal/pkgmgr/` as a possible change to the read-grant
  producer compiler, so Inc 1 made `packages/edge-manifest` (the one package declaring `ReadGrantDomains`)
  need a version bump. The gate's coarseness is deliberate — its own doc comment says the whole directory
  is the trigger so that splitting the generator across files cannot reopen the gap — and the repo's
  standing convention is to bump (`dbe783f`, `b9121e8`). **Fixed this run** (`e2b20ba`), not filed: left
  alone it would have reddened `main`.
- *Committing from a tree a builder is still mutating.* A staged commit captured a revert-proof mutation
  (a constant-false conjunct disabling the double-diagnosis guard) that was live in the tree for the
  seconds between the builder planting it and restoring it. **Fixed this run** (`936b4fd`). The lesson is
  a lead-side one and belongs in the dossier: a green bar is not a safe commit point while a builder is
  running revert-proofs — only the builder's *report* is.

**7. Non-goals.** Enforcement on edge origin (§3 — needs relationship-property projection, a designer
pass if and when a driver appears); `holdsRole` (§3 — no declared side); any `docs/contracts/*` edit (§4);
the vertex plane's own three stated residuals.
