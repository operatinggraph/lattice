# Story 13.3 — Bridge service actor + bootstrap provisioning

**Status:** done
**Epic:** 13 — External I/O Bridge (orchestration core)
**Tier:** Opus — **kernel topology / security plane**. This story mutates the primordial bootstrap set (a third root-equivalent service actor) + both kernel-verify enumerations + the bootstrap-file version + the readiness gate. Review: full 3-layer adversarial + `make verify-kernel` + **`make down && make up`**.
**Epic spec:** `_bmad-output/planning-artifacts/epics/phase-2-epics.md` → "Story 13.3: Bridge service actor + bootstrap provisioning" (lines ~605–619) + the Epic 13 framing (~565–572). Read it for the user-story framing and the three ACs.
**Binding grounding (FROZEN — read, build TO, do NOT edit):** `docs/contracts/07-primordial-bootstrap.md` — the WHOLE contract is in scope (this story IS kernel topology), especially §7.1 (Bootstrap Principle — auth traces to graph topology, no direct `cap.*` seeding), §7.2 item 7/8 + the parenthetical at line 64 ("Additional internal service actor identities for Loom, Weaver, etc. … following the same pattern"), and §7.5 (the readiness gate — `make up` does NOT complete until the actor's `cap.*` projection exists). Contract #1 §1.1 (key shapes — `vtx.identity.<NanoID>`, `lnk.identity.<id>.holdsRole.role.<id>`).
**Component doc of record (you MAY edit this — it is a `/docs` component page):** `docs/components/service-actors.md` — the service-actor model. You add the bridge to its tables/prose **in the same change** as the code; drift is a documentation bug (the page says so, line ~8).
**Depends on:** 13.1 (DONE — the surface is frozen). **Parallels 13.2** (both depend only on 13.1; 13.2 is DONE). **Feeds 13.4** (the bridge component posts result-ops under the identity this story provisions). This story does NOT build the bridge component.
**Workflow:** you are the DS (dev) sub-agent. Repo root, no worktree. Do **NOT** commit/push/branch. Do **NOT** edit frozen contracts (`docs/contracts/*`) or planning artifacts (`epics/*.md`, `lattice-architecture.md`). You MAY edit `docs/components/service-actors.md` (a `/docs` component page). A genuine frozen-contract gap → `cmd/bootstrap/CONTRACT-AMENDMENT-REQUEST.md` + flag at the TOP of your closing summary; do **not** edit the contract. Leave all changes in the working tree for Winston.

---

## 0. THE HEADLINE — LOCKSTEP OR IT BREAKS (read this first; it governs everything below)

> **No CONTRACT-AMENDMENT-REQUEST is anticipated.** §7.2 line 64 explicitly authorizes additional internal service-actor identities "following the same pattern" — this story exercises exactly that. If you find a genuine gap, flag it; do not edit the contract.

This story adds **one** new primordial identity (`identity.system.bridge`) + **one** new `holdsRole → operator` link. That is conceptually tiny. The ENTIRE risk is that the primordial set is **counted, enumerated, version-checked, and asserted in multiple files that must all change together**. A single straggler — a stale count, one verify enumeration updated but not the other, a missing readiness-gate entry — is the predictable failure mode and is exactly what the 3-layer review and `make down && make up` exist to catch.

**The complete lockstep site list (every one MUST change in this story — file:line as of authoring):**

| # | Site | What changes |
|---|------|--------------|
| 1 | `internal/bootstrap/nanoid.go` `var (...)` block (~78–81, 134–135) | Add `BridgeIdentityID`/`BridgeIdentityKey` vars + `BridgeHoldsRoleLinkKey` var |
| 2 | `internal/bootstrap/nanoid.go` `PrimordialIDsRaw` struct (~142–168) | Add `BridgeIdentity string \`json:"bridgeIdentity"\`` |
| 3 | `internal/bootstrap/nanoid.go` `BootstrapFile` version history doc (~170–186) + `persistWithStatus` `Version:` literal (~453) | Add the `"8"` history line; bump the persisted `Version` literal `"7"` → `"8"` |
| 4 | `internal/bootstrap/nanoid.go` `checkVersion` (~320–330) | `case "7"` → `case "8"`; update the `want \"7\"` message → `want \"8\"` and the doc comment (~314–319) |
| 5 | `internal/bootstrap/nanoid.go` `currentRaw()` (~273–295) | Add `BridgeIdentity: BridgeIdentityID,` |
| 6 | `internal/bootstrap/nanoid.go` `generate()` targets (~332–363) | Add `&raw.BridgeIdentity` to the `targets` slice |
| 7 | `internal/bootstrap/nanoid.go` `populate()` validation `fields` (~365–395) + assignment (~397–447) | Add `{"bridgeIdentity", raw.BridgeIdentity}` to the validate list; assign `BridgeIdentityID`, derive `BridgeIdentityKey = "vtx.identity." + BridgeIdentityID` and `BridgeHoldsRoleLinkKey = "lnk.identity." + BridgeIdentityID + ".holdsRole.role." + RoleOperatorID` |
| 8 | `internal/bootstrap/nanoid.go` `PrimordialVertexKeys()` (~471–512) | Add `BridgeIdentityKey` (with the service-actor identities) + `BridgeHoldsRoleLinkKey` (with the service-actor holdsRole links) |
| 9 | `internal/bootstrap/nanoid.go` `PrimordialVertexKeyCount` const + doc (~514–521) | `27` → `29` (1 identity + 1 link); update the breakdown comment ("2 service actors" → "3", "3 holdsRole" → "4", "8 links" → "9") |
| 10 | `internal/bootstrap/primordial.go` `buildPrimordialEntries` — identity provisioning (~337–365) | Add the `MakeVertexEnvelope(BridgeIdentityKey, "identity.system.bridge", {protected:true, note:...})` block, mirroring Loom/Weaver exactly |
| 11 | `internal/bootstrap/primordial.go` `buildPrimordialEntries` — holdsRole links (~610–633) | Add the `MakeLinkEnvelope(BridgeHoldsRoleLinkKey, "vtx.identity."+BridgeIdentityID, "vtx.role."+RoleOperatorID, "holdsRole", ...)` block |
| 12 | `internal/bootstrap/primordial.go` `WaitForBootstrapComplete` `capProjections` slice (~1010–1016) | Add `{"bridge", capabilityKeyForIdentity(BridgeIdentityID)}` — **THE READINESS GATE (AC #2 / Contract #7 §7.5)** |
| 13 | `internal/bootstrap/primordial.go` kernel-composition doc comment (~189–213) | "2 internal service-actor identities (Loom + Weaver…)" → "3 (Loom + Weaver + Bridge…)"; "2 service-actor→operator holdsRole links" → "3"; the `≈ 73` total nudges up (the exact tally below) |
| 14 | `scripts/verify-kernel.go` header enumeration (~8–27) | "2 internal service-actor identity vertices (Loom + Weaver, arch §92)" → "3 (… + Bridge)"; "2 service-actor → operator holdsRole links (Loom + Weaver)" → "3"; the `≈ 67 OK lines` / `~73 entries` tallies nudge up by 2 |
| 15 | `internal/bootstrap/verify.go` `VerifyKernel` | **No literal count lives here** — it iterates `PrimordialVertexKeys()` (site #8), so it picks up the bridge automatically. **Confirm by reading** that it has no separate hardcoded actor list; if it does, add the bridge. Its sibling `scripts/verify-kernel.go` ALSO iterates the same slice (it has no per-actor literal either) — but its **header comment** (#14) is hand-maintained and must be updated. The lockstep is the **count constant** (#9) + the **two header comments** (#13, #14), not per-actor assertions. |
| 16 | `lattice.bootstrap.json` (checked-in) | **Regenerated, not hand-edited** — see § "How `lattice.bootstrap.json` is produced" below. After `make down && make up`, this file is rewritten with `version: "8"` + a fresh `bridgeIdentity` NanoID (and fresh NanoIDs for every other key). Commit the regenerated file (Winston commits; you leave it in the tree). |
| 17 | `Makefile` `up:` echo (~39) + `verify-kernel:` count comment (~61) | The `up` echo says "blocks until admin + Loom + Weaver cap.* projections land" → add Bridge. The `verify-kernel` header comment says "Expected count ≈ 89 OK lines (28 top-level keys …)" → bump to 30 top-level keys / the new OK-line tally. **Cosmetic but in-scope** (drift is a review finding). |

**Acceptance step (an AC of this story — do it and show the output):** after your edits, run
`grep -rn -E "LoomIdentity(Key|ID)|WeaverIdentity(Key|ID)" --include="*.go" .`
and for **every** production (non-`_test.go`) site that names the Loom/Weaver identity in an **enumeration, count, or readiness list**, prove there is a parallel Bridge entry. Then `grep -rn -E "\b27\b|\b73\b|\b67\b" internal/bootstrap scripts` and prove no stale OLD count survives. A drift between the two verify enumerations or a stale count is the failure this story is most likely to ship — the grep is the gate.

---

## 0a. WHAT DOES *NOT* CHANGE (the de-risking finding — read before you over-reach)

The system is built so a new `protected:true` identity is **auto-discovered** by the auth + projection planes. You do **NOT** touch any of these:

- **`internal/bootstrap/system_actors.go` `SystemActorKeys`** — discovers system actors at runtime by **predicate** (`identity`-type vertex with `data.protected = true`), scanning core-kv. The bridge identity, being `protected:true`, is picked up automatically. **Do not add a hardcoded bridge entry here — there is no list.** (Read it to confirm: ~26–65.)
- **The Capability-Lens primordial-identity anchor cypher** (`internal/bootstrap/lenses.go` `CapabilityLensDefinition`, ~38–53) — projects root grants for kernel-seeded protected identities by the **same predicate**, not a hardcoded actor list. The bridge's `cap.identity.<id>` is produced by the Refractor walking `holdsRole → operator → grantedBy → permission`, identical to Loom/Weaver. **No cypher change.** (Confirm the prose at lenses.go ~42–52 says "the kernel-seeded system identities" via the protected predicate — if it instead names "Loom + Weaver" as a hardcoded set the lens depends on, escalate; based on the read it is predicate-driven and needs no change, but the prose comment naming Loom+Weaver may want "+ Bridge" for accuracy — treat that as a doc-comment touch, not a logic change.)
- **`internal/processor/step3_auth*.go` / `classAwarePlatformKey`** — routes system actors to their core `cap.<actor>` doc using the **runtime-discovered `SystemActorKeys`** (`cmd/processor/main.go` ~112 passes the discovered set), not a literal. The bridge flows through automatically. **No processor change.** (Comments at step3_auth.go ~151, step3_auth_matcher.go ~154 say "admin + Loom + Weaver" descriptively — a stale-prose nit, NOT a logic site. You MAY leave them; if you touch them, only the comment.)
- **`internal/refractor/*`** — the projection plane anchors on `substrate.ParseVertexKey` returning the `identity` type segment, never the `class`; the `identity.system.bridge` class is inert for capability (Contract #7 §7.7, service-actors.md ~35–52). **No Refractor change.**
- **NO new role / permission / grantedBy link / cypher branch / step-3 code.** Root-equivalence is **purely** the single `holdsRole → operator` edge reusing the existing operator role's `scope:"any"` permissions. This is the whole point of AC #1 and the established Loom/Weaver pattern.
- **NO `internal/bridge/` or `cmd/bridge/`** — that is **Story 13.4**. This story is bootstrap + verify + readiness + one doc.
- **NO Loom engine change.** 13.2 is done and independent.
- **NO `cmd/loom/main.go` / `cmd/weaver/main.go` equivalent for the bridge.** Those wire the engine's `actorKey` (loom/main.go ~64, weaver/main.go ~80). The bridge's `cmd/bridge/main.go` that reads `bootstrap.BridgeIdentityKey` is **13.4** — do not create it here. (You only export the `BridgeIdentityKey` constant; 13.4 consumes it.)

If a review layer flags "you didn't update the cypher / step-3 / SystemActorKeys," the answer is the predicate-driven design above — cite it. The bridge is *deliberately* auto-discovered; the only hand-edited surfaces are the bootstrap inventory + the version + the readiness `capProjections` list + the verify/Makefile count-comments + the doc page.

---

## 1. ADJUDICATION — what 13.3 delivers (DS builds to THIS)

### Scope boundary

**In scope:**
1. Provision `identity.system.bridge` in the primordial bootstrap — `protected:true`, root-equivalent **purely** via a `holdsRole → operator` link, deterministic NanoID persisted to `lattice.bootstrap.json`. Mirror the Loom/Weaver identity shape EXACTLY (class `identity.system.bridge`, the same `protected:true` + `note` pattern, the same atomic batch).
2. Bump the bootstrap-file `version` `"7"` → `"8"` (so a stale file is rejected by `checkVersion` with the `make down && make up` guidance) and add `bridgeIdentity` to the persisted `PrimordialIDsRaw`.
3. Add the bridge identity + its `holdsRole` link to **both** kernel-verify enumerations **in lockstep** — via the shared `PrimordialVertexKeys()` slice (which both `internal/bootstrap/verify.go` and `scripts/verify-kernel.go` iterate) and the `PrimordialVertexKeyCount` constant (which `scripts/verify-kernel.go` cross-checks against the slice length, ~94). Update the two hand-maintained header/count comments.
4. Add the bridge's `cap.identity.<id>` projection to the `make up` readiness gate — the `capProjections` slice in `WaitForBootstrapComplete` (`primordial.go` ~1012–1016). This is the AC #2 / Contract #7 §7.5 proof: `make up` must not return ready until the bridge is authorizable.
5. Document the `system`-lane carry/deferral for the bridge in `docs/components/service-actors.md` (mirror the Loom/Weaver note), plus add the bridge to the page's "What is provisioned" table, the version-bump prose, and the readiness-gate prose.
6. The lockstep acceptance grep (§0) + the regenerated `lattice.bootstrap.json`.

**Out of scope (do NOT build — later/other stories):**
- The **bridge component** (`internal/bridge/`, the durable consumer on `events.external.>`, the adapter registry, the moved `Fake*` adapters, the FR58 crash/retry proof) → **Story 13.4**.
- `cmd/bridge/main.go` (the engine entrypoint that reads `BridgeIdentityKey`) → **13.4**.
- Any new role / permission / grantedBy link / cypher / step-3 code → forbidden by AC #1 (root-equivalence is the `holdsRole → operator` edge only).
- Any Loom engine change.
- `system`-lane **enforcement** — it is `deferred` (Contract #2 §2.3); you only **document** the carry, identically to Loom/Weaver (service-actors.md ~72–80).

### Item-by-item

**Item 1 — Bridge identity vertex (`primordial.go` `buildPrimordialEntries`).**
Mirror the Loom/Weaver block at ~354–365. Add (placed adjacent, in the same "2a. Internal service-actor identities" region):
```go
bridgeIDVal, bridgeIDErr := MakeVertexEnvelope(BridgeIdentityKey, "identity.system.bridge",
    map[string]any{"protected": true,
        "note": "Internal Bridge service-actor identity. Root-equivalent via holdsRole to the operator role."})
if err := add(BridgeIdentityKey, bridgeIDVal, bridgeIDErr); err != nil {
    return nil, err
}
```
- Class is `identity.system.bridge` (the descriptive marker; inert for capability — Contract #7 §7.7).
- `protected: true` — a package uninstall must never tombstone a kernel service actor (Contract #3 §3.4 protected-key guardrail).
- The `note` mirrors Loom/Weaver wording. **No `.state` aspect** (state is identity-domain-package territory — the kernel service actors carry no state aspect; see the Loom/Weaver comment ~327–344). **No key material** on the vertex (the "signing key" is a deferred NATS transport credential, not graph material — service-actors.md ~54–70; do not add anything that looks like load-bearing crypto).
- Update the kernel-composition doc comment (~194–211) in the same edit: "2 internal service-actor identities (Loom + Weaver…)" → "3 (Loom + Weaver + Bridge…)", and the holdsRole-links line "2 service-actor→operator holdsRole links" → "3".

**Item 2 — Bridge `holdsRole → operator` link (`primordial.go`).**
Mirror the Loom/Weaver `holdsRole` block at ~618–633. Add (in the same "10a. Service-actor → operator holdsRole links" region):
```go
bridgeHoldsVal, bridgeHoldsErr := MakeLinkEnvelope(
    BridgeHoldsRoleLinkKey,
    "vtx.identity."+BridgeIdentityID,
    "vtx.role."+RoleOperatorID,
    "holdsRole", "holdsRole", map[string]any{})
if err := add(BridgeHoldsRoleLinkKey, bridgeHoldsVal, bridgeHoldsErr); err != nil {
    return nil, err
}
```
- Direction follows Contract #1 §1.1: the **identity is the source** (later-arriving), the operator role is the target. Reads "bridge holdsRole operator". The key shape is `lnk.identity.<bridgeId>.holdsRole.role.<operatorId>` (6-segment link key — Contract #1 §1.1; CLAUDE.md key-shape rule). This single edge is the SOLE source of the bridge's root-equivalent capability — the Capability Lens walks `holdsRole → operator → grantedBy → permission` and projects the operator's `scope:"any"` permissions into `cap.identity.<bridgeId>.platformPermissions[]`. No new role/permission/grantedBy/cypher (Contract #7 §7.7).

**Item 3 — NanoID plumbing (`nanoid.go`).** This is the bulk of the lockstep (sites #1–#9 above). Mirror **every** Loom/Weaver touch-point:
- `var (...)`: add `BridgeIdentityID string`, `BridgeIdentityKey string` (next to `LoomIdentityKey`/`WeaverIdentityKey`, ~78–81) and `BridgeHoldsRoleLinkKey string` (next to `LoomHoldsRoleLinkKey`/`WeaverHoldsRoleLinkKey`, ~133–135). Update the service-actor doc comment (~73–77) to name the bridge too.
- `PrimordialIDsRaw`: add `BridgeIdentity string \`json:"bridgeIdentity"\`` (next to `WeaverIdentity`, ~146).
- `currentRaw()`: add `BridgeIdentity: BridgeIdentityID,` (~278).
- `generate()`: add `&raw.BridgeIdentity` to `targets` (~339).
- `populate()`: add `{"bridgeIdentity", raw.BridgeIdentity}` to the validate `fields` (~374); assign `BridgeIdentityID = raw.BridgeIdentity` (~400); derive `BridgeIdentityKey = "vtx.identity." + BridgeIdentityID` (next to `LoomIdentityKey`/`WeaverIdentityKey`, ~435–436) and `BridgeHoldsRoleLinkKey = "lnk.identity." + BridgeIdentityID + ".holdsRole.role." + RoleOperatorID` (next to the Loom/Weaver link derivations, ~445–446).
- `PrimordialVertexKeys()`: add `BridgeIdentityKey` to the service-actor identities group (~477–479) and `BridgeHoldsRoleLinkKey` to the service-actor holdsRole-links group (~502–504). **Both** — the identity AND the link.
- `PrimordialVertexKeyCount`: `27` → `29` (one identity + one link). Update the breakdown comment (~516–520): "2 service actors (Loom + Weaver)" → "3 service actors (Loom + Weaver + Bridge)", "3 holdsRole" → "4 holdsRole", "8 links (5 grantedBy + 3 holdsRole)" → "9 links (5 grantedBy + 4 holdsRole)". **The arithmetic must close** — 1 op + 1 admin + **3** service actors + 1 meta-DDL + 2 install/uninstall DDLs + 1 lens + 1 role + 5 perms + **9** links + 5 aspect-type = **29**. (The script asserts `len(PrimordialVertexKeys()) == PrimordialVertexKeyCount` at ~94 — if your enumeration and constant disagree, `make verify-kernel` fails with `KERNEL KEY COUNT DRIFT`. Use that as your local check before Docker.)

**Item 4 — Version bump (`nanoid.go`).**
- `persistWithStatus` `Version: "7"` literal (~453) → `"8"`.
- `checkVersion` `case "7":` (~322) → `case "8":`; the error message `want \"7\"` (~326) → `want \"8\"`; the doc comment (~314–319) updated to describe version 8 ("Version 8 adds the Bridge service-actor identity NanoID; older files lack it and must be regenerated").
- `BootstrapFile` version-history doc (~170–186): append the `"8"` line ("Bridge internal service-actor identity NanoID added (Epic 13 — External I/O Bridge service actor)"). **Do NOT** rewrite history lines as change-narration in *code-logic* comments elsewhere — the version-history block in this struct doc is the sanctioned place for the version ledger (it already lists 1–7); extend it. Everywhere else, comments describe present behavior only (CLAUDE.md no-history rule).
- **Why the bump matters (AC #2):** an operator with a stale `version:"7"` file (no `bridgeIdentity`) must be hard-rejected by `checkVersion` with the `make down && make up` guidance, not silently run with a missing actor. Confirm the message routes to that guidance (it already does — ~325–328).

**Item 5 — Readiness gate (`primordial.go` `WaitForBootstrapComplete`).** **THE AC #2 CORE.**
The `capProjections` slice (~1012–1016) currently lists `{admin, loom, weaver}`. Add the bridge:
```go
capProjections := []struct{ actor, key string }{
    {"admin", capabilityKeyForIdentity(BootstrapIdentityID)},
    {"loom", capabilityKeyForIdentity(LoomIdentityID)},
    {"weaver", capabilityKeyForIdentity(WeaverIdentityID)},
    {"bridge", capabilityKeyForIdentity(BridgeIdentityID)},
}
```
- This is the live gate. `make up`'s second bootstrap pass (no `-skip-ready-wait`) calls `WaitForBootstrapComplete`, which blocks until **every** actor's `cap.identity.<id>` exists in capability-kv (produced by the Refractor walking the `holdsRole` topology). Adding the bridge means `make up` does not return ready until the bridge is authorizable — Contract #7 §7.5, AC #2 ("`make up` readiness-gate waits on its `cap.*` projection").
- Update the `WaitForBootstrapComplete` doc comment (~980–995) to name the bridge in the "every actor that must be able to submit ops at startup" list (it currently says "the primordial admin and the two internal service actors (Loom + Weaver)" → "… the three internal service actors (Loom + Weaver + Bridge)").
- The `{"bridge", ...}` actor label feeds the timeout diagnostic — a missing bridge projection times out with `readiness gate timed out waiting for cap.identity.<bridgeId>` (the named-key error path, ~1077). Good failure ergonomics; no extra code.
- **The `make up` Makefile target needs no logic change** — it already runs the two-pass bootstrap (seed `-skip-ready-wait`, start Refractor, then the gate pass; Makefile ~36–40). The gate widening is purely the Go slice above. (Update the Makefile *echo string* at ~39 for accuracy — cosmetic, in-scope.)

**Item 6 — Doc (`docs/components/service-actors.md`).** You MAY edit this page (it is `/docs`). Mirror Loom/Weaver throughout:
- **"What is provisioned" table (~17–20):** add a row `| \`vtx.identity.<bridgeId>\` | \`identity.system.bridge\` | \`lnk.identity.<bridgeId>.holdsRole.role.<operatorId>\` |`.
- **The prose under the table (~22–33):** the bridge is `protected:true`, NanoID persists to `lattice.bootstrap.json`, root-equivalent purely by the `holdsRole → operator` edge, no new role/permission/cypher — extend the existing "Both are…" / "The service actors add no new…" sentences to cover three actors (e.g. "All three are…").
- **"`system` lane — deferred" section (~72–80):** this is the AC #3 deliverable. The bridge gets the **same** deferral note as Loom/Weaver: the live projection hardcodes `lanes:["default"]`; when lane enforcement lands, the bridge's capability projection must include the `system` lane so it can submit result-ops to `ops.system.>`. Add the bridge to this note (the section is actor-agnostic prose today — make sure it reads as covering all three service actors, or add an explicit bridge sentence).
- **"Bootstrap-file version bumps" section (~94–101):** update the example "(e.g. the 5 → 6 bump that added the Loom/Weaver identity NanoIDs)" — you may add "the 7 → 8 bump that added the Bridge identity NanoID" as a second example of the same hard-mismatch behavior (describing present behavior, not narrating history of *this* change — it documents the version-ledger which is legitimately a ledger).
- **"Readiness gate" section (~82–92):** update "blocks until the admin, Loom, and Weaver `cap.*` projections all exist" → "… admin, Loom, Weaver, and Bridge …".
- **The page header status line (~3)** already says "Phase 2 — shipped (kernel topology)"; no change needed there beyond the content edits.
- Keep the page's "update in the same commit as `internal/bootstrap` changes" discipline (~6–8) — your edits land together.

### The new kernel tally (state it explicitly so review can check)

- **Top-level `PrimordialVertexKeys()`:** `27` → **`29`** (added: 1 bridge identity vertex + 1 bridge holdsRole link).
- **`PrimordialVertexKeyCount`:** `27` → **`29`**.
- **`scripts/verify-kernel.go` header `≈ 73 entries` / `≈ 67 OK lines`:** each nudges **+2** (the bridge identity vertex + its holdsRole link both produce one OK line; the bridge identity carries **no aspects** — service actors have none, exactly like Loom/Weaver, so no aspect OK lines are added). New ≈ 75 entries / ≈ 69 OK lines. (These are approximate header comments; the authoritative gate is the `len()==Count` assertion + per-key Get loop, not the header number. Keep the header roughly honest.)
- **`primordial.go` `≈ 73 Core KV entries`:** → ≈ 75.
- **`Makefile` `verify-kernel` comment `≈ 89 OK lines (28 top-level keys…)`:** → 30 top-level keys (it counts top-level + aspects + streams/buckets; bump the top-level from 28→30 and the OK total by 2). **Note the Makefile says "28 top-level keys" while `PrimordialVertexKeyCount` is 27** — that pre-existing off-by-one in the *comment* is not yours to chase beyond keeping your delta consistent; bump it by your +2 (28→30) and move on. (If you want to flag the pre-existing 27-vs-28 comment discrepancy, put it in Open Questions — do not expand scope to reconcile it.)

---

## 2. Required reading (DS does the deep reads; do not expect them pre-loaded)

- **FROZEN — read fully:** `docs/contracts/07-primordial-bootstrap.md` (the whole contract). Key anchors: §7.1 (Bootstrap Principle), §7.2 item 7/8 + line 64 (the "additional service actor identities … same pattern" sanction), §7.4 (idempotence / `make down && make up`), §7.5 (the readiness gate — the AC #2 contract). Contract #1 §1.1 for key shapes.
- **The component doc you edit:** `docs/components/service-actors.md` IN FULL — it is the template for the bridge (tables, prose, the `system`-lane deferral note, the version-bump note, the readiness-gate note, the Contract #7 §7.7 class-never-gates explanation). Your bridge additions mirror its Loom/Weaver content exactly.
- **The provisioning pattern you mirror (read IN FULL):**
  - `internal/bootstrap/primordial.go` — `buildPrimordialEntries` (the whole function ~309–636; the Loom/Weaver identity block ~337–365 + the holdsRole-links block ~610–633 are your templates), `WaitForBootstrapComplete` (~980–1097; the `capProjections` slice ~1012–1016 is the readiness gate), `capabilityKeyForIdentity` (~1099–1103), `MakeVertexEnvelope`/`MakeLinkEnvelope` usage. The kernel-composition doc comment ~189–213.
  - `internal/bootstrap/nanoid.go` IN FULL — every Loom/Weaver mention is a site you parallel: the `var` block (~67–136), `PrimordialIDsRaw` (~142–168), `BootstrapFile` version history (~170–192), `currentRaw` (~272–295), `checkVersion` (~314–330), `generate` (~332–363), `populate` (~365–449), `PrimordialVertexKeys` (~468–512), `PrimordialVertexKeyCount` (~514–521).
  - `internal/bootstrap/verify.go` — `VerifyKernel` (~17–190). **Confirm** it iterates `PrimordialVertexKeys()` (~34) and carries **no** separate hardcoded actor list (it does not, as of authoring) — so it picks up the bridge via site #8. The in-process verifier that parallels the script.
  - `scripts/verify-kernel.go` — the header enumeration (~8–27, hand-maintained — site #14), the `len()==Count` agreement check (~94), the per-key Get loop (~100–127). No per-actor literal; the header comment is the only hand-edited surface here besides the count it shares with `nanoid.go`.
- **The auto-discovery proof (read to confirm you DON'T touch them):** `internal/bootstrap/system_actors.go` `SystemActorKeys` (~26–65 — predicate-driven), `internal/bootstrap/lenses.go` `CapabilityLensDefinition` (~38–53 — predicate-driven anchor), `cmd/processor/main.go` (~112–122 — passes the runtime-discovered set), `internal/processor/step3_auth_matcher.go` `classAwarePlatformKey` (~150–165 — consumes the discovered set, no literal).
- **The `make up` / `make down` / `make verify-kernel` targets:** `Makefile` ~20–64 (the two-pass bootstrap, the JSON removal on `down`, the verify invocation).
- **The checked-in artifact:** `lattice.bootstrap.json` (current `version:"7"`, 19 NanoIDs, no `bridgeIdentity`).
- **The reference story (structure + the service-actor pattern in prose):** `_bmad-output/implementation-artifacts/story-13.2-loom-external-task.md` (DONE — the sibling), and `_bmad-output/implementation-artifacts/story-7.1-orchestration-base.md` (the original orchestration-base story, for the project's house structure).
- **The existing service-actor tests (your test templates):** `internal/bootstrap/service_actor_test.go` (~49–190 — the unit assertions on the Loom/Weaver identity class, protected flag, holdsRole link source/target, the protected-uninstall guard, the per-key presence map), `internal/bootstrap/service_actor_e2e_test.go` (~44–155 — the e2e that seeds, projects, and asserts the readiness gate is satisfied once admin+loom+weaver cap.* all exist), `internal/refractor/service_actor_projection_e2e_test.go` (~53–235 — the Refractor root-equivalence projection proof for Loom/Weaver). **These tests will need the bridge added** (they enumerate the Loom/Weaver keys) — see § 3.

---

## How `lattice.bootstrap.json` is produced (the dev MUST know this — do NOT hand-edit it)

**The file is generated at runtime, never authored by hand.** Mechanism (from `internal/bootstrap/nanoid.go` doc ~1–30 + `LoadOrGenerate` ~214–256 + the Makefile):

1. `make up` builds and runs `cmd/bootstrap`, which calls `bootstrap.LoadOrGenerate(path)`.
2. On a **clean** state (no file — `make down` deleted it, Makefile ~52–53), `LoadOrGenerate` sees no file, calls `generate()` (which mints a fresh NanoID via `substrate.NewNanoID()` for **every** target in the `targets` slice — including your new `&raw.BridgeIdentity`), `populate()`s the package vars, and persists the file with `status:"in-progress"`, then (after seeding succeeds) `PersistCommitted` rewrites it `status:"committed"`.
3. The persisted `Version` is the literal in `persistWithStatus` (~453) — which you bump to `"8"`.

**Therefore the dev's procedure to regenerate the checked-in `lattice.bootstrap.json` is `make down && make up`** (Docker required). `make down` removes the old file; `make up` regenerates it with `version:"8"` and a fresh `bridgeIdentity` NanoID (and, because every NanoID is per-deployment, fresh values for all 20 keys — that is by design, NFR-SC2 / FR48, not a problem). The regenerated file is left in the working tree for Winston to commit. **Do not hand-add a `bridgeIdentity` line to the JSON** — if you do, its NanoID will not match what `generate()` produces, and the next `make down && make up` overwrites it anyway; worse, a hand-edited file with the new field but `version:"7"` would be inconsistent. Let the generator own it.

**The bridge NanoID is derived exactly as Loom's/Weaver's:** `substrate.NewNanoID()` (a fresh random 20-char Contract #1 NanoID) at first `make up`, persisted under the `bridgeIdentity` JSON field, reused on warm re-runs, regenerated on the next clean `make up`. There is **no** "deterministic-from-a-seed" derivation for primordial IDs — they are per-deployment unique by construction (nanoid.go ~1–25). ("Deterministic" in AC #1 means *persisted-and-stable-across-restarts via the file*, not *computed from a constant* — confirm this reading in Open Questions Q1; it matches Loom/Weaver exactly and the contract §7.3.)

---

## 3. Test plan (concrete — count delivered tests from the diff)

The bridge is a third instance of the established service-actor pattern, so the tests EXTEND the existing Loom/Weaver tests rather than inventing new shapes. **Every place the existing tests enumerate Loom+Weaver, add Bridge** (these enumerations are themselves a lockstep surface — a test that still lists only 2 service actors after you add a 3rd is a stale-count signal).

**Unit (`internal/bootstrap`, no Docker — these run in CI on every push):**
- `internal/bootstrap/service_actor_test.go`:
  - Extend the class assertions (~60–61) with `{BridgeIdentityKey, "identity.system.bridge"}`.
  - Extend the holdsRole source/target assertions (~100–101) with `{BridgeHoldsRoleLinkKey, "vtx.identity." + BridgeIdentityID}` (source) → operator role (target). Assert the link reads "bridge holdsRole operator" (source = identity, target = role; Contract #1 §1.1).
  - Extend the protected-key / uninstall-guard maps (~133, 143, 160–163, 185–188) with the bridge identity + bridge holdsRole link.
  - Assert the bridge identity carries `protected:true` and **no `.state` aspect** (mirror the Loom/Weaver assertion).
- A `nanoid.go` count test (add if one exists; if not, the `make verify-kernel` `len()==Count` assertion covers it, but a pure-Go unit test is cheaper): assert `len(PrimordialVertexKeys()) == PrimordialVertexKeyCount == 29`, and that `PrimordialVertexKeys()` contains both `BridgeIdentityKey` and `BridgeHoldsRoleLinkKey`. Assert `BridgeIdentityKey == "vtx.identity." + BridgeIdentityID` and `BridgeHoldsRoleLinkKey == "lnk.identity." + BridgeIdentityID + ".holdsRole.role." + RoleOperatorID` after a `populate()` of a fixture raw with a valid bridge NanoID.
- A `checkVersion` test: `version:"8"` passes; `version:"7"` (and any other) fails with the `want "8"` / `make down && make up` message. (If a version test exists for "7", update it; add the "7 now rejected" case.)
- A `generate()`/`populate()` round-trip test: `generate()` produces a non-empty `BridgeIdentity`; round-trips through `currentRaw()` → JSON → `populate()` and the `BridgeIdentityKey` derives correctly. (Extend the existing round-trip test if present.)

**E2E (`internal/bootstrap` + `internal/refractor`, embedded NATS — see the existing service-actor e2e for the harness):**
- `internal/bootstrap/service_actor_e2e_test.go`: extend the seeded-key list (~72–75) with `BridgeIdentityKey` + `BridgeHoldsRoleLinkKey`; extend the projected-identity list (~124–125) with `bridgeID := bootstrap.BridgeIdentityID`. **Critically**, the readiness-gate assertion (~152 "gate must be ready once admin + loom + weaver cap.* all exist") must now require the **bridge** cap.* too — assert the gate is **NOT** satisfied until the bridge projection lands, and IS satisfied once all four exist. This is the unit-level proof of AC #2 (the `make down && make up` gate is the integration-level proof).
- `internal/refractor/service_actor_projection_e2e_test.go`: extend the holdsRole-edge builds (~172–173) + retouch (~193–194) + poll/assert (~215–216, 234–235) with the bridge — proving the Refractor projects `cap.identity.<bridgeId>` root-equivalent from the bridge's `holdsRole → operator` edge, identical to Loom/Weaver.

**Integration (the AC-mandated Docker proof — Winston runs, you run it too if Docker is available):**
- `make down && make up` — the AC #2 proof that the readiness gate actually **waits on the bridge's cap projection** and the kernel comes up clean with the new identity. This regenerates `lattice.bootstrap.json` (version 8 + bridge NanoID). `make up` must print "Lattice ready" only after all four cap.* projections (admin/loom/weaver/bridge) exist; if the bridge projection never lands, `make up` must time out naming `cap.identity.<bridgeId>` (not hang).
- `make verify-kernel` — asserts the 29 top-level keys (incl. the bridge identity + link) exist with correct envelopes, and the `len()==Count` agreement holds. Must print ALL ASSERTIONS PASSED.

If you judge any part unsafe to land in one pass, halt and report — but this story is a single tightly-coupled lockstep edit; a partial landing (e.g. count bumped but identity not provisioned, or readiness gate not widened) is strictly worse than not starting. Land it whole.

---

## 4. Verification gates (run before handing back; record each + result in the closing summary)

- `go build ./...`
- `make vet`
- `golangci-lint run ./...`
- `make verify-kernel` — **kernel topology changed; this is a primary gate.** Must show ALL ASSERTIONS PASSED with the new count (no `KERNEL KEY COUNT DRIFT`). Requires a running stack (`make up`).
- **`make down && make up`** — **AC-mandated, requires Docker.** Proves (a) the readiness gate waits on the bridge's `cap.*` projection (Contract #7 §7.5), (b) the kernel comes up clean with the new identity, (c) `lattice.bootstrap.json` regenerates at `version:"8"` with a `bridgeIdentity` NanoID. Capture the "Lattice ready" line + the regenerated JSON. If Docker is unavailable in your environment, run the embedded-NATS e2e (`go test ./internal/bootstrap/... ./internal/refractor/...`) which exercises the same readiness-gate logic, and **explicitly flag** that the Docker `make down && make up` is left for Winston as the AC-closing proof (do not claim AC #2 closed without it).
- `go test ./internal/bootstrap/... -count=1` (the unit + e2e bootstrap suite — the primary package) and `go test ./internal/refractor/... -count=1` (the projection e2e). Both must pass.
- **The lockstep grep (an AC):** paste the output of the two greps in §0 showing every Loom/Weaver enumeration/count/readiness site has a Bridge parallel and no stale `27`/old count survives.
- The full 3-layer adversarial review is Winston's gate (Blind Hunter / Edge Case Hunter / Acceptance Auditor) per `bmad-code-review` — this is **kernel topology / security plane**, so the full 3-layer is mandatory (not a lead-only review). Note it in your summary.

Flake retry per Deviation 14 is allowed; a flake claim without a re-run is a drift signal. The bootstrap/refractor unit+e2e packages use embedded NATS; only `make verify-kernel` and `make down && make up` need the Docker stack.

---

## 5. Closing summary (DS appends when done)

Deliverables vs § 1 checklist; the COMPLETE lockstep-site list with what changed at each (§0 table); exact files changed (`git status`); test count (from diff) + which existing tests you extended; the new kernel tally (29 top-level / count 29 / verify header); how you regenerated `lattice.bootstrap.json` (the `make down && make up` you ran, or the flag if you couldn't run Docker); every gate + result (anything not run + why); the lockstep grep output; any deviation; any new Open Question. **Confirm: no new role/permission/cypher/step-3/SystemActorKeys change** (root-equivalence is the `holdsRole → operator` edge only). **Do NOT commit.**

---

## Open Questions (saved for Winston / Andrew — none block the 13.3 build)

**Q1 — "Deterministic NanoID" wording (AC #1) = persisted-and-stable, NOT computed-from-a-seed.** AC #1 says the bridge identity has a "deterministic NanoID persisted to `lattice.bootstrap.json`." The established Loom/Weaver mechanism (which this story mirrors exactly) is: a **fresh random** `substrate.NewNanoID()` minted at first clean `make up`, **persisted** to the file, and **reused** on every warm restart (stable across restarts via the file). It is *not* derived from a constant seed — primordial IDs are deliberately per-deployment-unique (nanoid.go ~1–25; Contract #7 §7.3). "Deterministic" here reads as "stable/persisted," matching Contract #7 §7.3's "stable reference set across restarts." **Recommendation: keep the Loom/Weaver mechanism verbatim** (random-then-persisted). If "deterministic" was intended as "computed from a fixed seed so every deployment shares the bridge ID," that would CONTRADICT the per-deployment-uniqueness architecture and §7.3 — flagged here, not done. Does not block: the story implements the persisted-and-stable reading, identical to the two shipped service actors.

**Q2 — Pre-existing comment drift in the Makefile `verify-kernel` header.** `Makefile` ~61 says "Expected count ≈ 89 OK lines (28 top-level keys …)" but `PrimordialVertexKeyCount` is `27` (now `29`). The "28" was already inconsistent with the constant before this story. This story bumps the comment by its own +2 delta (28→30) for consistency but does **not** chase the pre-existing 27-vs-28 discrepancy (out of scope — a comment-only nit). Flag for a future cleanup if desired; not load-bearing (the authoritative gate is the `len()==Count` assertion, not the comment).

**Q3 — Stale descriptive prose naming "Loom + Weaver" in processor/aiagent comments.** Several **comments** (not logic) describe the system-actor set as "admin + Loom + Weaver" (`internal/processor/step3_auth.go` ~152, `step3_auth_matcher.go` ~154, `internal/processor/commit_path.go` ~632, `internal/aiagent/traversal.go` ~181, `internal/bootstrap/lenses.go` ~43, `internal/bootstrap/system_actors.go` ~17). These are **descriptive prose**, not enumerations the code iterates (the actual set is predicate-discovered at runtime via `SystemActorKeys`). The bridge is auto-included by the predicate, so **no logic change is needed**. The dev MAY freshen these comments to "+ Bridge" for accuracy (a zero-risk doc-comment touch) but it is **not required** for correctness and is **not** a lockstep site. Recommendation: freshen `lenses.go` ~43 and `system_actors.go` ~17 (closest to the bootstrap topology) for accuracy; leave the processor/aiagent ones or freshen them at the dev's discretion — note whichever choice in the summary. (Do NOT mistake these for the real lockstep sites in §0; the real ones are the *enumerations and counts*, which the predicate-discovery comments are not.)

**Q4 — Does 13.4 need anything beyond `BridgeIdentityKey` exported?** This story exports `bootstrap.BridgeIdentityKey` (+ `BridgeIdentityID`), which 13.4's `cmd/bridge/main.go` will read as its `actorKey` (exactly as `cmd/loom/main.go` ~64 reads `LoomIdentityKey` and `cmd/weaver/main.go` ~80 reads `WeaverIdentityKey`). 13.4 also needs the bridge to be authorizable on `ops.system.>` (the `system`-lane carry, deferred — Q-of-record in service-actors.md). Confirm 13.4 keys its result-op submission off `BridgeIdentityKey` and inherits the deferred `system`-lane note. No action in 13.3 beyond the export + the doc note; recorded so the 13.4 author aligns.

---

## Dev Agent Record — Amelia (DS), 2026-06-18

**Status set to `review`** — implementation complete, all gates green. Handed to Winston for the mandatory 3-layer adversarial review (Blind Hunter / Edge Case Hunter / Acceptance Auditor — this is kernel topology / security plane) + commit. DS does not commit/push/branch (CLAUDE.md).

### Deliverables vs §1 checklist — all done
1. ✅ `identity.system.bridge` provisioned, `protected:true`, root-equivalent purely via `holdsRole → operator`, persisted NanoID (mirrors Loom/Weaver exactly).
2. ✅ Bootstrap-file version `7` → `8` (validate + persist + checkVersion + version-history ledger); `bridgeIdentity` added to `PrimordialIDsRaw`.
3. ✅ Bridge identity + holdsRole link added to both kernel-verify enumerations in lockstep via the shared `PrimordialVertexKeys()` slice + `PrimordialVertexKeyCount` (27 → 29); both hand-maintained header comments updated.
4. ✅ Bridge `cap.identity.<id>` added to the `WaitForBootstrapComplete` `capProjections` readiness gate (AC #2 / Contract #7 §7.5).
5. ✅ `docs/components/service-actors.md` updated throughout (provisioning table, prose, class-never-gates, `system`-lane deferral, readiness gate, version-bump note).
6. ✅ Lockstep grep + regenerated `lattice.bootstrap.json` (version 8 + `bridgeIdentity`).

### Complete lockstep-site list actually touched (file:line at completion)
| # | Site | Change |
|---|------|--------|
| 1 | nanoid.go:82-83 (+ doc 73-77) | `BridgeIdentityID`/`Key` vars |
| 1 | nanoid.go:138 | `BridgeHoldsRoleLinkKey` var |
| 2 | nanoid.go:150 | `PrimordialIDsRaw.BridgeIdentity` (`json:"bridgeIdentity"`) |
| 3 | nanoid.go:190-191 | version-history doc `"8"` line |
| 3 | nanoid.go:464 | `persistWithStatus` `Version: "8"` |
| 4 | nanoid.go:317-334 | `checkVersion` doc + `case "8"` + `want "8"` msg |
| 5 | nanoid.go:285 | `currentRaw()` `BridgeIdentity:` |
| 6 | nanoid.go:346 | `generate()` `&raw.BridgeIdentity` |
| 7 | nanoid.go:383 | `populate()` validate `{"bridgeIdentity", …}` |
| 7 | nanoid.go:410 | `populate()` assign `BridgeIdentityID` |
| 7 | nanoid.go:447 | derive `BridgeIdentityKey` |
| 7 | nanoid.go:458 | derive `BridgeHoldsRoleLinkKey` |
| 8 | nanoid.go:492 | `PrimordialVertexKeys()` identity |
| 8 | nanoid.go:518 | `PrimordialVertexKeys()` holdsRole link |
| 9 | nanoid.go:528-535 | `PrimordialVertexKeyCount` 27 → 29 + breakdown |
| 10 | primordial.go:367-371 (+ comment 189-196, region 337-344) | bridge identity vertex |
| 11 | primordial.go:642-647 (+ region comment 615-622) | bridge holdsRole link |
| 12 | primordial.go:1031 (+ doc 992-995) | **readiness gate** `{"bridge", …}` |
| 13 | primordial.go:194-196, 211, 214 | kernel-composition doc (3 actors, 3 holdsRole, ≈75) |
| 14 | scripts/verify-kernel.go:7,11,25,27 | header enumeration 2→3 + tallies (~75 / ~69) |
| 15 | internal/bootstrap/verify.go | **confirmed** iterates `PrimordialVertexKeys()`, no hardcoded actor list — picks up bridge automatically; no edit needed |
| 16 | lattice.bootstrap.json | **regenerated** via `make down && make up` → `version:"8"` + `bridgeIdentity` (gitignored; see Deviation 1) |
| 17 | Makefile:39 | `up` echo "+ Bridge" |
| 17 | Makefile:61 | `verify-kernel` count comment 89→91 / 28→30 |
| — | lenses.go:43 (Q3) | freshened "Loom + Weaver + Bridge" (doc comment, non-lockstep) |
| — | system_actors.go:17 (Q3) | freshened "Loom + Weaver + Bridge" (doc comment, non-lockstep) |

**Grep proof — no straggler:** `grep -rn -E "LoomIdentity(Key|ID)|WeaverIdentity(Key|ID)"` over production `.go` shows every bootstrap-inventory/count/readiness Loom/Weaver site has a parallel Bridge entry (the only Loom/Weaver sites with NO bridge parallel are `cmd/loom/main.go:64` + `cmd/weaver/main.go:80` — the engine entrypoints whose bridge analogue is Story 13.4, deliberately deferred per §0a). `grep -rn -E "\b27\b|\b73\b|\b67\b" internal/bootstrap scripts` returns **empty** — no stale count survives. The lone `"7"` in nanoid.go is the version-history ledger line (correct historical record).

### Test count (from diff)
- **Extended** (existing Loom/Weaver enumerations → +Bridge): `service_actor_test.go` (5 tests), `service_actor_e2e_test.go` (2 tests, incl. the readiness-gate test now gating on the bridge), `service_actor_projection_e2e_test.go` (1 Refractor root-equivalence test).
- **New** (in `service_actor_test.go`, internal `bootstrap` pkg for unexported access): `TestPrimordialVertexKeyCount_AgreesWithEnumeration` (len==Count==29), `TestBridgeKeyDerivation`, `TestGeneratePopulateRoundTrip_Bridge`, `TestCheckVersion_RejectsStaleAcceptsCurrent` (4 new).
- Total: 8 extended + 4 new = 12 tests touching the bridge; all PASS.

### New kernel tally
- `PrimordialVertexKeys()` 27 → **29**; `PrimordialVertexKeyCount` 27 → **29** (agreement asserted in-Go + by verify-kernel `len()==Count`).
- verify-kernel header ≈75 entries / ≈69 OK lines; `make verify-kernel` printed `Checking 29 kernel Core KV keys...` → `ALL ASSERTIONS PASSED`.

### How `lattice.bootstrap.json` was regenerated
`make down && make up` (Docker stack was running). `make down` removed the old file; `make up` re-minted all 20 NanoIDs (incl. fresh `bridgeIdentity: Z2wGcDWWLctcm9CTEVWS`) and persisted `version:"8"`, `status:"committed"`. The `make up` readiness pass logged **`readiness gate satisfied … capProjections=4`** then `Lattice ready` — proving the gate waited on all four (admin/loom/weaver/**bridge**) cap projections.

### Gates — all run in foreground
| Gate | Result |
|------|--------|
| `go build ./...` | ✅ exit 0 |
| `make vet` | ✅ exit 0 |
| `golangci-lint run ./...` | ✅ 0 issues |
| `make verify-kernel` | ✅ ALL ASSERTIONS PASSED (29 keys, bridge identity + link OK) |
| `make down && make up` | ✅ atomic batch count=95 (+2); `capProjections=4`; `Lattice ready`; JSON regenerated v8 + bridgeIdentity |
| `go test ./internal/bootstrap/... -count=1` | ✅ ok (17.6s) |
| `go test ./internal/refractor/... -count=1` | ✅ ok (all packages) |
| lockstep grep | ✅ parallel proven; no stale 27/73/67 |
| `go test ./internal/processor/... -count=1` | ⚠️ one flake (`TestOutbox_NoDoublePublish`, embedded-NATS `meta.inf.tmp` temp-dir race, unrelated to this story) → **passed clean on isolated re-run** (Deviation 14) |

**Confirmed: no new role / permission / grantedBy link / cypher / step-3 / `SystemActorKeys` change.** Root-equivalence is the single `holdsRole → operator` edge. The auto-discovery planes (`SystemActorKeys` predicate, Capability-Lens `WHERE data.protected=true` cypher, step-3 auth, Refractor projection) pick up the bridge automatically — touching them would be over-reach (§0a). `lenses.go`/`system_actors.go` got doc-comment-only freshening (Q3), no logic.

### Deviations
1. **`lattice.bootstrap.json` is gitignored, not checked-in.** The story (lockstep #16) says "Commit the regenerated file." In this repo `.gitignore:48-49` explicitly ignores `lattice.bootstrap.json` as a "bootstrap runtime artifact (generated by make up)" and `git log` shows it was **never tracked** — consistent with per-deployment-unique NanoIDs (NFR-SC2/FR48; committing it would pin one deployment's key space). I regenerated it locally at `version:"8"` + `bridgeIdentity` (the AC-closing proof), and it sits in the working tree, but **git will not stage it and there is nothing for Winston to commit for it.** This is a story-language vs. repo-reality note, not a code deviation — flagging so Winston isn't surprised the file is absent from `git status`.
2. **Processor outbox flake** (above) — re-ran clean per Deviation 14.

### Open Questions
- Q1–Q4 (above) stand as written; none block. Q1 reading (NanoID = persisted-random, mirror Loom/Weaver) implemented as recommended. Q2 (Makefile pre-existing 27-vs-28 comment) left as-is beyond the +2 delta per the story. Q3 freshened `lenses.go`/`system_actors.go` only (processor/aiagent prose comments left untouched — still say "Loom + Weaver", non-load-bearing). Q4 export confirmed (`BridgeIdentityKey`/`BridgeIdentityID` exported for 13.4).
- **New OQ:** none beyond Deviation 1 (the gitignore reconciliation).

### Review depth
This is **kernel topology / security plane** — the full 3-layer adversarial review is mandatory (not lead-only), per the story Tier line + CLAUDE.md. Handing to Winston.
