# Story 1.5.5 — Adversarial CR Report

**Reviewer:** CR sub-agent (claude-opus-4-8) · **Date:** 2026-05-30
**Scope:** Route capability-package install/uninstall through the Processor (`InstallPackage`/`UninstallPackage` kernel ops), kernel-protection residual, PreInstall fold, uninstall OCC CAR, testutil refactor, Contract #8.
**Verdict:** Review-only. Implementation is broadly sound and the install half is genuinely proven. **One P1 design gap on kernel-protection robustness** (the headline adversarial focus) must be adjudicated before this closes the 1.5.2 residual. Everything else is P2/clean.

---

## Triage summary

| Sev | Count | Items |
|-----|-------|-------|
| P0  | 0     | — |
| P1  | 1     | KP-1: UninstallPackage protected-key guard is dead code (empty `state`) — bypass surface for the catastrophic kernel-destruction Winston deferred to 1.5.5 |
| P2  | 3     | KP-2 (no test for the install/uninstall protected path), B-1 (dead `now` param in build.go), DOC-1 (Contract #8 / script-comment overstate the guard) |

### PROTECTED-KEY VERDICT (headline)

**Script-level protection is sufficient for the `MetaRootDDLScript` path (real + tested) but is NON-FUNCTIONAL for the `UninstallPackage` path, which is the exact catastrophic surface Winston flagged.** The `_is_protected_root(state, root)` check in `install_ddl.go` keys off `data.protected` in the hydrated `state`, but the installer never sets `ContextHint.Reads`, so for both InstallPackage and UninstallPackage ops **`state` is empty** and the check is always `False`. InstallPackage is incidentally safe (create-only conflicts at commit on any pre-existing protected key). **UninstallPackage is not:** an operator-authed caller (the operator role is granted the `UninstallPackage` permission primordially) submitting a crafted `declaredKeys` that includes a protected kernel key (e.g. `vtx.meta.<MetaRootKey>`, the `CapabilityLens`, the operator role) will have it tombstoned unconditionally — the script's protected check is dead, step-6 has no class to enforce against (tombstone documents carry no `class`), and there is no Processor-level backstop.

**Recommendation: MUST-FIX-NOW a minimal backstop, OR accept-with-explicitly-documented-residual + a tracked follow-up — Winston's call, but do not let this silently ship as "closed."** The cheapest correct fix is a **Processor-level (step-6 or committer) rejection of any tombstone/update of a root whose live doc carries `data.protected==true`**, independent of which DDL emitted it. That is exactly the "where the protected set lives + who enforces it" mechanism the 1.5.2 deferral (CAR lines 361–368) asked for, and it closes the residual for *all* paths rather than per-script. If accepted-as-residual instead, the story's §6 checkbox "closes the 1.5.2 kernel-protection residual" is **overstated** and must be downgraded to "partially closes (MetaRoot path only)."

---

## P1 — KERNEL-PROTECTION ROBUSTNESS

### KP-1 (P1) — `UninstallPackage` protected-root guard never fires; protection is bypassable by a privileged caller

- **Where:** `internal/bootstrap/install_ddl.go:69-79` (`_is_protected_root`), `:164-166` (uninstall call site); `internal/pkgmgr/installer.go:227-274` (`submitOp` sets no `ContextHint`); `internal/processor/step4_hydrate.go:143-165` (hydration is ONLY `ContextHint.Reads`); `internal/processor/step6_validate.go:73-152` (no protected backstop).
- **What:** The protected-root check in the Install/Uninstall scripts inspects `state[root].data.protected`. `state` is the hydrated map, populated exclusively from `env.ContextHint.Reads` (step4_hydrate.go:146). `Installer.submitOp` builds the `OperationEnvelope` with no `ContextHint` (installer.go:232-240), so `state` is **empty** for every InstallPackage/UninstallPackage op. Therefore `root not in state` is always true → `_is_protected_root` always returns `False`. The guard is dead code.
  - InstallPackage is saved by create-only semantics: writing any existing (protected) key fails the atomic batch on a CreateOnly conflict. So the install path is *incidentally* protected, though the "clear ProtectedKey error" the script comment promises never materializes (the caller sees a create-conflict instead).
  - **UninstallPackage emits `tombstone` ops, not `create`** (install_ddl.go:168). A tombstone of an existing protected key succeeds. There is no create-only safety net and no Processor-level backstop (grep confirms `protected` appears in zero Go files under `internal/processor`, `internal/committer`, `internal/substrate`). A crafted `{"name":"x","declaredKeys":[{"key":"vtx.meta.<MetaRootKey>"}, ...]}` would tombstone the kernel root DDL / CapabilityLens / operator role.
- **Why it matters:** This is precisely the failure Winston classified as "catastrophic" and deferred to 1.5.5 (CONTRACT-AMENDMENT-REQUEST.md:361-368: "no guard preventing TombstoneMetaVertex … from targeting primordial kernel entities … needs a protected-key mechanism (where the protected set lives + who enforces it)"). The 1.5.5 implementation added the guard to `MetaRootDDLScript` (which DOES work — see below) but left the *new* `UninstallPackage` op — a brand-new, operator-reachable tombstone path — without working protection. The residual is not closed; it is relocated.
  - **Threat model:** `UninstallPackage` is operator-privileged. The operator role is granted the permission primordially (nanoid.go:471-472; primordial.go:490-512). The whole point of §3.4 "defense in depth" is to protect the kernel even from a privileged/compromised/mistaken operator. A trusted-operator argument does NOT discharge this, because Winston's own deferral framed kernel protection as a hard guard, not an operator-trust assumption — and `make up` runs `LATTICE_AUTH_MODE=stub` (Makefile:38), under which *any* publisher to `ops.meta` is authorized.
- **Fix (recommended, minimal, path-independent):** Add a Processor-level backstop that rejects any `tombstone`/`update` mutation whose target root document (already loadable by the Committer at commit, or via a targeted read in step-6) carries `data.protected==true`, regardless of the DDL that produced it. This is one guard, enforced by the kernel, covering MetaRoot + Uninstall + any future op. Alternative (weaker, per-path): have the installer populate `ContextHint.Reads` with each declared key's root so the existing script check actually has state to inspect — but this trusts the client to declare reads honestly and an adversarial submitter simply omits them, so it is NOT a real defense. Prefer the Processor-level backstop.
- **If accepted as residual:** downgrade the §6 "closes the 1.5.2 kernel-protection residual" claim to "MetaRoot path only," and open a tracked follow-up CAR. Do not mark the 1.5.2 residual closed.

---

## P2

### KP-2 (P2) — No test covers the Install/Uninstall protected path; the passing protected test exercises a different (working) guard

- **Where:** `packages/rbac-domain/install_flow_test.go:243-279` (`TestInstallFlow_ProtectedMetaVertexRejected`).
- **What:** The protected-rejection test submits `TombstoneMetaVertex`/`UpdateMetaVertex` with `Class:"root"` and `ContextHint.Reads:[protectedKey]` — i.e. it drives the **`MetaRootDDLScript`** `is_protected` guard (meta_ddl.go:115-130, 238-239, 388-389), which IS real and solid (its `vertex_alive` requires `meta_key` in Reads, so the root IS hydrated). That guard correctly rejects. **But no test submits an `InstallPackage`/`UninstallPackage` op targeting a protected key**, which is the path that is actually broken (KP-1). The green test gives false confidence that "§3.4 protected rejection" is covered end-to-end.
- **Why:** A regression test that exercised an `UninstallPackage` with a protected key in `declaredKeys` would have caught KP-1 immediately (it would commit the tombstone instead of rejecting).
- **Fix:** Add a test that submits `UninstallPackage` with a protected root in `declaredKeys` and asserts rejection (after the KP-1 backstop lands). Until then, KP-2 is a symptom of KP-1, not independent.

### B-1 (P2) — Dead `now time.Time` parameter in `buildInstallBatch`

- **Where:** `internal/pkgmgr/build.go:35` (param `now time.Time`), called at `installer.go:171`.
- **What:** Since the manifest now emits logical documents with no provenance stamping, `now` is no longer referenced anywhere in `buildInstallBatch` (confirmed: only the signature line mentions it). It is threaded from `i.Now()` purely as vestigial state.
- **Why:** Harmless (a used param can't trip `unused`), but it is dead surface that misleads a reader into thinking the builder timestamps something. Cleanliness only.
- **Fix:** Drop the param (and the `now := i.Now()` at installer.go:170 if it becomes unused).

### DOC-1 (P2) — Script comment + (likely) Contract #8 overstate the protected guard

- **Where:** `internal/bootstrap/install_ddl.go:18-22` (header comment: "Protected roots present in `state` … are rejected"); `docs/contracts/08-package-install.md` (protected-key claims).
- **What:** The script header presents the protected-root reject as an active guardrail; per KP-1 it is inert for the only way these scripts are invoked (empty `state`). The comment is technically hedged ("present in `state`") but reads as a working defense. If Contract #8 asserts UninstallPackage rejects protected keys as a guarantee, that claim is false today.
- **Fix:** Either land the KP-1 backstop (making the claim true), or amend both the comment and Contract #8 to state plainly that protected-key rejection on the install/uninstall path depends on the Processor-level backstop / is currently a documented residual.

---

## Areas reviewed and found SOUND (no material findings)

- **Guardrail completeness (install_ddl.go) — #2:** Key-shape (`_is_valid_key_shape`), create-only enforcement, and underscore-aspect reject are present and correct *as input validators*. `_is_valid_key_shape` is permissive (accepts any `vtx.<type>.<id>[...]` / `lnk.<...>` with ≥3 segments) but step-6 `substrate.ClassifyKey` re-validates every mutation key against Contract #1 (step6_validate.go:87-95), so a malformed key is caught by the kernel regardless. The worst a crafted **InstallPackage** payload can do is create *new* non-kernel vertices/aspects/links with arbitrary class/data — which is the legitimate function of an install and is create-only (cannot overwrite anything existing, including protected roots, via the CreateOnly conflict). Acceptable for an operator-privileged op. The one real hole is the **tombstone path of UninstallPackage** (KP-1), not the input shape checks.
- **MetaRoot Update/Tombstone guard (#1c):** Solid. `vertex_alive` requires `meta_key` in `ContextHint.Reads`, so `is_protected` always has the root in `state`; the guard fires for any `Class:"root"` op. Correctly tested.
- **testutil refactor — #3:** `RunMetaInstallPipeline`/`MakeStubPipeline` wire the REAL Executor, Validator, and Committer (commit_path.go:616-633); only the Authorizer is stubbed. Installs run the actual DDL script + step-6 + step-8 atomic commit — not vacuous. The two "duplicate"-failure fixes (`TestFR19_NFR_S10`, `TestGate4`) were **real root-cause fixes**, not masks: committed install ops lingered on the shared `ops.meta` subject and replayed under a later DeliverAll consumer as spurious duplicates; the stop-time `Purge(ops.meta)` + consumer delete + `core-events` stream provisioning (install_phase1_packages.go:107-122; pipeline.go) address the genuine cause.
- **build.go rewrite — #4:** Entity NanoIDs are deterministic (`deterministicNanoID(name,version,tag)`, installer.go:204-214) for package/ddl/lens/perm/role; op `requestId` is deterministic (`deterministicNanoID(name,version,"install-op")`, installer.go:187) → step-2 dedup short-circuits a re-submit. `declaredKeys` includes the folded identity-domain role keys (build.go:49-59) with the manifest snapshot taken *before* the manifest aspect's own key is appended (build.go:179). Logical docs omit provenance (docVertex/docAspect/docLink, build.go:212-244); Processor stamps it. Correct.
- **PreInstall fold — #5:** `seed.go` deleted; `Definition.Roles []RoleSpec` added (definition.go:52-68); 3 identity-domain roles created in-batch with deterministic NanoIDs (`deterministicNanoID(name,version,"role:"+canonical)`, installer.go:127-131) + roleindex + aspects, all captured in `declaredKeys`. `resolveGrants`/`RoleIDs` intact: declared roles are registered into `RoleIDs` before `resolveGrants` runs (installer.go:123-134), and a post-resolution validation rejects any GrantsTo that didn't resolve to a valid NanoID (installer.go:142-148) — closing the silent-dangling-grant footgun. No breakage. `PreInstallFn` mechanism fully removed (grep-clean; only doc-comment references remain).
- **Uninstall OCC CAR — #6:** Deferring per-key OCC (unconditional tombstone) is acceptable for an admin-driven uninstall and the window is honestly documented (installer.go:483-494; CAR:372-417). The `KVGet.Revision` (stream-level) ≠ per-subject sequence (the `Nats-Expected-Last-Subject-Sequence` header, exposed only via the committing op's `OperationReply.Revisions`) rationale is **correct** — using the stream revision as `expectedRevision` would produce spurious `wrong last sequence` rejections. The proposed follow-up (persist install-reply revisions into `.manifest`) is sound. The batch remains atomic; only lost-update protection is relaxed. NB: this OCC gap is orthogonal to KP-1 (KP-1 is about *which keys* may be tombstoned at all, not about revision races).
- **M5/B2 + F-001 tests — #7:** `TestInstallFlow_M5B2_DomainOpWithoutRestart` genuinely proves no-restart coherence: it builds ONE `DDLCache` instance, refreshes it (asserting `rbac` absent), runs a single `sharedCachePipeline` over that same instance for both `ops.meta` and `ops.default`, installs, then asserts `cache.Lookup("rbac")` resolves and a `CreateRole` commits — all on the shared cache, no restart (install_flow_test.go:119-178). This is a real shared-instance test, not a fresh-cache cheat. `TestInstallFlow_F001_ReinstallNoOrphans` proves the folded roles are in `declaredKeys` (≥3 role + ≥3 roleindex), every declared key is tombstoned post-uninstall, and a fresh re-install succeeds (install_flow_test.go:184-238). Solid. (The protected test is real but covers the wrong path — see KP-2.)
- **grep-clean + Contract #8 — #8:** `grep -rn AtomicBatch packages/ internal/pkgmgr/ cmd/lattice-pkg/` returns only `packages/identity-domain/state_machine_test.go:264` — an unrelated lease-tombstone test fixture, correctly NOT an install write. Bootstrap retains its sanctioned seed path. Install writes are grep-clean. (Contract #8 doc not line-audited beyond the protected-claim concern in DOC-1.)

---

## Where nothing material was found

Findings #2 (guardrail input-shape), #3 (testutil coverage), #4 (build.go), #5 (PreInstall fold), #6 (uninstall OCC rationale), #7 (M5/B2 + F-001 tests), #8 (grep-clean) are all clean or carry only the P2 nits above. The single load-bearing issue is **KP-1** (with KP-2/DOC-1 as its test/doc shadows).
