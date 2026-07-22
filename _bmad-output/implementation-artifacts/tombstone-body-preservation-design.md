# Design — Tombstone body preservation as posture (a tombstone may never blank a body)

**Status: 📐 awaiting-Andrew (ratification)**
**Author: Winston (Designer fire, 2026-07-22)**
**Backlog row:** `planning-artifacts/backlog/lattice.md` → *Component maintenance* → "[Processor] A tombstone now retains the entity body".
**Grounded demand:** the step-8 document-preservation fix (`7e5f1e6`, 2026-07-19) made body-preservation the runtime truth; the board filed the residual posture question — *whether a tombstone may ever blank a body* — as a Contract #3 call for Andrew.

---

## For Andrew (one-look ratification)

**What it does (two lines).** Ratifies **"a tombstone is a logical-absence marker, never an eraser"** as the permanent Contract #3 posture: a tombstone mutation carries no `document`, can never modify or blank the stored body, and the two real erasure jobs stay where they already live — **crypto-shred** (§3.10/§3.11) erases sensitive content, the **shelved hard-delete verb** reclaims keyspace. The build is a small cleanup fire: remove the three inert `{"isDeleted":true,"data":{}}` husks the emitters still ship, relax the UpgradePackage input schema that *forces* the husk, and make the parser warn (later reject) on a tombstone that carries a document.

**No architectural fork.** This codifies shipped behavior; the one judgment call (silent-ignore vs warn vs reject on a tombstone-with-document) is designed through in §5 with warn→reject recommended, mirroring the script-read-posture warn→block precedent.

**Frozen-contract changes (staged UNCOMMITTED in `main` — the diff is the proposal).**
- **Contract #3 §3.3 (`tombstone`)** — two sharpening sentences: a tombstone mutation carries no `document` (one supplied is not honored); body erasure is not a tombstone capability — sensitive-content erasure is crypto-shred (§3.10/§3.11), keyspace reclaim is the (shelved) hard-delete verb. Affected consumers: script authors + the three in-repo emitters (all cleaned in Fire 1); no runtime semantics change.
- **Contract #8 (UpgradePackage payload example)** — the tombstone example row drops its `"document": { "isDeleted": true, "data": {} }` husk, which today *teaches* the pattern this design retires. Affected consumers: `internal/pkgmgr` (the only payload producer; cleaned in Fire 1).

---

## 1. Problem + intent

The substrate has no partial update — every KV write replaces the whole value — so before `7e5f1e6`
a tombstone (whose `document` the mutation parser never reads) erased the entire stored body: a
tombstoned link lost the `class`/`sourceVertex`/`targetVertex` that make it readable *as a link*,
and every tombstoned entity lost its Contract #1 §1.3 creation triplet. Step 8 now reads the stored
document behind every update/tombstone and carries it over whole, changing only `isDeleted` + the
lastModified triplet (`step8_commit.go` `buildMutationValue`/`readPriorDocuments`).

That fix left a posture residue in three places that still *say* "blank the body":

1. **`internal/bootstrap/meta_ddl.go` `make_tombstone`** — emits `{"isDeleted": True, "data": {}}`.
2. **`internal/bootstrap/install_ddl.go` (UninstallPackage script)** — same husk per declared key.
3. **`internal/pkgmgr/upgrade.go:282`** — the diff's tombstone mutations carry the husk, and the
   **UpgradePackage input schema** (`install_ddl.go` `upgradePackageInputSchema`) *requires*
   `document` on **every** mutation including tombstones — the schema forces the fabrication.

The husk is a no-op twice over: the Starlark result parser reads `document` only for
`create`/`update` (`starlark_runner.go:256`), and step 8 rebuilds a tombstone's value from the
stored document regardless. But it is *misleading* dead weight: it teaches script authors a blanking
capability that does not exist (and silently swallows their intent if they try), and Contract #8's
payload example reproduces the pattern. The board filed the open question: **may a tombstone ever
blank a body?**

## 2. The posture decision: no — a tombstone is a logical-absence marker, never an eraser

**Recommendation: ratify body-preservation as the permanent Contract #3 posture.** Grounds:

- **The preserved body is load-bearing.** `kv.Links`/`kv.Read` return tombstoned entities carrying
  `isDeleted` — the script decides (Contract #2) — so a tombstoned link must stay *readable as a
  link*. Restore-by-update (Contract #3 §3.3: an `update` setting `isDeleted: false` restores) only
  means something if the body survived. The creation triplet must survive for provenance (and no
  script can resupply it — step 8 drops script-supplied triplet values precisely so they can't be
  forged).
- **Erasure-by-blank is a false guarantee.** Core KV runs History=1, so an overwrite does drop the
  prior revision from the *bucket* — but by then the body has already propagated to every derived
  plane: lens targets, the Chronicler's orchestration-history, CDC consumers, backups. Contract #3
  §3.11 states the platform's erasure model plainly: *the guarantee is key-destruction, not
  byte-deletion*. A blank-on-tombstone would be byte-deletion at exactly one of N copies — worse
  than useless, because it *reads* like an erasure guarantee.
- **The real erasure jobs are already owned.** Sensitive content: crypto-shred (§3.10/§3.11 — a
  shredded key makes ciphertext permanent gibberish everywhere at once, including history and
  backups), with the step-8 consequence already shipped: a tombstoned sensitive aspect retains real
  ciphertext but `decryptSensitiveDoc` refuses a soft-deleted aspect. Keyspace reclaim: the
  hard-delete `delete` verb — designed, ratification-shelved (Andrew 2026-07-02), and explicitly a
  *key-removal* (NATS `DEL` marker), not a body-blank. Tombstone needs no third erasure identity.
- **No consumer wants blanking.** The three emitters tombstone package DDL entities (meta-vertices,
  lenses, roles, permissions) — code/config, no personal data. The board row records it: *no
  consumer depends on body-erasure*.

### Alternatives considered

- **B — honor the supplied document on tombstone (blank-on-request).** Rejected: reintroduces the
  unreadable-link/erased-triplet class of bug for any author who supplies a partial document,
  contradicts the §3.2 field table (`document`: create/update only), and ships the false erasure
  guarantee above. A narrower variant (blank `data` only, preserve structure) fails the same
  false-guarantee test and still has no consumer.
- **C — route blanking through the hard-delete verb.** Out of scope by construction: `delete`
  removes the *key* (history retained); it is shelved with its own revival conditions, and nothing
  here revives it. This design cites it only to fix the erasure taxonomy: soft tombstone = logical
  absence · crypto-shred = content erasure · `delete` (shelved) = keyspace reclaim.

## 3. Reconciliation with the existing mental model

- **"Didn't we already handle this?"** The *runtime* did (`7e5f1e6`). What remains is the posture
  codification (Contract #3 never says whether a supplied tombstone document is honored — it is
  silently discarded), the three misleading emitters, the input schema that forces the husk, and
  the Contract #8 example that teaches it.
- **Scope guard — Refractor target-row tombstones are a different plane.** Lens-target soft deletes
  (`refractor/adapter/natskv.go` writing `{isDeleted: true, projectionSeq}` rows; Contract #10's
  read-model tombstones) are *projection* deletes of derived rows — husk-by-design, correct as-is.
  This design governs **Core KV mutation tombstones only**; a builder must not "clean up" the
  adapter.
- **Does this add new state or machinery?** No. It removes dead payload, relaxes one input schema,
  and adds one parser warning. Nothing is built ahead of a consumer (dead-scaffolding test: the
  warn's consumer is the flip decision in Fire 2; the flip's consumer is every future script
  author).
- **Contract #4's operator-tombstone-then-resubmit** (tombstoning a `vtx.op.` tracker to permit
  retry) is unaffected — it keys off `isDeleted`, not the body.

## 4. Contract surface (both edits staged UNCOMMITTED)

- **Contract #3 §3.3 `tombstone` paragraph** — append: *"A tombstone mutation carries no `document`;
  one supplied is not honored (warned today, rejected once the emitter sweep lands — §5). A
  tombstone can never modify or blank the stored body: content erasure is crypto-shredding
  (§3.10/§3.11), and keyspace reclaim is the separately-designed `delete` verb."* Build-to
  everywhere else.
- **Contract #8 UpgradePackage payload example** — the tombstone row becomes
  `{ "op": "tombstone", "key": "vtx.permission.MnPq…" }`, and the payload-schema prose notes
  `document` is required for create/update only.

## 5. The one judgment call, resolved: silent-ignore → warn → reject

Today a tombstone-with-document is **silently ignored** — a fail-quiet surface: an author who
believes they are blanking (or *modifying-while-deleting*) gets a no-op with no signal. The
fail-loud house rule says this should ultimately **reject** (`InvalidReturnShape`). But an immediate
reject would break live stacks: DDL scripts are *stored in Core KV*, and bootstrap seeds only an
empty bucket — an existing stack keeps the old `make_tombstone`/UninstallPackage scripts emitting
the husk until its world is recreated. So:

- **Fire 1 ships warn:** the parser logs a structured warning (`requestId`, mutation index, key) and
  drops the document, exactly as today minus the silence. All in-repo emitters stop emitting the
  husk in the same fire, so a warn sighting after Fire 1 identifies a stale stored script or an
  out-of-tree author.
- **Fire 2 flips warn→reject** once warn sightings are clean (dev stacks recreate freely; the demo
  world rotates nightly — one rotation after Fire 1 lands, stored scripts are current everywhere
  that matters pre-prod). This mirrors the script-read-posture warn→block precedent: land the sweep,
  observe clean, then gate.

## 6. Build decomposition for the Steward

**Fire 1 (S) — emitter sweep + warn.** (a) `meta_ddl.go` `make_tombstone` and the UninstallPackage
script emit `{"op": "tombstone", "key": key}` (+ `expectedRevision` where present); (b)
`pkgmgr/upgrade.go` drops the husk `Document` from tombstone `installMutation`s; (c)
`upgradePackageInputSchema` moves `document` out of the top-level `required` (create/update
enforcement stays with the parser + step 6, which already govern); (d) the parser warn per §5; (e)
vectors: parser accepts a bare tombstone / warns-and-drops a documented one; upgrade + uninstall
e2e green with huskless payloads; the existing step-8 preservation tests continue to pin the
runtime posture; (f) drive-by doc fix: `docs/components/vault.md` cites contract authority
`03-processor-mutation-semantics.md` — the file is `03-mutation-batch-event-list.md`. Winston
commits the two contract edits with this fire once ratified.

**Fire 2 (XS, sequenced behind a clean warn window) — flip warn→reject** with a regression vector;
no emitter changes remain by then.

## 7. Test strategy & migration

No stored-data migration: nothing about committed documents changes shape. Old stored scripts keep
working under warn (Fire 1) and are naturally replaced by world recreation before the Fire-2 flip.
Proof: unit vectors in `starlark_runner`/`step6`/`pkgmgr` per §6, plus the existing
`step8_commit_test.go` preservation pins; the package upgrade/uninstall e2e paths exercise the
huskless wire shape end-to-end.

## 8. Risks

Low. The only behavioral deltas are a new warning line and, at Fire 2, a rejection of a shape no
in-repo emitter produces. The rejection is deliberately sequenced behind observation, so the known
failure mode (a stale stored script on a long-lived stack) surfaces as a warning, never an outage.
