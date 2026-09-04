# The write gate reads the entity as STORED — a bare tombstone is governed by the class it removes, and the kernel's own topology links are protected

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — build-ready.** · Designer fire 2026-09-03 ·
Winston. Andrew's one direction at review (2026-09-03): *no contract changes — this fixes a basic code issue the contracts
already promise against; contracts do not spell out implementation details.* The draft's contract surface (§7) is withdrawn.
**Lane:** Lattice.
**Board row:** *[Processor] A documentless tombstone skips every DDL-driven write gate, and no link is ever protected* (★★★ / M).
**Filed by:** the cold review of `TombstoneSupersededLeaseServiceInstance` (commit `d7020963`, 2026-09-03), recorded in
[bgcheck-runaway-and-broad-filter-design.md](bgcheck-runaway-and-broad-filter-design.md) §6 — quoted verbatim in §0.
**Contracts:** none change. The design builds to Contract #1 §1.5/§1.6 (`permittedCommands` governs a write), Contract #8 §8.4
(*"an operation cannot disable auth"*) and Contract #3 §3.3 (a tombstone carries no document) — promises the runtime was
failing to keep. Which document's `class` the gate reads, and which keys the commit-time guard holds, are mechanism (§7).
**Adversarial pass:** §11 — run 2026-09-03, two independent passes (mechanism; census + contracts), 24 findings, every body
section below rewritten in place where a finding landed.

---

## For Andrew

**What this is, in three lines.** Step 6 decides which DDL governs a mutation by reading the `class` field of the *mutation's
own document*. A `tombstone` carries no document, so it resolves no DDL and skips `permittedCommands` — any script of any
package can tombstone any root, aspect or link whose root is not `data.protected`. The proposal is one rule: **for an `update`
or `tombstone`, the governing class is the class of the document as stored** (the entity being rewritten or removed), read from
the prior-document pass step 8 already performs, moved ahead of step 6. Alongside it, **the twelve links the kernel seeds** —
six `holdsRole → operator`, six `grantedBy → operator` — join the protected set, by exact key, threaded into the Processor the
way `SystemActorKeys` already is.

**Why it is Winston-adjudicated.** No architectural fork and no contract change (§7). The draft carried three contract clauses
describing the new refusals; Andrew struck them at review — the contracts already promise the outcomes, the clauses were
narrating how the runtime would keep them.

**Three things want your attention.**

**(i) The row's ★★★ is honest for one half and generous for the other — I filed it, so I owe you the split.** The
*kernel-link* half is a one-operation brick with no heal path: `RevokeRole` on the primordial admin's `holdsRole` (or any
service actor's) is a sanctioned op, no guard covers links, kernel reconcile deliberately never restores a tombstoned link
(`internal/bootstrap/reconcile.go:118-123`), and Contract #8 §8.4 promises in so many words that *"an operation cannot disable
auth"*. That is ★★★ by the contract's own promise, and it is reachable by accident from a raw op submit. The *write-scope*
half is a correctness gap in FR57 under the **current** threat model, where a package author is trusted at install (your
2026-08-21/22 rulings on the parked authority-minting and shelved admission designs): the census (§1.7) found **three shipped
sites the gate would refuse today** — two `wellness-domain` DDLs and one `cafe-domain` DDL whose `permittedCommands` were never
extended when a second op started tombstoning their aspects — which is exactly the drift `permittedCommands` exists to catch and
the bypass has been hiding. Real, cheap, but ★★ on its own. I kept them in one design because they share the mechanism
(the prior-document pass) and the fix is smaller together than apart (§5 row A).

