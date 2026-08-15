# Permission/role provenance write-once — closing the `UpgradePackage` rewrite gap

**Status: 📐 awaiting-Andrew (ratification)** — Designer fire (Winston), 2026-08-14. Security &
trust-boundary item, S–M. No architectural fork; **Contract #8 touch staged uncommitted** (§7 below) —
flagged for Andrew per that alone, plus the security severity.

## For Andrew (one-look ratification block)

**What it does.** A live, non-root privilege-escalation gap: any actor holding `UpgradePackage`
(granted to the non-root `consoleOperator` role, not just the kernel `operator`) can submit a
hand-crafted mutation through the ordinary Gateway API that **rewrites an existing permission
vertex's `operationType`/`scope`/`origin`/`declaredBy`** — including one already `grantedBy` their
own role — self-escalating to any operationType with no new grant step. This design adds one
Go-level, commit-time, path-independent guard (`internal/processor/step8_commit.go`) that makes those
four fields **write-once** on a `vtx.permission.*` root once created — mirroring the existing
`rejectProtectedMutations` guard exactly (same prior-document read pass, already fetched; new sibling
function, new typed error, new wire code). Zero new state, zero new KV reads, no legitimate flow
broken (verified against every real caller — §4).

**No architectural fork.** This is an extension of an existing, already-ratified mechanism
(the step-8 protected-key guard), not a new trust-boundary decision.

**Contract touch:** `docs/contracts/08-package-install.md` §8.4 gains a new subsection describing this
guard, mirroring how §8.4 documents `rejectProtectedMutations` today. Edit is staged **uncommitted** in
`main` alongside the two pre-existing uncommitted §8.3/§8.6 edits (see §7) — same file, same posture,
not a third proposal at cross purposes with the first two.