**(ii) The kernel-link arm admits exactly one mutation: a revive.** A kernel link that has *already* been revoked (a
deployment that suffered the brick before this lands) has one heal path today — `AssignRole`'s `revive_link`, an `update` that
flips `isDeleted` back to false, submitted under any still-operator service actor (`packages/rbac-domain/ddls.go:159-188`).
Reseed never restores it: `reconcile.go:113-131` refuses to rewrite a soft tombstone and `create` conflicts on a tombstoned key
(Contract #3 §3.3). A blanket "kernel links are immutable" would make every existing brick permanent, so the arm refuses a
`tombstone` and any `update` that soft-deletes or reshapes the link, and admits an update whose written body is the seeded
shape (§2.3). Contract #8's "cannot disable auth" is kept; the auth plane stays healable.

**(iii) The link-protection set is epoch-aware by construction, and it has to be — with one operational precondition.**
`lattice bootstrap retire` (the stranded-epoch design's Fire 2) revokes a *stranded* epoch's `holdsRole`/`grantedBy` edges
through ordinary `RevokeRole`/`RevokePermission` — their sources are protected identities and permissions of the old epoch, so
any rule of the shape "a link is protected if its endpoint is protected" (§5 rows C, D, E) refuses the verb you ratified on
2026-08-25. The proposed set is the exact twelve link keys the *running Processor's* `lattice.bootstrap.json` names; a stranded
epoch's keys are not in it. The precondition: every Processor replica must have restarted on the current table before `retire`
runs, or the old replica refuses the old epoch's edges as its own. §2.3 has `retire` check that and name the skew in its
diagnostic. Zero reads, and the comment in `reconcile.go:119-120` that calls the primordial links "outside the guard by design"
becomes false and is rewritten.

**One thing that shrank under review.** The draft claimed the committer honoured a supplied tombstone document against
Contract #3 §3.3 and offered you a refuse-vs-ignore choice. The parser already refuses one (`starlark_runner.go:360-365`,
shipped by the tombstone-body-preservation design you ratified 2026-07-22); the overlay branch and the write-once arm built for
it are unreachable. Their removal stays in Inc 2 as dead-code removal (§2.2), with no contract question attached.

---

## 0. The filed row, clause by clause

The row was filed by me from a cold review, not by Andrew; still, its filing text is the demand, so it is quoted from the
commit that filed it (`d7020963`) and answered clause by clause:

> `internal/processor/step6_validate.go:247-260` derives a mutation's class only from its own document, so a documentless
> `{"op":"tombstone","key"}` skips the whole DDL-driven block (permittedCommands, abstract-class, sensitive custody);

- **permittedCommands** — the gap. Closed by §2.1.
- **abstract-class, sensitive custody** — *not* gaps. Both exemptions on `tombstone` are deliberate and correct: a tombstone
  cannot create an abstract-typed instance, and it writes no sensitive `data` to custody (`step6_validate.go:207-245`, and
  Contract #1 §1.7's abstract paragraph says so in the contract's own voice). The design keeps them (§1.1).

> in step 8, `protectedRootKey` (`step8_commit.go:1375-1385`) returns "" for any link key and `rejectPermissionRoleRewrites`
> (`:1349-1353`, `:1397-1400`) skips bare tombstones.

- **`protectedRootKey` and links** — closed for the links that matter to the anti-brick invariant, by exact set (§2.3).
  Business links stay governed by the write-scope rule, not by protection (§2.4).
- **the write-once guard skipping bare tombstones** — correct as stated and stays: a bare tombstone overlays nothing, so there
  is nothing to hold write-once. The *other* arm (a tombstone that carries a document) guards a shape the parser already
  refuses, so it and the overlay it guards against go as dead code (§1.3, §2.2).

> A package script can therefore tombstone any root not marked `data.protected`, any aspect of it, and any link of any package —
> and the corpus relies on that (identity-domain, clinic-domain, rbac-domain, orchestration-base, privacy-base, service-location
> all emit bare tombstones).

- The corpus relies on **bare tombstones**, not on the **bypass**: §1.7's census shows 63 of 66 emissions target a class whose
  DDL admits the op or has no DDL (permissive by Contract #1 §1.6). The three that rely on the bypass are declaration drift,
  fixed in Increment 1.

> This op's two link tombstones depend on it: no `instanceOf` / `providedTo` link DDL exists to resolve against, so a tightening
> must derive class from the prior document and name a link-type authority.

- **derive class from the prior document** — §2.1, exactly.
- **name a link-type authority** — the authority for a link class is its `meta.ddl.linkType` DDL, resolved by the stored
  `class` (every link create in the corpus writes the relation name as `class` — census §1.7 table 3), under the same
  permissive default as vertices and aspects. `instanceOf`/`providedTo` have no DDL and stay permissive; the op's two link
  tombstones pass. Whether `holdsRole`/`grantedBy` should get a *closed* link DDL is answered in §2.4: no — it is the tombstone
  half of the parked authority-minting design and rides its revive.

## 1. Grounding ledger

### 1.1 The write path today: the gate reads the class the script proposes

`validateOne` (`internal/processor/step6_validate.go:183-324`) runs, per mutation: op enum → key shape → abstract-segment gate
(tombstone-exempt) → reserved-name gate (tombstone-exempt) → **class from `m.Document["class"]`** (`:247-253`) → if a governing
DDL resolves (`resolveGoverningDDL`, `step6_resolve_ddl.go:252-300`: exact class lookup, else the bounded `instanceOf` chain to a
vertexType authority): abstract-class refusal (tombstone-exempt), **`permittedCommands`** (`:283-293`), sensitive custody
(aspects only). A `tombstone` has no document, so `class == ""` and the whole `if class != ""` block is skipped.

The `HydratedState` step 6 receives (`script_context.go:33-73`) carries `Hydrated map[string]VertexDoc` — the declared reads,
each with `Class` — and nothing else about the stored world. Step 6 has no view of the document a tombstone removes unless the
script declared it, which Contract #3 §3.3 assumes (*"at its hydrated revision"*) but nothing enforces.

### 1.2 Step 8 already reads every stored document an update or tombstone touches

`readPriorDocuments` (`step8_commit.go:641-708`) reads, for every `update`/`tombstone`, the mutation key **and** its
3-segment root, in one bounded concurrent pass (`priorReadConcurrency = 16`, bounded by `c.Timeout`), keyed by KV key, one
`KVGet` per distinct key. Three sibling guards consume the same pass — `rejectProtectedMutations` (`:728-742`),
`rejectPermissionRoleRewrites` (`:824-920`), `rejectPackageScopeViolations` (`:1050-1108`) — and `buildMutationValue` preserves
the body from it. A read *error* fails the commit (`firstErr`, `:683-690`) — retryable, never permissive. **The stored class of
every update/tombstone target is therefore already read, one step too late for the gate that needs it.**

`protectedRootKey` (`:1379-1385`) maps an aspect to its root and returns `""` for a link: *"links, which are not vertex-rooted
and are not kernel-protected entities"*. `rejectPackageScopeViolations` refuses any `holdsRole` mutation — but only after
`if lifecycle == "" { return nil }` (`:1061-1064`), so only for the three package-lifecycle ops.

### 1.3 The tombstone-document branch is dead code

Contract #3 §3.3 (line 47): *"A tombstone mutation carries no document; one supplied is not honored. A tombstone can never
modify, blank, or reclaim the stored body."* The runtime honours it at the parser: a Starlark tombstone that carries a
`document` is refused `InvalidReturnShape` before any step runs (`starlark_runner.go:360-365`), shipped by
[tombstone-body-preservation-design.md](tombstone-body-preservation-design.md) Fire 2 (Andrew-ratified 2026-07-22; its
alternative B, "honour the supplied document", was rejected there). `MutationOp` is constructed at three production sites
(`starlark_runner.go:348`, `step65_encrypt.go:278` — an update, `autocomplete.go:86` — an update), so `m.Op == "tombstone" &&
m.Document != nil` is unreachable. Two pieces of code exist only for that shape: the overlay branch in `buildMutationValue`
(`:522-530`) and the write-once guard's tombstone-with-document arm (`:818-822`, `:828-830`, doc comment *"a tombstone that DOES
carry a document overlays it exactly as an update does"*). Both are removed as dead code in Inc 2 (§2.2). The draft of this
design read them as a live drift and offered a refuse-vs-ignore fork; the adversarial pass (§11 C1) retired it. There is no
"chosen class" vector through a tombstone; the stored-class rule in §2.1 rests on the bare-tombstone gap alone.

### 1.4 The generalisation probe: the same mechanism reaches updates

The mechanism is "the gate keys on the class the submitter proposes". The tombstone is the instance where the proposal is
empty. Two siblings:

- **An `update` may change `class`** — Contract #1 §1.3's field table marks `class` *mutable* (line 73). An update writes the
  whole value from the script's document (`buildMutationValue`, `:544-549`), and step 6 resolves the DDL of the **new**
  class. An update that rewrites a `patient` as class `foo` is gated by `foo`'s (absent) DDL, never by `patient`'s
  `permittedCommands`. And `mutationTombstoned` (`step6_resolve_ddl.go:553-562`) already treats an update carrying
  `isDeleted: true` as a tombstone — the corpus has such soft-deletes (`make_vtx_tombstone` / `make_link_tombstone` /
  `stale_indexes_tombstone`, census §1.7), eight restating the stored class, so they are gated correctly today *by convention*,
  not by rule. The ninth is the exception that proves the rule: `MergeIdentity`'s link-rekey loop
  (`identity-hygiene/ddls.go:679-686`) copies the *stored* class into the soft-delete and writes `""` when the stored link has
  none — a declared class of `""` resolves nothing, so that loop is gated today only where the stored body happened to carry a
  class. Under §2.1 the stored class governs regardless.
- **The type authority is resolved against the in-flight batch first** — `instanceOfTargetOf` (`:333-372`) lets a batch
  tombstone of the vertex's `instanceOf` link suppress the committed edge, so a mutation of a subtype vertex bundled with a
  tombstone of its own `instanceOf` resolves *no* authority and falls to the permissive default. For a `create` the batch is
  the only truth; for an `update`/`tombstone` the entity as **stored** has an authority, and that is the one whose
  `permittedCommands` the op must satisfy. Census: exactly one shipped op bundles the two (`TombstoneSupersededLeaseServiceInstance`,
  root + `instanceOf` + `providedTo` in one batch) and its DDL admits it either way.

Both are folded into the rule in §2.1: **the class stored at the key governs; the class the script writes is checked in
addition.** No contract field changes mutability.

### 1.5 The kernel topology, who can revoke it, and who must

Root is topology: the bootstrap capability lens is `MATCH (identity {key: $actorKey})-[:holdsRole]->(role) WHERE
role.canonicalName = 'operator'` (`internal/bootstrap/lenses.go:136`); the wildcard read grant is the same walk (`:359`);
`SystemActorKeys` is discovered from the same links (`system_actors.go:50-84`). Contract #7 §7.7 and Contract #6 §6.1:
*root capability is established by graph topology … `data.protected` retains only its anti-brick meaning.* The kernel seeds
**twelve** links, listed in Contract #7 §7.2 item 8 (`primordial.go:783-802` six `grantedBy`, `:808-859` six `holdsRole`; the
Gateway identity deliberately gets none, `:862-865`; corroborated by `PrimordialVertexKeyCount = 37`'s own comment,
`nanoid.go:733-742`). Every vertex on both ends carries `data.protected: true`; **no link carries anything**
(`MakeLinkEnvelope(..., map[string]any{})`). The six `grantedBy` keys already have an owner: `bootstrap.KernelGrantLinkKeys()`
(`nanoid.go:647-671`, pinned byte-equal to the seeder by `kernel_grant_links_test.go`, consumed by `pkgmgr/permissionreconcile.go`
and `scripts/verify-permission-provenance.go`), assembled by concatenation because `substrate.LinkKey` panics on an unloaded id.

Who can tombstone one today: any script (§1.1); and one sanctioned op — `RevokeRole` (`packages/rbac-domain/ddls.go:399-408`)
tombstones `lnk.<actorType>.<actorId>.holdsRole.role.<roleId>` for any `actorKey`/`roleKey` with no protected check, granted to
`operator` (`permissions.go:41`). Nothing heals it: `planReconcile` never rewrites a soft-tombstoned kernel key *"— the
primordial links are outside the Processor's protected-key guard by design … Rewriting one would silently restore a revoked
grant"* (`reconcile.go:118-123`). A revoked admin means the `lattice` CLI (which submits as the bootstrap identity) is dead;
recovery is a hand-crafted op under a service actor's NKey. Reachability: Loupe exposes no `RevokeRole` (grep, §6.5); the path
is a raw op submit — an accident a legitimate operator can have once, and the contract says it must not be possible.

Who **must** be able to revoke one: `lattice bootstrap retire` (`cmd/lattice/bootstrap/retire.go:50-55`) tombstones *"every
live holdsRole/grantedBy edge into"* a **stranded** operator role via `RevokeRole`/`RevokePermission` — the stranded epoch's
sources are `data.protected` identities and permissions. The stranded-epoch design's Fire 2 relied on the very gap this row
files: *"`protectedRootKey` returns "" for any key not starting `vtx.`, so no guard exemption is needed"*
([primordial-epoch-stranded-authority-design.md](primordial-epoch-stranded-authority-design.md) §7 item 1). Any protection rule
must let that verb through — which rules out every endpoint-derived rule (§5 rows C–E) and selects the exact current-epoch set.

`cmd/processor/main.go:71-72` loads `lattice.bootstrap.json` (`bootstrap.Load`), so `RoleOperatorID`, the six root-equivalent
identity IDs and the six kernel permission IDs are in the Processor's process; `AuthWiring` (`commit_path.go:1171-1199`) is the
seam that carries bootstrap-derived facts into the pipeline (`PrimordialActors`: *"bootstrap-derived and fixed"*, `main.go:158-161`).
(The `PrivacyActorKey` comment at `system_actors.go:90-93` saying cmd/processor *"deliberately do[es] not load"* the json is
stale — `main.go:72` does; the builder corrects it in passing.)

### 1.6 Threat and precondition, priced against the actual deployment

- **A package script tombstoning another package's entity.** Under the standing rulings (authority-minting parked 2026-08-21;
  admission model shelved 2026-08-22) a package author is trusted at install until `consoleOperator` is delegated below root,
  and AI authoring is dormant (`BRIDGE_CAPABILITY_AUTHOR=real`). So the *hostile* form gates nothing today. The *accidental*
  form is live: three shipped scripts tombstone aspects their DDL does not admit them to touch (§1.7), unnoticed because the
  refusal never fires. That is FR57's promise (Contract #1 §1.6) silently half-kept — the same op would be refused if it
  *updated* the aspect.
- **Revoking a kernel link.** Sanctioned op, root-held, one submit, permanent, contract-promised impossible. Not a hostile
  precondition — a legitimate operator's mistake.
- **`holdsRole` creation** (the escalation direction) is *already* permissive for creates — no `holdsRole` DDL exists, and the
  only refusal is lifecycle-scoped — which is the parked authority-minting design's R0/R1 territory. This design does not close
  the create side and therefore does not close the tombstone side of `holdsRole`/`grantedBy` by DDL either (§2.4): closing one
  direction of the auth-plane link classes while the other stays open would be dead scaffolding with a false sense of coverage.

### 1.7 The census (re-runnable in §6)

Run 2026-09-03 by a read-only sub-agent over `packages/**/*.go` (non-test) and `internal/bootstrap`; re-derived independently
by the adversarial census pass the same day (§11 C2), which walked **every** emission to its target class's DDL against all
140 non-empty `permittedCommands` lists in the repo. The draft's headline was 48; the command it cited prints 79 and did so at
the filing commit too (75 at `d7020963`). The corrected population is below; the three-refusal conclusion survived the re-walk.

| Population | Count | Detail |
|---|---|---|
| Raw §6.1 grep lines, `packages/` | 79 | 36 `"op": "tombstone"` literals + 43 `make_tombstone(` occurrences, disjoint |
| … of which helper *bodies* (`return {"op": "tombstone", …}` inside a `def make_*tombstone`) | 13 | clinic ×3, rbac, service-location, cafe ×2, location, wellness ×4, lease-signing — a body is not an emission |
| **Bare tombstone emissions** | **66** | 15 files, 13 packages; every helper is bare; six multi-line inline literals opened by hand (identity-hygiene ×2, privacy-base, objects-base, cafe-domain, service-domain) all continue with `expectedRevision` |
| … carrying a `document` | **0**, structurally | the parser refuses one (§1.3); no census can find what cannot be built |
| Document-carrying soft-deletes (`update` + `isDeleted: true`) | 9 sites, 7 files | `clinic-domain/site.go:259`, `semantic-contracts/scripts.go:34`, `loftspace-domain/ownership.go:129`, `identity-domain/ddls.go:650`, `lease-signing/scripts.go:60,81` (helper definitions) and `identity-hygiene/ddls.go:685,716,724` — eight restate the stored class; the identity-hygiene rekey loop copies it and may write `""` (§1.4) |
| Kernel bare tombstones | 3 ops | `TombstoneMetaVertex` cascade (`meta_ddl.go:431-433`), `UninstallPackage` (`install_ddl.go:160`), `UpgradePackage` (`:233`) — targets are `meta.*` / `permission` / `role` / `roleindex` / kernel aspect classes, **none governed by any DDL** (the meta-root DDL's canonicalName is `root`, so `meta.ddl.*` classes resolve nothing; the five kernel aspect DDLs carry no `permittedCommands`, `primordial.go:926-1060`) → permissive before and after |
| Target classes with a DDL whose `permittedCommands` **omits** the emitting op | **3 emissions, 2 packages** | `wellness-domain/ddls.go:4267` `ReleaseOrphanedBooking` → `sessionSeatClaim` (DDL `:1101`: `CreateBooking, CancelBooking`); `:4392` `ReleaseOrphanedBooking` → `sessionBookerClaim` (DDL `:1186`: `CreateBooking, JoinWaitlist, CancelBooking`); `cafe-domain/ddls.go:1389` `SettleStaleTab` → `cafeOpenTabGuard` (DDL `:294`: `OpenTab, Settle`, doc comment *"tombstoned by Settle"*). The sibling cells in the same ops (`sessionWaitlistClaim`, `bookerSlotClaim`, the `openFor` link) were updated when the ops were added; these were missed |
| Target classes with a DDL that admits the op, or an empty list | 30+ | incl. the subtype-via-`instanceOf` pair `service.<x>.instance` / `service.<x>.template` |
| Target classes with **no DDL** (permissive, Contract #1 §1.6) | 20+ | `credentialindex`, `identityindex`, `queuedFor`, `assignedTo`, `holdsRole`, `grantedBy`, `role`, `permission`, `residesIn`/`worksAt`/…, `instanceOf`, `providedTo`, `ledBy`, `atStudio`, `content`, `openFor`, `servedAt`, `containedIn`, caller-named object links |
| `meta.ddl.linkType` DDLs in the repo | **3** | `indexes`, `duplicateOf`, `boundTo` — all `identity-domain`, all `permittedCommands` empty *"intentionally: multi-writer, open posture"* (`ddls.go:480-547`) |
| Link creates whose `class` is the relation name | 5/5 spot-checked, uniform | `make_link(key, source, target, cls, local_name, data)` with `cls == local_name` everywhere |
| Cross-package bare tombstones | 6 | `identity-hygiene` and `privacy-base` into `identity-domain` classes — all empty-list or no-DDL |
| Ops bundling a mutation of a subtype vertex with a tombstone of its `instanceOf` | 1 | `TombstoneSupersededLeaseServiceInstance` (`lease-signing/scripts.go:1532` builds the key, `:1612` tombstones it) — admitted by the authority either way |
| Operation-type names defined by more than one package | 1 | `DebitAccount`: `cafe-ledger/permissions.go:54` and `loftspace-ledger/permissions.go:66`; listed in three packages' `permittedCommands` (§2.1, name-scoped matching) |
| Interface doubles the §3 signature change reaches | **8** | `nfrValidator`, `nfrCommitter`, `StubValidator`, `StubCommitter`, `raceCommitter`, `racyCommitter`, `testutil.FaultyValidator`/`FaultyCommitter`, and `packages/identity-domain/testhelpers_test.go:518` `interposingValidator`; none build-tagged, repo-wide |

**The error bucket, opened.** The classifier's "no-DDL" bucket is not an error bucket: each member was checked against the
`CanonicalName:` corpus and none has a DDL under a different kind. The `service.<x>.*` pair is the one shape that resolves
only through the chain; both authorities admit their op.

## 2. The rule, and the shape

**One sentence.** *A mutation is governed by the DDL of every class it touches: for `update` and `tombstone`, the class of the
document stored at the key (the entity being rewritten or removed); for `create` and `update`, the class the document
declares. The kernel's own topology links are protected exactly as its vertices are.*

### 2.1 Stored-class governance (step 6)

For each mutation, step 6 computes a **stored class** and a **declared class**:

| op | stored class | declared class | checks on stored | checks on declared |
|---|---|---|---|---|
| `create` | — (a create-only key has no stored body; a create on a tombstoned key conflicts at commit) | `document.class` | — | as today: abstract, `permittedCommands`, custody |
| `update` | `prior[key].class` | `document.class` | `permittedCommands` | as today |
| `tombstone` | `prior[key].class` | — | `permittedCommands` | — (abstract/custody stay exempt, §0) |

When stored and declared class are equal (every shipped update), the stored check resolves the same DDL and costs nothing
new. When the stored key is absent (`prior` not found) the stored class is empty and resolves nothing — today's behaviour for a
tombstone of an absent key, which the `TombstoneMetaVertex` cascade relies on (§8).

**Absent and corrupt are different states, and only absent is permissive.** The stored class is read through `prior.lookup`
(`step8_commit.go:609-615`), not `prior.doc`: an entry that exists but did not decode (`Found && Doc == nil` — the state the
write-once guard already fails closed on, `:846-848`) or whose `class` is present and not a JSON string is a
`DDLViolation{permittedCommands}` refusal naming the key, never the permissive default. Nothing today validates that a written
`class` is a string (`validateMutationBooleanFields`, `step6_validate.go:96-149`, covers only the three booleans), so the batch
pre-pass gains `class` (string when present); otherwise one admitted write of `{"class": 7}` would ungovern the entity forever,
including from this gate. A stored class of `""` or an absent `class` field is the absent case: resolves nothing, permissive
(Contract #1 §1.5 *"No default class"*).

**Type authority for a stored class resolves against the committed world.** `resolveGoverningDDL`'s chain walk gains a
`committedOnly` disposition used for the stored-class resolution: `instanceOfTargetOf` skips the in-flight-batch layer (and the
`batchDead` exclusion) and reads working set → memo → on-demand, so a batch tombstone of the entity's own `instanceOf` cannot
un-type it for the gate. The declared-class resolution keeps today's batch-first layering (a create's authority exists only in
the batch). Sharing `DDLResolutionMemo` between the two dispositions is sound: the memo caches only the raw pre-exclusion
`LiveInstanceOfTargets` answer keyed on the walk node, both layers re-run against this call's mutations on every hit, and a
fault is never memoised (`step6_resolve_ddl.go:118-136`, `:322-360`; verified §11 M-T2).

**Neither half of the rule is submitter-exhaustible.** The stored class comes from the prior-document pass (§3), which is off the
script's live-read budget. The chain walk's on-demand reads — for the stored class **and**, on an `update`, for the declared
class when the exact lookup misses — run through the `Checked` variant (`resolveGoverningDDLChecked`, `step6_resolve_ddl.go:271-276`)
against a nil `LiveReads` budget (nil-safe = unlimited; Processor dossier: *"a guard whose subject is computed from
submitter-supplied input is not a guard"*). Today the declared-class walk charges the script's budget and faults **open** on
`errInstanceOfLiveReadBudgetExceeded` (`:36-40`, `:245-248`, `:340-352`); left as is, a script could exhaust its own budget and
then re-type a chain-resolved subtype under no authority — the same switch-off §5 row B was rejected for. A `create` keeps
today's budgeted walk (its authority is in the batch; the exact-miss read is the script's own cost).

**A read fault is retryable; a resolved refusal is terminal.** A KV error on the prior pass or on a chain read is handled the way
a step-8 read error is handled today — `NakWithDelay`, redelivered, never a refusal and never the permissive default. The draft
made a chain-read fault a terminal `DDLViolation` on step 6.5's precedent; that precedent is terminal because the alternative
is committing plaintext, a stake this gate does not share, and a transient blip must not permanently reject a valid op (§11 M5).
`DDLViolation` is reserved for a verdict: the DDL resolved and its `permittedCommands` omits the op, or the stored body is corrupt.

**Refusal surface.** `DDLViolation` with `ViolatedConstraint: "permittedCommands"` and a `Detail` naming the stored class and the
DDL — the existing wire code; a consumer sees the same refusal an `update` gets today.

**The match is by operation-type name, as today.** `permittedCommands` is compared against `env.OperationType` with no package
qualification (`step6_validate.go:286-287`). One name is defined by two packages today (`DebitAccount`, census §1.7), so
`cafe-ledger`'s op satisfies `loftspace-ledger`'s `transaction` DDL and vice versa — a property of the existing gate that this
design extends to tombstones without changing. Package-scoped matching is out of scope; it is recorded as a residual line in the
build note, not a row, until a second collision or a hostile-author threat model makes it a demand.

### 2.2 The tombstone-document branch is removed (step 8, dead code)

The parser already refuses a tombstone that carries a document (§1.3), so `buildMutationValue`'s overlay branch for
`m.Op == "tombstone" && m.Document != nil` and `rejectPermissionRoleRewrites`' `case "tombstone": if m.Document == nil { continue }`
arm are unreachable. Both go: the tombstone path preserves the stored body whole and sets `isDeleted`, the write-once arm becomes
`case "tombstone": continue`, and the doc-comment paragraph about the overlay is deleted. Step 6 does not read a tombstone's
document (the stored class governs). Net: −lines, one fewer path, no behaviour change; the owning test is a unit assertion over a
hand-built `MutationOp` (§9 Inc 2), not a script.

### 2.3 Kernel topology links are protected (step 8)

`AuthWiring` gains `KernelLinkKeys []string`, computed in `cmd/processor` from the loaded bootstrap table by
`bootstrap.KernelTopologyLinkKeys()` — a two-line composition, not a new derivation: `KernelGrantLinkKeys()` (the six `grantedBy`
keys, already owned and pinned, §1.5) plus the six `*HoldsRoleLinkKey` constants (`Bootstrap`, `Loom`, `Weaver`, `Bridge`,
`Objmgr`, `Privacy`). The `RoleOperatorID == ""` check comes strictly first and returns `ErrPrimordialIDsUnloaded`, like
`SystemActorKeys`; the keys are assembled by concatenation, never `substrate.LinkKey`, for the reason `KernelGrantLinkKeys`' doc
comment gives (it panics on an unloaded id, and readiness paths must report on an unloaded process rather than crash it). The
existing `TestKernelGrantLinkKeys_MatchesWhatTheSeederEmits` pins the grant half; a new pin covers the `holdsRole` half
byte-equal against `PrimordialEntries`.

`rejectProtectedMutations` takes the set. For a key in it:

- a `tombstone` is refused;
- an `update` is refused unless its written document is the **seeded shape**: `isDeleted` false, `sourceVertex`,
  `targetVertex`, `class` and `localName` equal to the key's own segments (`ParseLinkKey`; `class == localName == relation`,
  as every kernel link is seeded). A soft-delete (`isDeleted: true`) or a re-pointed endpoint is refused; a revive — the
  `AssignRole` `revive_link` update that restores a revoked link, `packages/rbac-domain/ddls.go:159-188` — is admitted. This is
  the one heal path an already-bricked deployment has (reseed refuses to rewrite a soft tombstone, `reconcile.go:113-131`;
  `create` conflicts on a tombstoned key), and a blanket immutability rule would have removed it (§11 M2 / C3). The `data`
  stamp the revive carries is not compared: the grant-edge-provenance rules already govern it, and a kernel link's `data` is `{}`.

The refusal is the existing `*ProtectedKeyError`; `Root` for a link is parsed from the key's first three segments (the source
vertex — the actor or the permission — so the reply names it), since `protectedRootKey` returns `""` for anything not
vertex-rooted and stays that way. The `opwire` doc comment on `ErrCodeProtectedKey` (*"whose root document carries
`data.protected == true`"*, `opwire.go:172-177`) is rewritten to cover the seeded links; the message text stays accurate because
both endpoints are themselves `data.protected`. Vertex-rooted keys are unchanged. No read is added — `readPriorDocuments` still
reads the link's own document for body preservation, as today.

What this refuses and what it does not:

| Mutation | Today | After |
|---|---|---|
| `RevokeRole` on the primordial admin / a service actor's `holdsRole → operator` | commits; kernel bricked or engine dead | `ProtectedKey` |
| `RevokePermission` on a kernel meta-/install-permission's `grantedBy` | commits; operator loses the kernel op | `ProtectedKey` |
| Any script tombstoning or soft-deleting one of the twelve (incl. `MergeIdentity`'s link loop, were a kernel identity ever the secondary — its root is already `ProtectedKey`) | commits | `ProtectedKey` |
| `RevokeRole` on an ordinary identity granted `operator` via `AssignRole` | commits | commits (not seeded) |
| `RevokeRole` on the Gateway's `identityProvisioner` | commits | commits (not seeded; Contract #7 §7.2 item 8 gives the Gateway no seeded link) |
| `lattice bootstrap retire` revoking a **stranded** epoch's edges | commits | commits (a different `RoleOperatorID`; not in the running set — precondition below) |
| `AssignRole` reviving a revoked kernel link | revive is an `update`; commits | commits — the seeded-shape update is the admitted heal path |
| `AssignRole` re-creating a kernel link | `RevisionConflict` (key exists tombstoned) | unchanged |
| `UpgradePackage`/`UninstallPackage` touching `holdsRole` | `PackageScope` | unchanged, and now also `ProtectedKey` for the twelve |

**Epoch precondition.** The set is a function of the **Processor's** loaded table (process lifetime, §4); `retire` loads whatever
json the operator points at (`retire.go:84-86`) and verifies it only against Core KV (`CurrentEpochOperatorReachable`,
`:110-127`), never against the running Processor. If the json has been regenerated but a Processor replica has not restarted,
that replica's set names the **old** epoch — the edges `retire` is revoking — and refuses them, non-deterministically per op in
a mixed roll. So: (a) the precondition "every Processor replica has restarted on the current table" is stated in `retire`'s
help text and in `docs/components/processor.md`; (b) `retire` recognises a `ProtectedKey` reply on a revocation and reports it
as *epoch skew: a Processor still holds the retired table* rather than a generic failure. A positive pre-check (a Processor
health field naming its loaded `RoleOperatorID`) is a Health-KV self-report the Processor may add later; it is not in this
design's increments because the `ProtectedKey` diagnosis already makes the skew loud and the operator action (roll the
Processor) obvious.

### 2.4 What stays permissive, deliberately

- A link class with no `meta.ddl.linkType` DDL (`holdsRole`, `grantedBy`, `instanceOf`, `providedTo`, `assignedTo`, …) is
  governed by no `permittedCommands` — Contract #1 §1.6's permissive model, unchanged. A package that wants a closed write set
  on a link class registers the DDL; the three that exist chose an open posture on purpose.
- `holdsRole`/`grantedBy` get **no** closed DDL in this design (§1.6): eight ops across seven packages create `holdsRole` links
  (`clinic-domain/ddls.go:1786`, `identity-domain/ddls.go:1392,1657`, `orchestration-base/ddls.go:452`, `wellness-domain/ddls.go:4687`,
  `service-domain/ddls.go:841`, `identity-hygiene` merge repoints — which also soft-delete the secondary's `holdsRole` links,
  `ddls.go:679-686` — `rbac-domain` itself), the create side is the parked
  authority-minting design's R0/R1, and its revive trigger (`consoleOperator` delegated below root) is the moment both
  directions close together. Recorded there as a line in its §13 residuals by the build (a doc pointer, not a new row).
- `role` / `permission` / `roleindex` roots have no DDL; the step-8 write-once guard is their gate. Unchanged.

## 3. Mechanism — the prior-document pass moves ahead of step 6

Today: step 4 hydrate → 5 execute → **6 validate** → 6.5 encrypt → 7 events → **8 commit (reads priors, three guards, batch)**,
inside the OCC retry loop (`commit_path.go:323-520`).

After: step 4 → 5 → **5.5 shape pre-pass, then read priors** → 6 validate(prior) → 6.5 → 7 → 8 commit(prior, topped up).
Concretely:

- **The key-shape gate runs before any read.** Step 6 is the only mutation-key gate (the Starlark runner builds `MutationOp`
  with no shape check, `starlark_runner.go:348`), and `readPriorDocuments` does none of its own; a key with a space, `*`, `>`
  or `@` reaches `KVGet` and the pinned nats.go (v1.52.0, `jetstream/kv.go:502,924`) returns `ErrInvalidKey` — an error, which
  the pass would treat as retryable, turning today's terminal `keyPattern` refusal into an unbounded redelivery loop (§11 M3).
  So step 5.5 opens with the batch-wide shape pre-pass step 6 already has the shape of (`validateMutationBooleanFields`): every
  mutation key through `ClassifyKey`, a failure refused `DDLViolation{keyPattern}` exactly as step 6 refuses it today, before
  the first read. Step 6 keeps its per-mutation shape check (idempotent on a batch the pre-pass admitted).
- `readPriorDocuments` becomes a `Committer` method exposed on the interface — `ReadPrior(ctx, mutations) (PriorDocs, error)` —
  and `Commit` takes the `PriorDocs` it produced (`Commit(ctx, env, result, tracker, prior)`). The type is exported as
  `PriorDocs` (today's unexported `priorDocs`) because the pipeline and the validator now hold it.
- **`Commit` tops up; the hoist is a memoisation, not a relocation.** The mutation set `Commit` receives is not always the set
  step 6 validated: the task auto-complete appends an `update` of the task root *after* validation
  (`commitWithTaskAutoComplete`, `commit_path.go:443`, `autocomplete.go:86-124`), and on a batch conflict calls `Commit` up to
  three times with re-derived injections (`:909-948`). A `prior` read at 5.5 has no entry for that key, and every step-8
  consumer of the map degrades silently on a missing entry: `preserveImmutableFields` would re-stamp the task's creation
  provenance from the current op, and the three guards — including this design's link arm — would see "not found" (§11 M1).
  So `Commit` reads, with the same pass, every `update`/`tombstone` key absent from the map it was handed and merges the
  result. On the validated path that is zero reads (every key is present); on the injected path it is the one read the
  injection costs today. "Exactly as many `KVGet`s as today" holds by construction.
- `Validator.Validate` gains the `prior PriorDocs` parameter. Doubles that must change (census §6.4): **eight** — `nfrValidator` /
  `nfrCommitter` (`nfr_r1_test.go:565,577`), `StubValidator` / `StubCommitter` (`step_interfaces.go:86,110` — the stub pipeline
  the meta-install harness wires, `commit_path.go:1156-1164`), `raceCommitter` (`optional_reads_test.go:204`), `racyCommitter`
  (`integration_test.go:445`), `testutil.FaultyValidator` / `FaultyCommitter` (`internal/testutil/faultinjector.go:159,177` —
  `testutil` is a non-test package, so this is a production-API change), and `packages/identity-domain/testhelpers_test.go:518`
  `interposingValidator`. No build-tagged file anywhere in the repo implements or calls either interface (§6.4).
- The read moment moves ~one step earlier than today; the argument in `readPriorDocuments`' doc comment (*"a commit that
  succeeds proves no write landed in between"*) is unchanged: every update/tombstone with an `ExpectedRevision` or a found
  prior is revision-conditioned (`conditionRevision`, `step8_commit.go:1394-1402`), so a concurrent write in the widened window
  conflicts. The one unconditioned shape — an update/tombstone of a key **absent** at read time — is unchanged in kind and
  wider in window (§4).
- A `ReadPrior` error is handled where a step-8 read error is handled today: retryable (`NakWithDelay`), never a refusal, never
  permissive.
- On an OCC retry the loop re-hydrates, re-executes, re-reads priors, re-validates: the same order as today with the read
  hoisted. Nothing is carried across attempts. The `derive_reads` pre-pass runs at step 4, before any mutation exists; no interplay.
- `rejectProtectedMutations(mutations, prior, kernelLinks)` — the set is a `map[string]struct{}` built once at `NewCommitter`
  from `AuthWiring.KernelLinkKeys`.

**Cost.** Zero new reads on the hot path. The stored-class chain walk adds on-demand reads only for a subtype vertex whose
`instanceOf` is neither declared nor memoised — one `KVGetMulti` per such vertex per execution, the same cost the declared-class
walk pays today for the same vertex, and memoised on the same `DDLResolutionMemo`. One cost shifts rather than grows: an op
refused at step 6 for a reason other than key shape now pays the prior reads before its refusal. NFR-S6 is a wire-*shape*
collapse (`nfr_s6_wire_shape.go:32-58`), not a timing equalisation, and its own refusals are raised at step 5, ahead of 5.5.

## 4. State and lifetime

| State | Created | Reset | Carried | Ordered | Notes |
|---|---|---|---|---|---|
| `PriorDocs` (per attempt) | step 5.5, per pipeline attempt; topped up by `Commit` for injected keys | never mutated by a reader; dropped at attempt end | **not** across OCC retries, redeliveries, or the `derive_reads` pre-pass | read at the step-4 snapshot's successor moment; a commit that succeeds proves no intervening write for every conditioned key. The sole unconditioned shape — an update/tombstone of a key absent at read time — now spans steps 6, 6.5 (Vault round trips) and 7 instead of the prior pass alone; accepted, it is the same race as today and the cascade tombstones that produce it have no body to protect | today's `priorDocs`, one step earlier |
| `KernelLinkKeys` set | `cmd/processor` start, after `bootstrap.Load` | process restart | process lifetime, like `SystemActorKeys` | none | pure function of `lattice.bootstrap.json`; a regenerated json (new epoch) yields a new set on restart, which is what un-protects a stranded epoch's links |
| `DDLResolutionMemo` | unchanged | unchanged | unchanged | unchanged | now also warmed by the stored-class walk |

**Unset `KernelLinkKeys` (tests, `MakeStubPipeline`) = nothing protected = today's behaviour.** Empty cannot fail closed here
("every link protected" bricks every link write), so the production wiring is pinned instead: a `cmd/processor` test asserts the
wired set equals `bootstrap.KernelTopologyLinkKeys()` and is non-empty after `Load` — the same shape as the `SystemActorKeys`
discovery pin. Recorded in `AuthWiring`'s doc comment beside `PrimordialActors`' *"unset is fail-closed"* note, stating the
opposite posture and why.

## 5. Alternatives

| # | Option | Verdict |
|---|---|---|
| 1 | **Do not have this thing** — accept that a bare tombstone resolves no DDL and that links are unprotected; amend Contract #1 §1.5 to say so and Contract #8 §8.4 to exclude links from *"an operation cannot disable auth"* | Rejected. The second half withdraws a promise the kernel's anti-brick posture is built on, for a one-op self-brick with no heal. The first half alone *would* be a defensible "document it" — FR57 becomes a create/update scope — but the three drifted DDLs show the gate's silence is already costing correctness, and the fix rides a read that is already paid for |
| A | **Step-8 sibling guard** (`rejectUngovernedRewrites` over the same prior pass, its own `ddlResolver`, no interface change) — the precedent the filing points at | Rejected, narrowly. Same reads, same refusal; but it evaluates `permittedCommands` in a second place with a second resolver, and the contract locates write-scope at step 6. Andrew's doctrine: a new mechanism patching the previous one's gap is the signal to re-derive the base — the base is step 6's class derivation, and §2.1 fixes it in place. Interface change is the price (eight doubles, no build-tagged ones) |
| B | **Step-6 on-demand reads** (`classOf(key)` for every update/tombstone, no interface change) | Rejected. `classOf`'s on-demand read is charged to the script's live-read budget and faults open to the permissive default; a script exhausts its own budget then emits bare tombstones — a gate the submitter can switch off. Fixing that means a second, budget-free read path: strictly worse than moving the one that exists |
| C | **Protect a link whose source root is protected** (`protectedRootKey(link) = source root`; mirrors PackageScope rule 4's ownership asymmetry) | Rejected. Refuses `lattice bootstrap retire` (a stranded epoch's identities are protected), makes the Gateway's `identityProvisioner` grant irrevocable, and any package role assigned to a kernel identity |
| D | **Protect a link whose both ends are protected** | Rejected. Still refuses `retire` (both ends of a stranded edge are protected). Two reads instead of one where C needed one |
| E | **`data.protected: true` on the seeded link documents**, guard reads the link's own prior body (already read) | Rejected. Existing deployments lack the marker and reconcile never rewrites links; a stranded epoch's links keep the marker forever, so `retire` is refused; and the marker cannot be removed once set. The exact-set rule is epoch-aware for free |
| F | **Closed `holdsRole`/`grantedBy` link DDLs in rbac-domain** (`permittedCommands` = the RBAC ops + the eight creators + the three lifecycle ops) | Not this design (§2.4). Closes the denial direction while the escalation direction (create) stays open by the parked authority-minting ruling; and it is a cross-package `permittedCommands` list eight packages must keep current. Rides that design's revive |
| G | **Refuse a tombstone that carries a document** instead of ignoring it | Already the runtime: the parser refuses it (`starlark_runner.go:360-365`), ratified 2026-07-22 by the tombstone-body-preservation design. Not a choice this design can offer; §2.2 removes the unreachable overlay |
| K | **Kernel links immutable in both directions** (refuse every `update`, revival by reseed) — the draft's §2.3 | Rejected. Reseed never rewrites a soft tombstone and `create` conflicts on one, so an already-revoked kernel link would have no heal path at all; the seeded-shape revive exemption (§2.3) keeps "cannot disable auth" and keeps the plane healable |
| L | **Kernel links protected by an epoch-independent rule** (any `holdsRole → <role with canonicalName operator>`) | Rejected. Reads the target role's body per link mutation (one read the exact set does not pay), and still refuses `retire` — a stranded epoch's operator role carries the same canonical name. The exact set plus the stated precondition is the smaller shape |
| H | **Refuse a tombstone of an absent key** (make §3.3's *"assert the key existed"* true) | Rejected here. The `TombstoneMetaVertex` cascade tombstones aspect suffixes a lens may never have had, on purpose (`meta_ddl.go:409-425`), and Contract #1 §1.7 documents the materialised entry. A separate row if ever wanted; not a security gap (an absent key has no body to rewrite) |
| I | **Make `class` immutable on update** | Rejected. Contract #1 §1.3 says mutable; nothing in the corpus needs a change, but a re-typing under both DDLs' `permittedCommands` (§2.1) is the weaker, sufficient rule and touches no field semantics |
| J | **Resolve the stored class from `state.Context.Hydrated` only** (no read; a tombstone of an undeclared key resolves nothing) | Rejected. Contract #3 §3.3 assumes declaration but nothing enforces it, and the undeclared case is exactly the one a careless or hostile script produces — a gate that only sees declared targets is switched off by not declaring |

Priced in combination: A+E and B+E each need two new mechanisms for what §2 does with one moved read and one threaded set.

## 6. Executable censuses

### 6.1 Bare tombstone emitters — expect 79 raw lines, 13 helper bodies, 66 emissions

```
grep -rn '"op":\s*"tombstone"\|make_tombstone(' packages --include='*.go' | grep -v _test.go | grep -vE ':\s*def ' | wc -l   # 79 raw
grep -rn -A1 'def make_[a-z_]*tombstone' packages --include='*.go' | grep -c '"op": "tombstone"'                          # 13 helper bodies
```
Emissions = raw − bodies = **66**. There is no command for "carrying a document": the parser refuses the shape
(`starlark_runner.go:360-365`), so the pin is that refusal's own test, not a census.

### 6.2 DDLs whose `permittedCommands` omit an op that tombstones their class — expect exactly the three, then zero after Inc 1

```
grep -n 'PermittedCommands' packages/wellness-domain/ddls.go | sed -n '/1101\|1186/p'
grep -n 'PermittedCommands: \[\]string{"OpenTab", "Settle"}' packages/cafe-domain/ddls.go            # cafeOpenTabGuard
```
The runtime pin is the two packages' integration tests (`packages/wellness-domain/integration_test.go`,
`packages/cafe-domain/integration_test.go`), which drive `ReleaseOrphanedBooking` and `SettleStaleTab` through the real
pipeline — Inc 2 must red them with Inc 1 reverted (revert-the-fix discipline, dossier).

### 6.3 Link-type DDLs — expect 3, all empty

```
grep -rn -B3 'Class:\s*"meta.ddl.linkType"' packages internal --include='*.go' | grep -v _test | grep -c CanonicalName   # 3
```

### 6.4 Interface doubles the signature change reaches — expect 8, none build-tagged

```
grep -rnE 'func \([^)]*\) (Validate|Commit)\((ctx|_) context\.Context, env \*' --include='*.go' . | grep -v 'step6_validate.go\|step8_commit.go' | wc -l   # 8
grep -rl '^//go:build ' --include='*.go' . | xargs grep -l 'Validator\|Committer'                                                             # (none)
```
The draft's census required the parameter to be named `ctx` and was scoped to `internal cmd`; it missed `StubValidator` (`_`)
and `packages/identity-domain`'s `interposingValidator`. Parameter-name-agnostic, repo-wide, or it is shaped by the answer.

### 6.5 Kernel topology links — expect 12, and no operator surface for `RevokeRole`

```
grep -c '"grantedBy", "grantedBy"' internal/bootstrap/primordial.go   # 2 loops × 3 permissions = 6 grantedBy
grep -c '"holdsRole", "holdsRole"' internal/bootstrap/primordial.go   # 6 holdsRole
grep -rln 'RevokeRole' cmd/loupe cmd/lattice | grep -v _test           # cmd/lattice/bootstrap/retire.go only
```

### 6.6 Ops bundling a subtype mutation with an `instanceOf` tombstone — expect 1

The key is built into a variable and tombstoned lines later, so a one-line grep prints nothing (the draft's did). Two stages:

```
grep -rn '\.instanceOf\.' packages --include='*.go' | grep -v _test | grep -oE '^[^:]+:[0-9]+:\s*[a-z_]+ = ' | sed -E 's/ = $//'   # the variables holding an instanceOf link key
grep -rn 'make_tombstone(instance_of_lnk)' packages --include='*.go' | grep -v _test                                             # lease-signing/scripts.go:1612 (built at :1532)
```
Stage 1 lists the six scripts that build such a key (lease-signing ×3, service-domain ×3); stage 2 finds the one that
tombstones it. Expect exactly one; any new one is a second consumer of the `committedOnly` disposition.

## 7. Contract surface — none; the design builds to the contracts as written

Andrew, at review (2026-09-03): no contract changes. The draft proposed four clauses (Contract #1 §1.5 step 1 rewritten to
say which document's `class` governs; Contract #8 §8.4 and Contract #2 §2.6 naming the seeded links; Contract #7 §7.7 scoped).
Each described *how* the runtime keeps a promise the contract already makes, which is the implementation-detail failure the
contracts rule exists to stop:

- **Contract #1 §1.5/§1.6** promise that a governed class's `permittedCommands` gates the operations that write it. A tombstone
  is a write of that class. Which stored or declared field the gate reads to find the class is mechanism.
- **Contract #8 §8.4** promises *"an operation cannot disable auth"* and Contract #7 §7.7 that root capability is topology.
  A guard that refuses removing the seeded topology is the runtime keeping that promise; the key set it holds is mechanism.
  §7.7's *"removing the role's inbound `grantedBy` links drops the corresponding capabilities"* describes what a removal does
  when one happens; it does not promise that the seeded edges are removable, and the bypass suite that proves it builds its
  own grants (`capadv_runtime_reserved_grant_test.go:105,220,236`), so it keeps proving it.
- **Contract #2 §2.6**'s `ProtectedKey` row already reads *"the path-independent kernel/auth bricking guard"*; a link that
  bricks auth is inside that sentence.
- **Contract #3 §3.3** is already honoured (§1.3).

The mechanism lives in `docs/components/processor.md` (the build updates *The 9-step write path* row 6, *Kernel protection*,
and the `reconcile.go` comment) and in this design. `internal/bypass` stays in Inc 3's gates.

## 8. Reconciliation with the existing mental model

- **"Didn't `rejectProtectedMutations` already close bricking?"** For vertices and their aspects, yes. Root is topology
  (Contract #6 §6.1), the topology is links, and links were declared out of scope in the guard's own comment. The stranded-epoch
  design built on the gap deliberately, which is why the closure is an exact current-epoch set and not a marker.
- **"Didn't the write-once guard cover tombstones?"** It covers *rewrites* of permission/role bodies; a bare tombstone rewrites
  nothing. Its tombstone-with-document arm covered an overlay the parser already refuses; both go as dead code.
- **"Didn't the tombstone-body-preservation design settle the supplied document?"** Yes — Fire 2 of that design (ratified
  2026-07-22) made the parser refuse it, and its alternative B rejected honouring one. This design changes nothing there; the
  draft's §1.3 had missed the parser and re-opened a closed question, which §11 C1 caught.
- **"Isn't a revive of a kernel link exactly the 'unlogged privilege escalation' `reconcile.go` warns about?"** The warning is
  about the *seeder* rewriting a tombstone at boot — no op, no actor, no audit. The admitted revive is an `AssignRole` op:
  actor-attributed, audited, permission-checked, and the grant-edge-provenance stamp rides it. The two are different planes.
- **"Isn't PackageScope rule 1 the `holdsRole` guard?"** Only for the three lifecycle ops. This design does not widen it (F).
- **"Didn't the hard-delete design settle per-DDL opt-ins?"** Its shelving condition (1) — *"`delete` becomes a per-DDL
  structural opt-in; omission denies at step 6"* — is the same instinct: the DDL declares who may remove. Here no new verb; the
  existing `permittedCommands` list is the declaration, made to apply to the removal verb it always named.
- **"Is this the parked authority-minting design by another door?"** No. That design admits or refuses what a package may
  *mint* (permissions, roles, `holdsRole` creates). This one makes the existing write-scope apply to removals and protects the
  kernel's twelve links. Its residual list gains a pointer (§2.4); its revive trigger is unchanged.
- **"Why not fail closed on an absent stored key?"** Because the kernel cascade tombstones keys that may not exist (H), and an
  absent key has no body to protect. Documented in Contract #1 §1.7 already.
- **"Does the stored-class walk against committed state change what a *create* resolves?"** No — creates keep batch-first
  resolution; only the stored-class check reads committed-only.
- **"New state?"** One process-lifetime set, wired like `SystemActorKeys`; the per-attempt prior map already existed.

## 9. Decomposition for the Steward

Order: **Inc 1 → Inc 2 → Inc 3.** Inc 1 clears the corpus so Inc 2 lands over zero debt (migrate → gate). Inc 2 and Inc 3 are
independent of each other and each **posture-changing** (full review depth per `agents/steward/SKILL.md` §4); Inc 1 is a
routine package fix.

### Inc 1 — three `permittedCommands` additions (packages, XS)

`wellness-domain`: add `ReleaseOrphanedBooking` to `sessionSeatClaim` (`ddls.go:1101`) and `sessionBookerClaim` (`:1186`).
`cafe-domain`: add `SettleStaleTab` to `cafeOpenTabGuard` (`:294`) and fix its doc comment. Manifest version + `Version`
constant bumps in both. **Owns:** no new test — the packages' integration tests already exercise both ops; Inc 2 is what makes
them the pin.

### Inc 2 — stored-class governance + the tombstone document honoured (Processor, S–M, posture-changing)

The §3 hoist (shape pre-pass ahead of the read; `ReadPrior` on `Committer`; `prior` into `Validate` and `Commit`; `Commit`
tops up; eight doubles), §2.1 in `validateOne` (stored class for `update`/`tombstone` via `prior.lookup`; declared class as
today, its chain walk budget-free on an update; `committedOnly` chain disposition; read faults retryable; corrupt body refused;
`class`-is-a-string in the batch pre-pass), §2.2 dead-code removal, `docs/components/processor.md` row 6, and the two package
comments that describe the bypass as a property (`lease-signing/ddls.go:609-615`, `lease-signing/scripts.go:1600-1609`)
rewritten to describe the gate. **Owns:**
- `TestValidate_TombstoneGovernedByStoredClass` — a bare tombstone of a key whose stored class's DDL omits the op is refused
  `permittedCommands`; the same tombstone by an admitted op passes; a tombstone of an absent key passes (H).
- `TestValidate_UpdateGovernedByStoredAndDeclaredClass` — an update re-typing `patient → foo` under an op `patient` does not admit
  is refused; the same op re-typing under an admitted op passes; a same-class update is unchanged; a re-typing of a
  chain-resolved subtype with the script's live-read budget already exhausted is still refused (the declared walk is off-budget).
- `TestValidate_StoredClassAuthorityIgnoresBatchInstanceOfTombstone` — a subtype root update bundled with a tombstone of its
  own `instanceOf` still resolves the committed authority (mutation-test: with the `committedOnly` disposition removed, the
  test must red).
- `TestValidate_StoredClassReadFaultIsRetryable` — a faulting chain read or prior read yields a retryable failure (`NakWithDelay`
  path), never the permissive default and never a terminal refusal.
- `TestValidate_CorruptStoredBodyIsRefused` — a stored entry that does not decode, and a stored `class` that is not a string,
  each refuse a tombstone of the key; a stored body with no `class` field is permissive.
- `TestValidate_MalformedKeyRefusedBeforeAnyRead` — a mutation key nats.go would reject (`"vtx.patient.a b.x"`) is refused
  `keyPattern` with zero `KVGet`s issued (counting committer double), not redelivered.
- `TestCommit_TopsUpPriorForInjectedMutation` — a task-path op whose auto-complete injection adds the task root after step 6
  commits with the task's `createdAt`/`createdBy`/`createdByOp` preserved and the injected update revision-conditioned
  (revert-proving: with the top-up removed, the provenance is re-stamped and the test reds).
- `TestCommit_TombstoneDocumentBranchRemoved` — a hand-built `MutationOp{Op: "tombstone", Document: {…}}` writes the stored
  body unchanged with `isDeleted: true`; documented as a unit assertion over an unreachable shape.
- The two package integration suites red with Inc 1 reverted (§6.2) — asserted once in the build note, not as a test.
- The declared-read drift and NFR-S6 wire-shape suites stay green: the hoist adds no per-cause work (the pass runs for every
  operation with an update/tombstone, regardless of outcome).

### Inc 3 — kernel topology links protected (bootstrap + Processor + cmd/processor, S, posture-changing)

`bootstrap.KernelTopologyLinkKeys()` (composed from `KernelGrantLinkKeys()` + the six constants), `AuthWiring.KernelLinkKeys`,
`NewCommitter` set, `rejectProtectedMutations` link arm with the seeded-shape revive exemption and link-parsed `Root`,
`retire`'s `ProtectedKey` → epoch-skew diagnosis + help-text precondition, and the comment sweep: `reconcile.go:118-123` (the
links are now inside the guard; the "never rewrite a soft tombstone" rule stays, for the seeder), `system_actors.go:90-93`
(stale "does not load"), `install_ddl.go:154-158,225-232` (guard "KVGets each tombstone's root" — now also the link set),
`opwire.go:172-177` (`ErrCodeProtectedKey` definition), and `docs/components/processor.md` *Kernel protection*. No contract
edit (§7). **Owns:**
- `TestKernelTopologyLinkKeys_MatchesSeededEntries` (bootstrap) — the six `holdsRole` keys byte-equal to what `PrimordialEntries`
  emits (the grant half is already pinned by `TestKernelGrantLinkKeys_MatchesWhatTheSeederEmits`); twelve total;
  `ErrPrimordialIDsUnloaded` when unloaded, with no panic.
- `TestCommit_KernelLinkTombstoneIsProtected` — `RevokeRole`-shaped tombstone of the admin's `holdsRole` → `ProtectedKey` with
  `Root` = the admin key; a soft-delete `update` (`isDeleted: true`) of it → `ProtectedKey`; an `update` re-pointing
  `targetVertex` → `ProtectedKey`; the same shapes against a non-seeded identity → commit; against a link whose target is a
  *different* role id (a stranded epoch) → commit.
- `TestCommit_KernelLinkReviveIsAdmitted` — over a stored soft-tombstoned kernel link, the `revive_link`-shaped update
  (`isDeleted: false`, seeded endpoints and class) commits (revert-proving: with the exemption removed, reds).
- `TestCommit_EmptyKernelLinkSetProtectsNothing` — documents the test-posture default.
- `cmd/processor` wiring pin — the wired set is `bootstrap.KernelTopologyLinkKeys()` and non-empty after `Load`.
- `cmd/lattice/bootstrap` — a `ProtectedKey` reply on a `retire` revocation is reported as epoch skew (unit test over the reply
  classifier).
- Gates: `make verify-kernel` unchanged (it asserts existence, not immutability); `go test ./internal/bypass/` (Contract #7
  §7.7's proving suite); `make test-control-plane-authz` and the convergence-tagged suites run because `Committer`'s interface
  changed in Inc 2 — enumerate per CLAUDE.md.

## 10. Risks, and the things I want disagreed with

- **The interface change is the widest blast radius** — eight doubles, all enumerated, none build-tagged, one in a vertical's
  own test helpers and two in the non-test `testutil` package. If a future harness adds one, the compiler says so. Acceptable.
- **The revive exemption is the one place the link arm reads a body.** It compares five fields of the written document against
  the key's own segments; the comparison is total (no field is optional) and a mismatch refuses. The test that pins it is
  revert-proving in both directions (§9 Inc 3).
- **The epoch precondition is operational, not enforced.** A Processor that has not rolled refuses `retire` loudly and by
  name; the positive pre-check is deferred to a Health-KV self-report if a real deployment ever trips it.
- **Stored-class governance could refuse a shipped flow the census missed.** The census is executable (§6.1/§6.2) and the
  packages' own suites are the runtime pin; a refusal is loud (`DDLViolation`), never silent. The Steward re-runs the census at
  Phase 0.
- **The `committedOnly` chain disposition is the one conjunct with no shipped consumer that needs it** (one op bundles the two,
  and it is admitted either way). I kept it because it is the same principle (§2.1) and ten lines; if the Steward finds it
  costs more than that, it is the first thing to cut, with a residual line rather than a row.
- **`KernelLinkKeys` empty in tests is fail-open by necessity.** The production pin is the mitigation, mirroring
  `SystemActorKeys`. A stronger shape — refuse to construct a capability-mode pipeline with an empty set — is available if Andrew
  wants it; it would touch every processor test fixture.
- **What this does not do:** close `holdsRole` creation (parked), close role/permission/roleindex tombstones by DDL (write-once
  is their gate), refuse absent-key tombstones (H), package-scope the `permittedCommands` match (§2.1, residual line), or add a
  positive epoch pre-check to `retire` (§2.3).

## 11. Adversarial pass

Run 2026-09-03, before the board flip, by two independent read-only reviewers: **M** (mechanism — the cited code, the hoist,
the chain walk, the kernel set, `retire`, OCC) and **C** (census + contracts — every §6 command re-run and re-derived with an
independent pattern, every contract quote opened). Both were briefed to falsify, not confirm. Every finding below has been
verified by Winston against the cited lines before folding; the body sections above are rewritten in place, not bannered.

| # | Severity | Finding | Disposition |
|---|---|---|---|
| M1 | BLOCKING | Task auto-complete injects an `update` of the task root *after* step 6 (`autocomplete.go:86-124`, `commit_path.go:443,909-948`, up to three `Commit` calls); a `prior` read at 5.5 has no entry, so provenance is re-stamped and all three step-8 guards (incl. the new link arm) go blind for that key | Folded §3: `Commit` tops up the map for any update/tombstone key it was not handed; owning test in Inc 2 |
| M2 / C3 | BLOCKING | "Revival is a reseed" is false: `reconcile.go:113-131` never rewrites a soft tombstone, `create` conflicts on one; `AssignRole`'s `revive_link` update (`rbac-domain/ddls.go:159-188`) is the only heal for an already-revoked kernel link, and the draft's immutability rule removed it | Folded §2.3: the arm refuses tombstone / soft-delete / re-point and admits the seeded-shape revive; For-Andrew (ii); alternative K; §8 |
| M3 | BLOCKING | Hoisting the read ahead of the key-shape gate: nats.go v1.52.0 returns `ErrInvalidKey` for a malformed key, which the pass treats as retryable — a terminal `keyPattern` refusal becomes an unbounded redelivery loop | Folded §3: batch-wide shape pre-pass opens step 5.5, before the first read; owning test in Inc 2 |
| C1 | BLOCKING | §1.3 attacked unreachable code: the parser refuses a tombstone with a document (`starlark_runner.go:360-365`, tombstone-body-preservation Fire 2, ratified 2026-07-22, its alt B rejected); the "chosen class" vector does not exist and alt G offered Andrew a choice already made | Folded §1.3, §2.2, §5 G, §8, For Andrew; Inc 2 keeps the removal as dead code |
| C2 / M8 | BLOCKING | §6.1's command prints 79 (75 at the filing commit), never 48; 13 lines are helper bodies; population is 66 across 13 packages, so "45 of 48" was arithmetic over a set that did not exist | Folded §1.7, §0, §6.1. C re-walked all 66 against all 140 non-empty `permittedCommands` lists: the three refusals are the complete set — the conclusion survived, the arithmetic did not |
| M4 | SHOULD-FIX | Absent and corrupt collapsed: `readPriorDoc` keeps a `Found`, nil-`Doc` entry on decode failure, and nothing validates `class` is a string — one admitted `{"class": 7}` ungoverns the entity forever | Folded §2.1: `prior.lookup`, corrupt ⇒ refusal, `class` string check in the batch pre-pass; owning test |
| M5 | SHOULD-FIX | A chain-read fault as terminal `DDLViolation` permanently rejects a valid op on a transient blip; step 6.5's precedent is terminal because the alternative is plaintext | Folded §2.1: read faults retryable, refusal reserved for a verdict or a corrupt body |
| M6 | SHOULD-FIX | The declared-class walk stays on the script's live-read budget and faults open, so the re-typing conjunct is submitter-exhaustible — the same switch-off row B was rejected for | Folded §2.1: the declared walk on an `update` runs `Checked`, off-budget; owning test extended |
| M7 / C7 | SHOULD-FIX | Doubles census shaped by the answer (`ctx` name, `internal cmd` scope): `StubValidator` and `identity-domain`'s `interposingValidator` missed — 8, not 7 | Folded §3, §5 A, §6.4, §1.7, §10 |
| M9 / C4 | SHOULD-FIX | `KernelTopologyLinkKeys()` re-derived a shape `KernelGrantLinkKeys()` already owns and pins, and `substrate.LinkKey` panics on an unloaded id | Folded §1.5, §2.3: two-line composition, unloaded check first, concatenation; pin scoped to the `holdsRole` half |
| M10 | SHOULD-FIX | Epoch awareness holds only once every Processor replica has restarted on the new table; `retire` verifies against Core KV, never the Processor | Folded §2.3 precondition + `ProtectedKey` ⇒ epoch-skew diagnosis in `retire`; For Andrew (iii); §10 |
| C5 | SHOULD-FIX | §6.6's one-line grep prints nothing; the key is built at `:1532` and tombstoned at `:1612` | Folded §6.6 two-stage census; §1.7 |
| C6 | SHOULD-FIX | `permittedCommands` matches the bare op name; `DebitAccount` is defined by two packages, so the cross-package closure is name-scoped | Folded §2.1 (stated property, residual line), §1.7 census row |
| C8 | SHOULD-FIX | §1.7's soft-delete row missed `identity-hygiene` (3 sites), incl. the loop that copies the stored class and may write `""` — the one stored/declared divergence in the corpus, and a `holdsRole` *tombstoner* | Folded §1.4, §1.7, §2.3 table, §2.4 |
| C9 | SHOULD-FIX | Contract #7 has no §7.8; the links are §7.2 item 8, the topology sentence is §7.7; "No default class" is §1.5 not §1.6 | Folded §1.5; §7's contract edits later withdrawn entirely at Andrew's review |
| C10 | SHOULD-FIX | Contract #7 §7.7 promises a `grantedBy` removal the runtime will refuse for the six seeded edges, and `internal/bypass` was in no gate list | `internal/bypass` added to Inc 3's gates. The proposed scoping clause was withdrawn at review: §7.7 describes what a removal does, not that the seeded edges are removable (§7) |
| C11 | SHOULD-FIX | §6.1's second command inspected one line after each helper `def` and could not have disagreed | Folded §6.1: command deleted, parser cited |
| M11 | NOTE | `Root` for a link is synthesised from the key, and `opwire`'s `ErrCodeProtectedKey` definition becomes false | Folded §2.3, Inc 3 sweep |
| M12 | NOTE | The unconditioned-write window (absent-key update/tombstone) widens across 6, 6.5 (Vault) and 7 | Folded §3, §4 (accepted, same race as today) |
| C12 | NOTE | Four in-repo comments become false (`lease-signing/ddls.go:609-615`, `scripts.go:1600-1609`, `install_ddl.go:154-158,225-232`) | Folded Inc 2 / Inc 3 sweeps |

**Verified true by the passes** (recorded so the build does not re-litigate them): `readPriorDocuments` reads key + root for
every update/tombstone with no subset (M-T1); `DDLResolutionMemo` cannot be poisoned across dispositions (M-T2); exactly twelve
seeded links, three ways (M-T3, C-T2); every conditioned mutation conflicts on a concurrent write (M-T4); no build-tagged file
implements or calls either interface, repo-wide (M-T5, C-T9); the `system_actors.go` comment is stale (M-T6, C-T6); the
write-once tombstone arm is reachable only through the overlay (M-T7); `derive_reads` runs before any mutation exists (M-T8);
NFR-S6 has no timing mechanism to break (M-T9); the three `linkType` DDLs are all empty and `holdsRole`/`grantedBy` have none
(M-T10, C-T4); the three refusals are the complete set over 66 emissions and 140 lists (C-T1); kernel/meta ops stay permissive
under every kernel DDL (C-T3); `RevokeRole`/`RevokePermission` are submitted only by `rbac-domain` and `retire`, never by Loupe,
Loom, Weaver or a schedule (C-T5); every contract sentence quoted in this doc checks out verbatim (C-T7); the lease-signing
bundle is admitted either way (C-T8); no in-flight design overlaps step 6/8, the interfaces, `AuthWiring` or protected keys
(C-T10).

**Lesson folded to the skill and memory** (designer §6): the draft attacked a branch the parser had already closed under a
ratified design, and censused an interface with a grep shaped by the expected count. Both are one grep away — "is the shape
even constructible?" before pricing a fix, and a parameter-name-agnostic, repo-wide pattern for any interface census.