**What this design deliberately does NOT fix** (found during grounding, scoped out, filed as two
follow-on board rows — §8): (a) a **`vtx.permission.*` CREATE forgery** vector — an attacker can mint a
brand-new permission vertex via `UpgradePackage`'s create arm with a self-declared `origin: "package"`,
since nothing server-side verifies a create mutation was actually produced by the named package's real,
compiled `Definition`; (b) **cross-package tombstone/DoS** — `UpgradePackage`/`UninstallPackage` can
tombstone *any* non-protected key regardless of whether the named package actually declared it. Both are
real, but they are a different, harder problem (verifying package-authored content server-side, when
Contract #8 §8.1's whole design is "client pre-computes, thin script trusts") — genuinely new design
territory, not a same-fire addendum to a field-level write-once guard.

---

## 1. Problem + intent

Board row (`lattice.md`, Security & trust boundary, ★★★, found 2026-08-14 by the grant-provenance
close-review): *"`UpgradePackage` accepts unvalidated mutations against permission/role vertex
classes… `consoleOperator` ('does not confer root') holds it at the meta lane — a live, non-root
escalation."*

`grant-provenance-runtime-permission-minting-design.md` (✅ RATIFIED, Branch A, Andrew 2026-08-13)
built the **origin invariant** — *"a `package`-origin vertex may grant any operationType its package
declares; a `runtime`-origin vertex may never confer a core-reserved operationType… Origin is
write-once — no operation may rewrite a permission vertex's body"* — and closed the **operation
channel** (`UpdatePermission` is deliberately ungranted to every role, and is itself in
`reservedOperationTypes`, so no held grant can rewrite a permission body through the RBAC op surface).
Its own §5.1 close-review named the gap this design closes: *"That closes the operation channel, not
every channel — `UpgradePackage`'s bootstrap DDL still accepts client-supplied mutations against any
`vtx.*` key… that gap is filed separately."* `packages/rbac-domain/permissions.go:21-24` carries the
identical comment. This design is that filed gap.

## 2. Grounding (verified live, this fire, `HEAD` at fire start)

- **`UpgradePackageDDLScript`** (`internal/bootstrap/install_ddl.go:197-261`) validates only:
  name/fromVersion/toVersion non-empty, each mutation's `op` ∈ {create,update,tombstone}, key-shape,
  underscore-aspect reject, `expectedRevision` is-integer-if-present. It runs with **empty hydrated
  state** (`ContextHint.Reads` is never declared) — by the script's own doc comment, *"protected-key
  (kernel/auth) protection is enforced authoritatively by the Processor commit-time guard… not
  here — the upgrade script runs with empty hydrated state."* Content-level enforcement is deliberately
  **not** the script's job; it is step 8's.
- **Step 6 never gates `class:"permission"`/`class:"role"`.** Census of every `CanonicalName:`
  registration under `packages/**` + `internal/bootstrap/**` (~150 DDLs): none is `"permission"` or
  `"role"`. `packages/rbac-domain/ddls.go:52` registers `CanonicalName: "rbac"`, but the documents it
  governs carry `class:"permission"`/`class:"role"`, never `class:"rbac"` — the exact-match resolver
  (`internal/processor/step6_resolve_ddl.go:278`) always misses, and neither vertex type carries an
  `instanceOf` link for the chain-walk fallback. `step6_validate.go`'s own doc comment: *"when no DDL is
  found for a mutation's class, the corresponding schema/permittedCommands/sensitive checks are
  skipped."* Confirmed by `grant-provenance-runtime-permission-minting-design.md:286-290`.
- **`consoleOperator` holds `UpgradePackage` at the meta lane, and is explicitly not root.**
  `packages/console-operator/permissions.go:69-71,73-84`: meta-lane grants are exactly
  `{InstallPackage, UninstallPackage, UpgradePackage}` — *"this does not confer root — every other
  privileged lane… and every other meta-lane op… stays ungranted."* `privilegedLaneAllowlist["UpgradePackage"]
  = {meta:true}` (`internal/processor/step3_auth_capability.go:419-430`) sanctions the lane.
- **The op payload is fully client-supplied.** `internal/gateway/gateway.go:405,786-822`
  (`buildEnvelope`): `OperationType`/`Lane`/`Payload` (incl. `mutations`) are attacker-controlled; only
  `Actor` is server-verified (from the Bearer token). Nothing restricts a submitted `UpgradePackage`
  payload to what `internal/pkgmgr.Installer.Upgrade` (`internal/pkgmgr/upgrade.go:94-155`) would
  actually have computed from the compiled-in `Definition` — the attacker skips `pkgmgr` entirely and
  crafts the Gateway call directly.
- **Permission keys are content-addressed on `(package, operationType, scope)`, never on `note`/`lanes`.**
  Contract #8 §8.1: *"Every entity's NanoID is derived from package name + entity tag… The permission
  tag keys on `operationType + scope` (logical identity), not the list index."* Confirmed in
  `internal/pkgmgr/build.go:346-393`: the deterministic key is fixed once at
  `(def.Name, p.OperationType, p.Scope)`; the created document is
  `{operationType, scope, origin:"package", declaredBy: def.Name, note?, lanes?}`. **Consequence: a
  legitimate version upgrade can never change an existing permission key's `operationType`/`scope`/
  `origin`/`declaredBy` — a change to any of those necessarily produces a *different* deterministic key
  (a create + a tombstone of the old key), never an update of the same key.** Only `note`/`lanes` can
  legitimately change on a **surviving** key (`internal/pkgmgr/upgrade.go:415-424`'s
  `logicalDocEqual`/update path — verified: it diffs the whole document and re-emits an `update` mutation
  whenever the body differs at all, but for a surviving key that can only ever be a `note`/`lanes` edit
  given the key-derivation invariant above).
- **The exact attack, concretely.** An actor holding `consoleOperator` (and, separately, some role `X`
  already `grantedBy` a narrow permission `vtx.permission.<id>`) submits
  `UpgradePackage{name:"<any-installed-package-name>", mutations:[{op:"update",
  key:"vtx.permission.<id>", document:{class:"permission", data:{operationType:"ShredRetentionClassKey",
  scope:"any", origin:"package", declaredBy:"<any-installed-package-name>"}}}]}`. No DDL gates it
  (step 6 misses). Step 8's only content check (`rejectProtectedMutations`) only fires for
  `data.protected == true` roots — ordinary permission vertices are never protected. The commit
  succeeds: the actor's **already-held** `grantedBy` link now confers the new operationType, with **zero**
  additional grant step (no forged link needed) — the permission key never changed, only its body did.
  This works whether the target permission's prior `origin` was `runtime` or `package`, and regardless
  of which package name the attacker names in the envelope (the name is never checked against the target
  key's actual owner — that's the separate, out-of-scope tombstone/create gap, §8).
- **`UpdateRole` (the one granted role-mutating op) never touches the role root** —
  `packages/rbac-domain/ddls.go:276-291`: it mutates only the `.description` **aspect**; a role's
  `vtx.role.<id>` root document is always `{}` (`packages/rbac-domain/ddls.go:263`,
  `internal/pkgmgr/build.go:76-98`) on every legitimate path.
  **Correction (build-time, closed by the shipped guard — see §13): this is a full authorization bypass,
  not a confusion vector.** `internal/refractor/ruleengine/full/executor.go`'s `resolveProperty` reads a
  node's **root document first** and only falls back to an aspect point-read when the root has no field
  of that name — so a top-level `canonicalName` field written onto the role **root** (not the aspect)
  shadows the real `.canonicalName` aspect in every cypher read, including the kernel root-grant lens
  (`internal/bootstrap/lenses.go:137`, `WHERE role.canonicalName.data.value = 'operator'`) and the
  wildcard read-grant lens (`lenses.go:359`). An `UpgradePackage` holder rewriting their own held role's
  **root** — not its aspect — with `{"canonicalName":{"data":{"value":"operator"}}}` reaches full kernel
  write-root plus an all-rows read grant, with no aspect ever touched. The guard therefore makes the
  **whole role root** write-once (byte-identical to the stored body, `isDeleted` excepted), not just the
  `.canonicalName` aspect — closing the shadow path structurally rather than by enumerating which fields
  could shadow which aspects.
- **Step 8 already reads the exact document this guard needs, for free.** `readPriorDocuments`
  (`internal/processor/step8_commit.go:513-563`) already `KVGet`s the stored document for **every**
  update/tombstone mutation's own key (not just the protected root) — `prior.doc(m.Key)` is already
  populated with the permission/role root's prior body by the time `rejectProtectedMutations` runs.
  **No new KV read is needed**; the new guard is a second pure function over data already in hand.

## 3. Reconciliation with the existing mental model

- **Didn't `0bb6daea` already handle this?** No — `0bb6daea` (`internal/pkgmgr/upgrade.go:354-378`,
  `diffManifest`'s already-tombstoned-skip branch) is **client-side**, inside the *honest* `pkgmgr`
  diff computation. It protects against `pkgmgr.Installer.Upgrade` accidentally reviving an
  out-of-band-revoked key. It does nothing against an attacker who skips `pkgmgr` and crafts the
  Gateway payload directly — which is exactly this vulnerability class. This design is the **server-side,
  authoritative** mirror `0bb6daea` does not provide, for the specific fields grant-provenance's
  invariant depends on. (The retention-class-key-custody design's §28 fix, `8796e6e9`, is the same
  shape one level up — a client-side diff exclusion with no server-side backstop — but for a different
  field/class; not duplicated here.)
- **Does this duplicate `rejectProtectedMutations`?** No — it is a **sibling**, not an overlap.
  `rejectProtectedMutations` guards **kernel-seeded, explicitly `protected:true`** roots (a handful of
  primordial entities) against *any* update/tombstone. This guard protects **every** `vtx.permission.*`
  root's four provenance fields — runtime-minted and package-declared alike — against rewrite by
  anything **other than** the RBAC op surface (which never rewrites them either, since `UpdatePermission`
  is ungranted). Different population, different fields, same mechanism shape.
- **New state?** None. Pure function over data step 8 already reads.

## 4. The shape

### 4.1 New guard function (`internal/processor/step8_commit.go`, beside `rejectProtectedMutations`)

Needs a new import — `internal/processor` does not currently import
`"github.com/operatinggraph/lattice/internal/substrate/keys"` (verified: no hit in the package). Add it;
`keys.IsVertexKeyOfType` and `keys.ParseAspectKey` are both public, stable helpers
(`internal/substrate/keys/keys.go:95-124`) already used elsewhere in the codebase for exactly this kind
of key-shape classification.

```go
// PermissionProvenanceError is the typed step-8 failure surfaced when an
// update mutation targets an existing vtx.permission.<id> root and changes
// data.operationType, data.scope, data.origin, or data.declaredBy — the four
// provenance fields grant-provenance-runtime-permission-minting-design.md's
// origin invariant depends on staying write-once. Permission entity keys are
// content-addressed on (package, operationType, scope) (Contract #8 §8.1), so
// a legitimate upgrade never needs to change any of the four on a surviving
// key — a real change produces a different key (create + tombstone), never an
// update. data.note / data.lanes may still change freely.
type PermissionProvenanceError struct {
	Key   string
	Field string // which of the four fields changed
}

func (e *PermissionProvenanceError) Error() string {
	return fmt.Sprintf("PermissionProvenance: update on %s attempted to rewrite provenance field %q", e.Key, e.Field)
}

// permissionProvenanceFields is exactly the set the origin invariant depends
// on. Order is deterministic so the reported Field is stable across runs.
var permissionProvenanceFields = []string{"operationType", "scope", "origin", "declaredBy"}

// rejectPermissionRoleRewrites is the authoritative commit-time guard closing
// the channel grant-provenance-runtime-permission-minting-design.md §5.1 named:
// UpgradePackage/UninstallPackage/InstallPackage (and any future path) trust
// client-supplied mutation bodies with no DDL gate (step 6 never resolves a
// governing DDL for class:"permission"/"role"). For every update mutation
// whose key is a vtx.permission.<id> root, the four provenance fields must be
// byte-identical to the prior committed document — an attacker cannot rewrite
// operationType/scope/origin/declaredBy on a key it (or anyone) already holds
// a grantedBy link to. For every update mutation whose key is a
// vtx.role.<id>.canonicalName aspect, the value must be byte-identical to the
// prior committed aspect (defense-in-depth: no live escalation depends on it,
// since a role root carries no data on any legitimate path — see design §2 —
// but a canonicalName rewrite is cheap to close with the same mechanism).
// create/tombstone mutations are exempt (out of this guard's scope — §8).
func rejectPermissionRoleRewrites(mutations []MutationOp, prior priorDocs) error {
	for _, m := range mutations {
		if m.Op != "update" {
			continue
		}
		if keys.IsVertexKeyOfType(m.Key, "permission") {
			priorDoc := prior.doc(m.Key)
			if priorDoc == nil {
				continue // no prior body — nothing to protect (create-then-immediate-update within one batch is not a legitimate shape and is caught elsewhere; absent-prior is simply not this guard's concern)
			}
			priorData, _ := priorDoc["data"].(map[string]interface{})
			newData, _ := m.Document["data"].(map[string]interface{}) // m.Document is already map[string]interface{} (script_context.go:201) — same access pattern as step65_encrypt.go:130, no new helper needed
			for _, f := range permissionProvenanceFields {
				if fmt.Sprint(priorData[f]) != fmt.Sprint(newData[f]) {
					return &PermissionProvenanceError{Key: m.Key, Field: f}
				}
			}
			continue
		}
		if _, vType, _, local, ok := keys.ParseAspectKey(m.Key); ok && vType == "role" && local == "canonicalName" {
			priorDoc := prior.doc(m.Key)
			if priorDoc == nil {
				continue
			}
			priorVal, _ := priorDoc["data"].(map[string]interface{})
			newVal, _ := m.Document["data"].(map[string]interface{})
			if fmt.Sprint(priorVal["value"]) != fmt.Sprint(newVal["value"]) {
				return &PermissionProvenanceError{Key: m.Key, Field: "value"}
			}
		}
	}
	return nil
}
```

Called from `Commit` right after `rejectProtectedMutations` (`step8_commit.go:173`), same error-handling
shape — a rejection here terminates before the atomic batch, no redelivery:

```go
if err := rejectProtectedMutations(result.Mutations, prior); err != nil {
    return CommitAck{}, err
}
if err := rejectPermissionRoleRewrites(result.Mutations, prior); err != nil {
    return CommitAck{}, err
}
```

`readPriorDocuments` needs **no change** — it already reads every update/tombstone mutation's own key
(§2), so `prior.doc(m.Key)` for a `vtx.permission.<id>` update is already populated.

### 4.2 Wire code + reply mapping (`internal/processor/opwire`, `envelope.go`, `commit_path.go`)

Mirror `ErrCodeProtectedKey` exactly — it is defined `ErrCodeProtectedKey ErrorCode = "ProtectedKey"` in
`internal/processor/opwire/*.go:175` (**not** a top-level `internal/opwire` package — verified live, the
package lives at `internal/processor/opwire`) and re-exported at `internal/processor/envelope.go:57` as
`ErrCodeProtectedKey = opwire.ErrCodeProtectedKey`:

```go
// internal/processor/opwire/<file>.go
ErrCodePermissionProvenance ErrorCode = "PermissionProvenance"

// internal/processor/envelope.go, beside ErrCodeProtectedKey
ErrCodePermissionProvenance = opwire.ErrCodePermissionProvenance
```

and in `commit_path.go`, beside the `*ProtectedKeyError` branch (`commit_path.go:444-459`):

```go
var provErr *PermissionProvenanceError
if errors.As(err, &provErr) {
    cp.deps.Metrics.OpsRejected.Add(1)
    cp.deps.Logger.Info("step 8: permission-provenance rejection",
        "requestId", env.RequestID, "key", provErr.Key, "field", provErr.Field)
    cp.replyTo(msg, BuildRejectedReply(env.RequestID, ErrCodePermissionProvenance,
        provErr.Error(), map[string]any{"key": provErr.Key, "field": provErr.Field}))
    return OutcomeRejected, substrate.Term
}
```

Terminal (`substrate.Term`, no redelivery) — same reasoning as `ProtectedKeyError`: a redelivery of the
identical attacker payload cannot succeed, the world is unchanged.

### 4.3 No new helper needed

`MutationOp.Document` (`internal/processor/script_context.go:198-203`) is already
`map[string]interface{}`, so `m.Document["data"].(map[string]interface{})` is a direct, existing-pattern
access (mirrors `internal/processor/step65_encrypt.go:130`'s `m.Document["data"]`) — no new accessor
function to write.

## 5. Executable census — every legitimate caller re-checked against the new rule

Run at build time, expected results recorded here so a future re-run re-verifies without re-deriving:

```sh
# Every UpgradePackage/InstallPackage caller in the corpus (must be pkgmgr only — no hand-rolled envelope).
grep -rn '"UpgradePackage"\|"InstallPackage"' --include=*.go . | grep -v _test.go
# Expected: internal/bootstrap/install_ddl.go (script consts), internal/pkgmgr/{upgrade,apply,installer}.go,
# cmd/lattice-pkg, cmd/loupe/pkg.go — no other submitter.

# Every legitimate update mutation pkgmgr's diff can emit against a vtx.permission.* key: confirm the
# only fields that vary across versions for a surviving key are note/lanes (never operationType/scope).
go test ./internal/pkgmgr/... -run TestUpgrade -v
```

Expected (pre-build, from reading `internal/pkgmgr/build.go:346-393` + `upgrade.go:415-424`): the
deterministic key for a permission is `(def.Name, p.OperationType, p.Scope)` — no code path constructs
an `update` mutation for a surviving permission key with a different `operationType`/`scope` than the
key was originally derived from; `origin`/`declaredBy` are computed the same way (`def.Name`) on every
call, never touched by an update. **If the census finds an update mutation for a surviving key where any
of the four provenance fields actually differs from the prior body, that is a pre-existing pkgmgr bug
this guard would newly reject — file it, don't loosen the guard** (mirrors the standing rule: negative
tests need their positive vector, and a rejection surfacing a latent bug is a finding, not a false
positive).

## 6. Contract surface

`docs/contracts/08-package-install.md` §8.4 ("Kernel protection") documents `rejectProtectedMutations`
today. This design adds a parallel subsection, staged **uncommitted** (§7):

> **Permission/role provenance protection.** In addition to the protected-root guard above, every
> `update` mutation (and any `tombstone` mutation that carries a document) targeting an existing
> `vtx.permission.<id>` root is rejected if it would change `data.operationType`, `data.scope`,
> `data.origin`, `data.declaredBy`, or `data.lanes` from the value already committed — the fields
> `grant-provenance-runtime-permission-minting-design.md`'s origin invariant and `platformLaneGate`'s lane
> gate depend on staying write-once outside the RBAC op surface (which itself never rewrites them —
> `UpdatePermission` is ungranted). `data.origin`/`data.declaredBy` may be set for the first time on a
> permission stored without them (a pre-existing installation predating their introduction); every other
> guarded field must already be present. `data.note` may still change freely. The identical rule makes a
> role's **entire root document** write-once (not only its `.canonicalName` aspect — a top-level field on
> the root shadows a same-named aspect in every cypher read, so the whole body must hold, `isDeleted`
> excepted) and protects the `.canonicalName` aspect itself the same way. This closes the channel the
> protected-root guard does not: an ordinary (non-`protected`) permission/role vertex, rewritten via
> `UpgradePackage` or any future path that trusts a client-supplied mutation body. Rejected with
> `ErrCodePermissionProvenance` (`"PermissionProvenance"`), terminal (no redelivery).

Not a change to §8.1's client-trust model (still "client pre-computes, thin script trusts") — an
*addition* to §8.4's kernel-protection guarantee list, same category as the guard it sits beside.

## 7. Staging note — the shared dirty file

`docs/contracts/08-package-install.md` already carries **two** pre-existing uncommitted edits (per
`retention-class-key-custody-design.md` §28.7 and the grant/role `0bb6daea` revival exception, both
awaiting Andrew, current `git diff` on `main`). **This design's §8.4 addition is a third, independent
edit to the same file** — additive, non-overlapping section (§8.4 vs. the other two's §8.3/§8.6), so no
merge conflict with either. Flagging this explicitly so Andrew's ratification pass can review all three
in one sitting rather than being surprised by a growing diff.

## 8. Non-goals — two related gaps found, deliberately not built here, filed separately

Both are genuinely different threat classes from the field-level rewrite this design closes, and both
need their own grounding pass, not a same-fire bolt-on:

- **(a) CREATE forgery.** `UpgradePackage`'s (and `InstallPackage`'s) **create** arm accepts a
  client-supplied document with no verification that it matches what the named package's real, compiled
  `Definition` would produce — an attacker can mint a **brand-new** `vtx.permission.<fresh-id>` vertex
  with a self-declared `origin:"package"`, `operationType:"<anything>"`, then link it `grantedBy` their
  own role via a second mutation in the same batch (link-key shape validates the same way; no ownership
  check exists for links either). This design's write-once guard does not touch creates by construction
  (there is no prior document to compare against) — closing it needs either (i) re-deriving the expected
  deterministic key server-side from `(package, operationType, scope)` and rejecting a create whose
  supplied key doesn't match its own claimed content (cheap, but only proves *internal consistency* of
  the forged payload, not that the *content* is real), or (ii) a genuinely new mechanism verifying
  submitted mutations against something the server can independently trust — which Contract #8 §8.1's
  "client pre-computes, thin script trusts" model does not currently provide for *any* class, not just
  permission/role. Needs its own design pass.
- **(b) Cross-package tombstone / manifest-ownership.** Neither `UpgradePackageDDLScript` nor
  `UninstallPackageDDLScript` verifies that a submitted update/tombstone's key actually belongs to the
  **named package's own committed `.manifest` aspect** — an actor holding `UpgradePackage`/
  `UninstallPackage` can tombstone (or, for non-permission/role classes, rewrite) any non-`protected` key
  in the graph while naming an unrelated installed package. A manifest-ownership check (verify the target
  key ∈ the named package's `.manifest.declaredKeys`, read server-side) would close this generally,
  across every vertex class the package-lifecycle primitives touch — not just permission/role — but it is
  a materially bigger mechanism (a new server-side KV read of the invoking package's manifest, applied to
  every class) than the four-field guard this design ships, and deserves sizing on its own.

Both filed as new `lattice.md` rows (§9) rather than left as prose in this doc — per steward guardrails,
a discovery either gets fixed this fire or gets one of the two sanctioned outs (needs Andrew / needs a
designer pass); both are the latter.

## 9. Board rows to file (Winston, same commit as this design)

1. **[bootstrap] `UpgradePackage`'s create arm can forge a package-origin permission/role vertex** — ★★★,
   M, 📐 needs designer pass · why: this doc §8(a).
2. **[bootstrap] `UpgradePackage`/`UninstallPackage` mutations aren't scoped to the named package's own
   manifest** — ★★, M, 📐 needs designer pass · why: this doc §8(b).

## 10. Decomposition for the Steward (single fire, no multi-fire split needed — S–M)

One increment, green in one pass:

1. `internal/processor/step8_commit.go`: `PermissionProvenanceError` type,
   `permissionProvenanceFields`, `rejectPermissionRoleRewrites`, wired into `Commit` right after
   `rejectProtectedMutations`; confirm/add the `mutationData` helper (grep first — §4.3).
2. `internal/processor/envelope.go` + `internal/opwire`: `ErrCodePermissionProvenance`.
3. `internal/processor/commit_path.go`: the `*PermissionProvenanceError` branch, mirroring
   `*ProtectedKeyError`'s (§4.2).
4. Tests, in `internal/processor/step8_commit_test.go` (the file exists, 17 existing `Test*` funcs, own
   fixture/harness style to mirror for setup — **verified live: neither `rejectProtectedMutations` nor
   `ProtectedKeyError` currently has a dedicated unit test anywhere in the repo** (grepped
   `internal/processor/*_test.go`, zero hits), so there is no same-mechanism precedent to mirror
   directly; use `step8_commit_test.go`'s own harness conventions instead. Worth a one-line note in the
   build note that the protected-key guard's own test gap is pre-existing and not this fire's to fix.
   Table-driven per the standing "one-clause predicate, enumerate the shapes" check:
   - **Positive (must still succeed):** an update to a surviving permission key changing only
     `note`/`lanes`; an update to a role's `.canonicalName` aspect with the *same* value (no-op update);
     any update to a non-permission/role class (unaffected).
   - **Negative, one per field:** update rewriting `operationType`, `scope`, `origin`, `declaredBy`
     alone (four cases, per the standing "one-clause predicate, enumerate the shapes" check) — each
     rejected with the correct `Field` in the error. A role `.canonicalName` rewrite — rejected.
   - **Boundary:** an update to a permission key with no prior document (absent) — allowed (matches
     `rejectProtectedMutations`'s "not found → not protected" posture; this is not the create-forgery
     path, §8(a), which is explicitly out of scope).
5. Run the §5 census live, record the actual output in the build note (not just cite the expectation).
6. `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
   `go test ./internal/processor/... ./internal/pkgmgr/...`.

**Review depth:** this is a security-plane, capability-adjacent change — full 3-layer adversarial per
steward SKILL.md §4, regardless of the S–M size.

## 11. Alternatives considered

- **Register `CanonicalName:"permission"`/`"role"` DDLs so step 6 gates them (Pattern A).** Rejected as
  the *primary* fix, worth doing *independently*: it's legal (`"permission"`/`"role"` aren't in
  `reservedTypeNames`), cheap, and closes a narrow side door (some *other* operationType smuggling a
  `class:"permission"` write past step 6) — but `UpgradePackage` itself must stay in any
  `permittedCommands` allowlist for real upgrades to work, so the DDL gate structurally cannot
  distinguish a legitimate diff-update from the attacker's identical-operationType payload. Not
  sufficient alone; not proposed as a substitute here (a future designer pass on §8(a)/(b) may still want
  it as defense-in-depth — noted, not built, since it buys nothing against *this* design's threat).
- **Field-level allow-list inside `UpgradePackageDDLScript` itself (candidate direction (b), teach the
  Starlark script class-specific knowledge)** — mirrors `UpdateMetaVertexDDLScript`'s per-field
  allow-list, but that script is deliberately narrow/single-class; `UpgradePackageDDLScript` is
  deliberately generic across every package and class (§4.1 of `install_ddl.go`'s own doc comment), and
  the script runs with **empty hydrated state** (§2) — it cannot read the prior document to compare
  against without a `ContextHint.Reads` declaration keyed on every mutation's own key, which the caller
  (`pkgmgr`) would have to supply and which reopens exactly the "trust the client" problem this design
  exists to close (a malicious caller simply omits the declaration or the Processor's hydration-fault
  path is bypassed some other way). **The Go/step-8 guard is strictly better**: it is
  path-independent (fires regardless of which script or future primitive emitted the mutation, exactly
  like `rejectProtectedMutations`), needs no script cooperation, and the prior document is already read
  for free (§2's last bullet).
- **Manifest-ownership check (verify target key ∈ named package's `declaredKeys`) as *this* design's
  fix, not §8(b)'s.** Considered and rejected as the primary mechanism here: it is *necessary* for the
  tombstone/cross-package class of attack (§8(b)) but **not sufficient** for the reported escalation —
  even a key genuinely owned by the named package can have its `operationType` rewritten to something
  dangerous by an attacker who legitimately holds `UpgradePackage` for *that* package (the grounding
  concretely walks this in §2's "exact attack" bullet: naming the *correct* installed package still
  succeeds against the manifest-ownership check alone). The field-level guard is the fix that actually
  closes the reported channel; manifest-ownership is a **different, additional** guarantee (closes a
  different attack — forging on someone else's key), correctly scoped to §8(b) instead of bolted on here
  under schedule pressure.
- **Deny `consoleOperator` the `UpgradePackage`/`UninstallPackage` grants entirely, require the kernel
  `operator` role for all package lifecycle ops.** Rejected: breaks the entire intended product surface
  (a console operator managing packages through Loupe without holding kernel `operator`) for a problem
  that has a narrower, mechanism-level fix; also does not close the gap for `operator` itself, which
  legitimately still shouldn't be able to forge grant escalations even though it is more trusted — the
  invariant should hold structurally, not by narrowing who holds a legitimate grant.

## 12. Test strategy summary

Unit-only, no e2e/ephemeral-stack needed — this is a pure commit-time Go guard over data already in the
step-8 code path. Build against `step8_commit_test.go`'s existing fixture/harness conventions (its own
17 `Test*` functions are the precedent for how this file constructs a `CommitterImpl` + mutation batch);
per §10 step 4, no sibling test for `rejectProtectedMutations` exists to mirror directly, so this is the
first unit test for this class of step-8 guard in the file.

## 13. Build note (Steward, 2026-08-14/15) — shipped shape differs from §4.1/§10, five review findings closed

Built in an isolated worktree (`internal/processor/{step8_commit.go,step8_commit_test.go,commit_path.go,
envelope.go}`, `internal/processor/opwire/opwire.go`; +365 lines, no other package touched). §10's decomposition
and §5's census both held as scoped. **Full 3-layer cold adversarial review (per §10's mandated review depth)
found one critical bypass and four lesser gaps in the first pass; all five closed and independently
mutation-tested before merge** — the shipped guard is materially wider than §4.1's sketch:

- **Role guard covers the whole root, not just `.canonicalName`** (§2's correction above) — closes a
  full kernel-root bypass a cold reviewer found live: an `UpgradePackage` holder writing a top-level
  `canonicalName` field onto their own held role's **root** shadows the real aspect in every cypher read
  (`resolveProperty` reads the root body before falling back to an aspect point-read), reaching the same
  kernel root-grant + wildcard read-grant lenses `.canonicalName` itself feeds. §4.1's aspect-only sketch
  did not close this; the shipped guard makes the role root byte-identical to its stored body
  (`isDeleted` excluded — that flag is the tombstone path's concern, not this guard's) rather than
  special-casing one field, since no legitimate path writes the root at all.
- **`data.lanes` joined the write-once set** (`permissionProvenanceFields`), compared as an
  order-independent set (`platformLaneGate` tests membership, `step3_auth_capability.go:553-574`) rather
  than freely mutable as §4.1/§6 originally said. `lanes` is authorization-consumed, not audit-only like
  `note`: `privilegedLaneAllowlist` sanctions exactly the `meta` lane for the three package-lifecycle ops
  this design's own threat model centers on, so a `lanes` rewrite on a surviving permission is the same
  escalation shape as the four guarded fields, closed the same way. Live census
  (`go test ./internal/pkgmgr/... -run TestUpgrade`) confirms no shipped package's upgrade path changes
  `lanes` on a surviving key. **Known, accepted consequence:** there is now no way to *add* a `Lanes`
  declaration to an existing permission short of a key-changing bump (absent→present is a presence
  change, and presence-change is exactly what closes the widening attack) — consistent with how every
  other guarded field already works (a real change mints a new key), not a new restriction class.
- **`origin`/`declaredBy` are healable when absent from the stored document** (`healableProvenanceFields`)
  — `internal/pkgmgr/build.go`'s `2666acd1` (landed the same day as this fire) started stamping both
  fields on permissions that could already be committed without them, so the very first upgrade of any
  pre-`2666acd1` package would otherwise be rejected by its own protection. Safe narrowly: runtime-minting
  (`grant-provenance-runtime-permission-minting-design.md`, `f464c7a5`) is itself new as of today, so no
  permission stored without an `origin` value could have been runtime-minted — an absent-origin heal can
  only ever assign what has always been true, never launder a genuinely-runtime grant. `operationType`/
  `scope` are not healable — a permission's key is derived from them, so a stored permission always
  carries both.
- **A `tombstone` mutation carrying a document is now held to the same comparison as an `update`.**
  `buildMutationValue` seeds the written value from the stored document and overlays the mutation's
  document on top — for `update` that's the whole point, but for a `tombstone` carrying its own document
  it means a rewrite could ride a delete-then-revive pair, one op each. Every current script drops the
  document on a permission/role tombstone (`starlark_runner.go` rejects a tombstone that carries one), so
  this was not reachable through any live path, but the guard is documented as path-independent like
  `rejectProtectedMutations` — this closes it at the guard's own layer instead of resting on a
  Starlark-layer check staying in place forever.
- **A stored document that exists but fails to decode is now rejected, not treated as absent.**
  `readPriorDoc` deliberately keeps an undecodable entry rather than failing the read (so one corrupt
  value can't wedge unrelated commits); the original sketch's `prior.doc()` accessor collapsed that state
  into the same "nil → allow" branch as "key never existed." `priorDocs.lookup()` now separates
  found/decoded, and this guard — unlike the availability-first `rejectProtectedMutations` it sits beside
  — treats present-but-undecodable as protected. Not reachable today (every writer marshals a proper map).

**Two adjacent gaps found during review, deliberately not built here** (same class as §8's non-goals — a
different mechanism than a field-level write-once guard, filed as new `lattice.md` rows rather than
bolted on): a tombstoned `grantedBy` **link** can still be revived via a direct `update` with
`isDeleted:false` (client-side `diffManifest` exclusion, no server-side backstop — the link-topology
mirror of what this design fixed for permission/role vertex fields); and `vtx.roleindex.<id>` (the
name→role lookup index) is the same class of unguarded surface one layer down, but has zero live readers
today (only `build.go` writes it, `cmd/loupe/pkg.go` reads it for display only) — not exploitable, filed
for when a consumer lands.

§6's contract text is corrected to match (role-root scope, `lanes`, the healable carve-out, tombstone
coverage) in the same uncommitted edit to `docs/contracts/08-package-install.md`.

## 14. Fire brief — grantedBy link revival guard (Steward, 2026-08-15)

**NOT SHIPPED — this fire's design was wrong; see §15 for what three cold reviews found and what to do
instead.** Left in place (not deleted) because the wrong turn is itself the useful artifact for whoever
designs the real fix — do not build the sketch below as written.

**Scope sentence (verbatim from the board row, `lattice.md` Security & trust boundary):** "A tombstoned
`grantedBy` link can be revived by a direct `update` with `isDeleted:false` — `diffManifest`'s
already-tombstoned-skip is client-side only; an `UpgradePackage` holder crafting the envelope directly
restores a specifically-revoked grant with no server-side backstop — the link-topology mirror of the
vertex-field guard `permission-role-provenance-write-once-design.md` just shipped." (§13 above named this
the same gap.)

**Verified touch-list:**
- `internal/processor/step8_commit.go` — new sibling guard beside `rejectPermissionRoleRewrites`
  (`step8_commit.go:736`), wired into `Commit` beside its call at `step8_commit.go:201`. Reuses
  `PermissionProvenanceError`/`ErrCodePermissionProvenance` (`step8_commit.go:66`,
  `opwire/opwire.go:183`) — no new error type, no new wire code.
- `internal/processor/step8_commit_test.go` — new test beside `TestCommit_PermissionRoleProvenanceIsWriteOnce`
  (`step8_commit_test.go:730`), mirroring its table-driven shape; use `newTestEnvelope`
  (`integration_test.go:160`) and override `env.OperationType`.
- `docs/contracts/08-package-install.md` §8.4 — extend the just-staged-uncommitted provenance-guard
  paragraph with this guard (same uncommitted edit, not a new one — see the file's current diff).

**Design decision (mine, implementation-level — §0 decide-don't-defer; not routed to Andrew or the
Designer, since it extends the shipped guard's own mechanism rather than inventing a new one):**

The vertex guard's "real change ⇒ new key" invariant does **not** hold for `grantedBy` links: a link key is
content-addressed on `(permission, role)` (`lnk.permission.<permID>.grantedBy.role.<roleID>`,
`internal/pkgmgr/build.go:387`), so re-granting the *same* permission to the *same* role after a revoke
**necessarily** reuses the same key and **must** be an `update` (a `create` asserts revision 0 and
RevisionConflicts forever against the tombstone — `packages/rbac-domain/ddls.go:150-158`'s own comment).
So "reject every update reviving a tombstoned grantedBy link" would brick the legitimate
`GrantPermission` re-grant path (`packages/rbac-domain/ddls.go:399-421`, `grant_link`/`revive_link`), not
just the attack.

The distinguishing signal is **which operation is running**, read off `env.OperationType`
(`opwire/opwire.go:130`) — already an established pattern for this exact class of policy
(`privilegedLaneAllowlist`, `step3_auth_capability.go:426`, a `map[operationType]...` core-owned allowlist;
also the `env.OperationType == "ClaimIdentity"` branch at `commit_path.go:428`). `env.OperationType` is
trustworthy at step 8: it selects which server-side script/handler actually ran, not a client-asserted
flag the mutation body could forge. Only `"GrantPermission"` (`packages/rbac-domain/ddls.go:399`) may
revive a tombstoned `grantedBy` link; every other path — chiefly `UpgradePackage`, whose honest client
(`internal/pkgmgr/upgrade.go:393-417`, commit `0bb6daea`) already skips reviving exactly this key class —
is rejected.

Confirmed this can't reach `InstallPackage` (fresh installs emit `create`, which conflicts on any existing
revision, tombstoned or not — never reaches the `update` branch) or `UninstallPackage` (emits `tombstone`,
which `buildMutationValue` forces `isDeleted:true` regardless of the mutation's document —
`step8_commit.go:463-466` — so a tombstone mutation can never revive). Only `update` is in play, on
`UpgradePackage`'s diff path.

**Scope boundary — `grantedBy` only, not `holdsRole`:** `0bb6daea`'s client-side skip is scoped to
"grant/role topology" reachable from a package **manifest** — `vtx.permission.*`, `vtx.role.*`,
`lnk.permission.*.grantedBy.role.*` (`upgrade.go:400-405`) — never `holdsRole` (identity→role grants are
runtime-minted, never package-declared, so `UpgradePackage`'s manifest diff never touches one). The
already-filed, separate, `📐 needs designer pass` row "`UpgradePackage`/`UninstallPackage` mutations
aren't scoped to the named package's own manifest" covers the broader "attacker declares an unrelated key
in a crafted manifest" class — out of scope here; this fire's guard is narrowly the `grantedBy`
revival-via-update vector §13 named, matching `0bb6daea`'s own ratified scope exactly.

**The guard (shape):**
```go
// grantedByLinkRevivalAllowlist is the core-owned policy of which operationType
// may revive a tombstoned lnk.permission.<id>.grantedBy.role.<id> link via an
// update that sets isDeleted back to false/absent. GrantPermission's own
// grant_link/revive_link (packages/rbac-domain/ddls.go) is the only legitimate
// revive path — re-granting a permission to a role after a RevokePermission
// necessarily reuses the same content-addressed key (a create RevisionConflicts
// against the standing tombstone), so this cannot be a blanket reject the way
// the vertex-field guard is. UpgradePackage's honest client already skips
// reviving this exact key class (internal/pkgmgr/upgrade.go, 0bb6daea); an
// UpgradePackage holder crafting the update directly is exactly what this closes.
var grantedByLinkRevivalAllowlist = map[string]bool{"GrantPermission": true}

// rejectGrantedByLinkRevival is the server-side, path-independent mirror of
// diffManifest's client-side already-tombstoned-skip (0bb6daea), for the one
// mutation shape that skip cannot reach: an update submitted directly, bypassing
// pkgmgr's diff computation entirely. Fires only for lnk.permission.<id>.grantedBy.role.<id>
// keys whose stored document is isDeleted:true and whose mutation document does
// not also assert isDeleted:true — i.e. an attempted revive.
func rejectGrantedByLinkRevival(mutations []MutationOp, prior priorDocs, operationType string) error {
	if grantedByLinkRevivalAllowlist[operationType] {
		return nil
	}
	for _, m := range mutations {
		if m.Op != "update" {
			continue
		}
		type1, _, linkName, type2, _, ok := keys.ParseLinkKey(m.Key)
		if !ok || type1 != "permission" || linkName != "grantedBy" || type2 != "role" {
			continue
		}
		priorDoc, found, decoded := prior.lookup(m.Key)
		if !found {
			continue
		}
		if !decoded {
			return &PermissionProvenanceError{Key: m.Key, Field: "isDeleted"}
		}
		wasDeleted, _ := priorDoc["isDeleted"].(bool)
		if !wasDeleted {
			continue
		}
		nowDeleted, _ := m.Document["isDeleted"].(bool)
		if !nowDeleted {
			return &PermissionProvenanceError{Key: m.Key, Field: "isDeleted"}
		}
	}
	return nil
}
```
Wire it into `Commit` right after the `rejectPermissionRoleRewrites` call (`step8_commit.go:201`), passing
`env.OperationType`. `keys.ParseLinkKey` is already imported (`step8_commit.go:16`,
`keys.IsVertexKeyOfType`/`keys.ParseAspectKey` already used at `step8_commit.go:749,796`) — no new import.

**Increment order (single increment, S–M):**
1. Add `grantedByLinkRevivalAllowlist` + `rejectGrantedByLinkRevival` to `step8_commit.go`; wire the call.
   Green check: `go build ./internal/processor/...`.
2. Table-driven test beside `TestCommit_PermissionRoleProvenanceIsWriteOnce`: (a) tombstoned link + `update`
   with `isDeleted:false` under a non-`GrantPermission` `OperationType` → `*PermissionProvenanceError`; (b)
   same under `OperationType:"GrantPermission"` → succeeds (proves the legitimate re-grant path is not
   bricked — standing checklist #3, positive vector before the negative); (c) a **live** link (not
   tombstoned) updated by any op → unaffected (guard is scoped to the revival transition only); (d) a
   `create` for a never-existing link → unaffected. Green check:
   `go test ./internal/processor/... -run TestCommit_GrantedByLinkRevival -v`.
3. Extend the already-uncommitted `docs/contracts/08-package-install.md` §8.4 paragraph (the one this
   design's §13 build note staged) with a sentence naming this guard, mirroring its existing structure.
   Green check: `git diff docs/contracts/08-package-install.md` reads as one coherent addendum, not a
   second disconnected proposal.
4. Full suite for the touched package: `go test ./internal/processor/...`.

**In-scope gotchas (standing checklist + dossier entries copied in):**
- Standing checklist #3 — the positive vector (increment 2b) must be proven **before** the negative is
  trusted; a guard tested only by its own rejection can pass vacuously.
- Standing checklist #5 — one deterministic key (the link), now with two active writers in spirit
  (`GrantPermission`'s legitimate revive vs. this guard's reject-everyone-else) — the allowlist *is* the
  arbitration, decided here, not left implicit.
- `docs/components/processor.md` dossier: *"A tombstone retains the prior document, so a reader that does
  not filter `isDeleted` sees a revoked declaration as live"* — this guard IS such a reader; it must read
  `priorDoc["isDeleted"]` directly (not assume absence-means-false at the read site, only at the write
  site) since the whole point is distinguishing a stored `isDeleted:true` from a stored `isDeleted:false`.
- `docs/components/pkgmgr.md` dossier: *"Two writers of one deterministic key"* — directly this fire's
  shape; resolved by the `operationType` allowlist above, not left as an unspoken assumption.

**Adjacent finds:** none beyond the two already named in §13 (roleindex — separate filed row, out of
scope; the broader manifest-scoping gap — separate filed row, out of scope, cited above).

**Non-goals:** `holdsRole` links (never package-declared, not reachable via `UpgradePackage`'s manifest
diff — see scope boundary above); the CREATE-forgery and manifest-scoping gaps (separate filed rows,
§8/§13); any change to `grant_link`/`revive_link`'s Starlark logic (already correct — this fire adds a
server-side backstop beside it, not a change to it).

## 15. Why §14 was not shipped (Steward, 2026-08-15) — three cold reviews, one blocking regression

The build ran clean, gofmt/vet/lint-conventions/full-package-suite all green, and the guard's own 6-row test
table (positive vector first) passed. Full 3-layer adversarial review — a bypass hunt, an edge-case walk, and
an acceptance audit, all cold, all opus, none the implementer — then found the central premise false. Not
merged; worktree abandoned; the `docs/contracts/08-package-install.md` addendum reverted back to the
pre-fire staged state (the retention-class + permission/role-provenance paragraphs only).

**Blocking: `env.OperationType` cannot distinguish the attack from an already-legitimate `UpgradePackage`
path, because at step 8 they are byte-identical.** `internal/pkgmgr/upgrade.go`'s `diffManifest` has two
branches that can revive a tombstoned `grantedBy` link via `update`, not one:

- the **`survives`** branch (key in both old and new manifest sets) — `0bb6daea`'s already-tombstoned skip
  covers this one (`upgrade.go:393-417`), which is what §14's design reasoned from.
- the **`!survives`** branch (`upgrade.go:361-384`) — a key **newly declared** relative to the immediately
  prior version, but whose deterministic content-addressed slot KV already holds tombstoned, because an
  **earlier** version of the same package declared it, a later version dropped it (tombstoning it as part
  of that version's own `old \ new` diff), and this version re-adds it. This is legitimate, intentional,
  already covered by a pre-existing green test (`internal/pkgmgr/upgrade_test.go`,
  `TestUpgrade_ReAddsRemovedEntity`, in place since 2026-07-19) — and it is **not** protected by
  `0bb6daea`'s skip, which only guards the `survives` branch.

Both branches, run under `operationType:"UpgradePackage"`, emit the exact same shape at step 8: `update`,
same key class, prior `isDeleted:true` → new `isDeleted:false`. §14's guard rejected both, so it broke the
legitimate one (`go test ./internal/pkgmgr/... -run TestUpgrade_ReAddsRemovedEntity` fails on this branch,
passes on `main`) while closing the attacker's one. **The real distinguishing fact — was this tombstone an
explicit operator `RevokePermission`, or a package's own prior-version removal — is not present anywhere in
the mutation, the prior document, or `env.OperationType`.** It would need a provenance marker at the
tombstone write site (e.g. distinguishing `RevokePermission`'s tombstone from `UpgradePackage`'s own
`old \ new` tombstone) — a genuinely new mechanism with real edge cases of its own (what a stored document
with no such marker, predating the mechanism, means; whether the marker survives a further round-trip). No
ratified pattern to extend. **This is Designer-pass work, not a same-fire correction** (§2.5's test).

**Also found, not this item's to fix but load-bearing for the eventual design:**

- **A general step-6/8 validation gap: no mutation document field is type-checked against the shape its
  readers assume.** A `grantedBy`/`holdsRole` link `update` whose `isDeleted` is present but not a JSON
  bool (e.g. the string `"true"`) is invisible to every guard that reads it with a Go type assertion
  (`.(bool)` silently yields `false` on a type mismatch) — including this fire's guard, `readPriorDoc`, and
  `rejectPermissionRoleRewrites`. Worse: `internal/refractor`'s pipeline/adjacency consumers parse the SAME
  field with a strict typed decode and either DLQ or hard-fail. A live link written with a malformed
  `isDeleted` therefore reads as **live** to the Processor (so `RevokePermission`/`GrantPermission`, which
  declare the key as a required/optional read, fail permanently at hydration) while some Refractor read
  paths treat it as gone and others keep projecting it — a worse outcome than the revival this fire set out
  to close, and general to every mutation class, not just `grantedBy`. **Filed as its own row** (below) —
  mechanical (type-check `isDeleted`, and plausibly every boolean-typed provenance field, at commit time;
  fail closed on a type mismatch same as the existing `!decoded` branches already do for a whole document),
  not a Designer-pass item.
- **The `holdsRole` exclusion this design reasoned as safe ("runtime-minted, never package-declared") is
  true only of the honest `pkgmgr` client — `UpgradePackage`'s server-side script has no check binding a
  mutation's key to the named package's own manifest at all** (confirmed live:
  `internal/bootstrap/install_ddl.go` validates only `name`/`fromVersion`/`toVersion` are non-empty and the
  key has ≥3 dot-segments). A `create` of a fresh `lnk.identity.<attacker>.holdsRole.role.<operator>` link
  under `UpgradePackage` needs no revival at all and grants the kernel `operator` role outright — this is
  the **already-filed** "`UpgradePackage`/`UninstallPackage` mutations aren't scoped to the named package's
  own manifest" row (`lattice.md`, found 2026-08-14), now with a concrete repro confirming it dominates
  (in severity and reachability) everything this item set out to close. No new row; that one's urgency is
  what changed.
- **Minor, corrected in place, no lasting effect:** §14's guard comment and its (now-reverted) contract
  prose both claimed `env.OperationType` "selects which server-side script actually ran." False — `class`
  (client-supplied, highest precedence, `step4_hydrate.go` `resolveClass`) selects the script; nothing
  binds `operationType` to the script DDL's `permittedCommands`. Not exploitable by the design's own
  threat actor (`GrantPermission` is granted only to `operator`, who already holds `UpgradePackage`), so
  this was a wrong justification for a control that happens to hold for an unrelated reason (step-3 auth
  gates `GrantPermission` itself) — but any future design leaning on "`OperationType` proves which script
  ran" is building on sand and must re-derive the actual guarantee.

**For the next Designer pass:** the real fix likely needs one of — (a) a provenance marker on a tombstone
distinguishing "explicit revoke" from "package-diff removal", written at both `RevokePermission`'s tombstone
and (going forward) never at `UpgradePackage`'s `old \ new` tombstone, checked by the revival guard; or (b)
folding this into the manifest-scoping design (the `!survives` revive is legitimate exactly when the
reviving `UpgradePackage` call is honestly diffing against the named package's own manifest — which is the
same authenticity question that design already has to answer for creates and tombstones). (b) may be the
more coherent shape: one mechanism that verifies an `UpgradePackage` mutation batch against the named
package's real compiled Definition would close the create-forgery gap, the manifest-scoping gap, AND this
revival gap at once, rather than three separate point guards. Worth deciding which before designing either.
