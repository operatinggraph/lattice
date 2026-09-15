# Verticals packages dossier — the two OCC classes go to gates (2026-09-14)

The second promotion pass over the `docs/components/_packages.md` "Review keeps catching" dossier, after
[verticals-packages-dossier-census-2026-09-14.md](verticals-packages-dossier-census-2026-09-14.md) retired the
`kv.Links` page-limit class. That census found the remaining classes clean over the vertical packages and left
the dossier at 21 live entries against its cap of 12, with two twice-plus-seen classes still walk-only:

- **the shared-vertex repoint class** — a live `kv.Read` followed by a revision-less `update` / `tombstone` on the
  key it read — six sightings since 2026-08-15, "mechanize on the next sighting" stated after the second and never
  done; and
- **the read-declaration OCC class** — a script's correctness resting on a key being HYDRATED while only the
  submitter's `contextHint` declares it — two sightings (café `CreditCafeAccount` 2026-09-05, lease-signing
  `TombstoneSupersededLeaseServiceInstance` 2026-09-13), whose prescribed check ("one test submits with an empty
  `contextHint`") the census of 2026-09-14 showed holds for **0 of the 10** vertical ops that define `derive_reads`.

This fire mechanizes both: each gets a `scripts/lint-*.go` gate proven against the corpus and its own mutation
vectors, the missing bare-envelope vectors land in the package suites, and the two dossier entries retire into the
Retired block with the half of each class that stays a walk stated.

## Fire brief

**Scope sentence.** Promote the two twice-plus-seen OCC classes of the `_packages.md` dossier to CI gates:
(A) `lint-live-read-pinned-mutation` — in every shipped package script, a `kv.Read(K)` whose read posture is
`(e)`, `(c)` or unannotated (the three shapes step 4 does not hydrate) followed in the same function by an
`update` or `tombstone` mutation on K that carries no `expectedRevision` is a finding; (B)
`lint-derive-reads-bare-vector` — every op a DDL script's `derive_reads` dispatches on has, in its package's
`_test.go`, at least one `OperationEnvelope{…}` submission with no `ContextHint` (nil or absent). Land every
vector (B) requires, fix every site (A) reports, wire both into `lint-static`, the Makefile and
`docs/components/lint-gates.md`, and retire the two dossier entries.

**Why the population is what it is (grounded live at `26a16ef5`).** `applyHydratedRevisions`
([commit_path.go:672](../../internal/processor/commit_path.go)) conditions a bare `update`/`tombstone` on the
step-4 hydrated revision only for keys in the hydrated set; a key the script obtained through a live `kv.Read`
"has no step-4 revision and stays unconditioned — the honest limit". The corpus annotates every `kv.Read` with
`# read-posture: (a|c|d|e|f)` (`lint-conventions.go:405`; binding rule `annotationAnchor` / `annotationSpan`,
`lint-conventions.go:1071-1115`: an annotation on its own comment line binds to the first code line beneath it
across comment lines, a blank line breaks the block). `(a)`, `(d)`, `(f)` are declared-and-hydrated, so a bare
update on them is engine-conditioned; `(e)` (walk follow-up) and `(c)` (config) are live by definition, and an
unannotated read is class-(b) debt — none of the three is hydrated. The descriptor floor
([step4_hydrate.go:296-300](../../internal/processor/step4_hydrate.go)) DEMOTES an envelope's declaration and
never adds to it, so the submitter's `contextHint` remains the only source of hydration for a key `derive_reads`
does not return — the second class's premise stands. `derive_reads` runs after authorization and before the first
Core KV GET (`step4_hydrate.go:321-327`), hydrating its keys exactly like declared ones; the read-drift guard
(`internal/testutil/read_drift_guard.go`) is armed on every `CapabilityPipeline`, so a bare-envelope submission
that lazily reads an undeclared, un-walked, un-baselined key fails the test at the read.

**Touch-list (verified live at `26a16ef5`).**

- Gate A precedent: [`scripts/lint-links-page-limit.go`](../../scripts/lint-links-page-limit.go) — corpus walk
  over `pkgregistry.Names()` / `Lookup(name).DDLs`, each distinct `Script` body examined once, parsed with
  `go.starlark.net/syntax`; self-test on every run with mutation vectors; refuses an all-clear over zero scripts;
  `--list`; `STRICT=1`; `VERBOSE=1` for unmodelled. Mutation-helper shapes: `make_aspect_update(vtx_key,
  local_name, cls, data)` returns `{"op": "update", "key": vtx_key + "." + local_name, …}` with no
  `expectedRevision` (cafe-ledger `scripts.go:706`); `make_aspect_upsert_occ(…, expected_revision)` sets
  `m["expectedRevision"] = expected_revision` (cafe-ledger `scripts.go:699`); `make_aspect_update_occ` builds it
  in the literal (lease-signing `scripts.go:51`); `make_tombstone(key)` returns `{"op": "tombstone", "key": key}`
  (wellness-domain `ddls.go:1639`). The op vocabulary is `create` / `update` / `tombstone` (129 / 102 / 41
  literals corpus-wide); no `upsert` op exists — the `*_upsert*` helpers emit `update`.
- Gate A positive vectors in the corpus (the gate must judge these CLEAN, and must FIRE when the pin is dropped):
  identity-hygiene `ddls.go:729`, `:843` (`idx_vtx.revision`), identity-domain
  `tombstone_orphaned_credential_index.go:310`, `unbind_identity_credentials.go:294` (`binding.revision`),
  privacy-base `purge_identity_dedup_footprint.go:371`. Conditional shapes the gate must not flag: lease-signing
  `scripts.go:110` `make_link_create_or_revive` (read, pinned revive on present, bare create on absent — a
  `create` is never a finding); privacy-base `seal_identity_for_erasure_complete.go:531` (a `(d)`-annotated read
  whose annotation sits 14 comment lines above the call — the binding rule, not line adjacency).
- Gate B precedent: [`scripts/lint-seed-declared-reads.go`](../../scripts/lint-seed-declared-reads.go) —
  `go/parser` over Go files, `OperationEnvelope{…}` composite literals keyed by their `OperationType:` string
  literal, `pkgregistry.All()` for the op set. The governed set is derived from the consumer: the op names a
  `derive_reads` compares `op.operationType` (or its alias) against. Eight scripts define one: cafe-ledger
  `scripts.go:347` (`EvaluateCafeArrears`) and `:1782` (`DebitAccount`, `CreditCafeAccount`, `RefundCafeCharge`,
  `PayoutCafeCredit`); clinic-domain `ddls.go:3193` (`CreateAppointment`, `RescheduleAppointment`); clinic-ledger
  `scripts.go:753` (`ClinicDebitAccount`, `ClinicCreditAccount`); wellness-domain `ddls.go:2601` (`CreateSession`,
  `CreateSessionSeries`); lease-signing `scripts.go:1772` (`TombstoneSupersededLeaseServiceInstance`);
  identity-domain `ddls.go:1152`; identity-hygiene `ddls.go:351` (`MergeIdentity`); objects-base `ddls.go:537`
  (`AttachObject`, `DetachObject`). The gate lists the exact set on `main`.
- Gate B vector precedent: identity-domain
  [`derive_reads_test.go:132`](../../packages/identity-domain/derive_reads_test.go) `*_UndeclaredSubmitter_*` —
  the envelope literal carries no `ContextHint` field at all, and the assertion is the op's EFFECT (the
  `duplicateOf` link), not the accept alone, "which a script that stopped probing entirely would also produce".
  Every package's own accept test for the op is the fixture to copy (cafe-ledger `ledger_test.go:225` shape).
- Wiring: `.github/workflows/ci.yml:438-453` (the `lint-static` steps), `Makefile:2382-2411` (targets + the
  `.PHONY` list at `:176`), `docs/components/lint-gates.md:31-32` (the table rows).

**Increment order.**

1. Gate A, self-tested, run with `--list` on `main` — the list IS the census; every finding fixed in the same
   increment (pin the read's `revision`, or declare the key so it hydrates); `STRICT=1 go run
   ./scripts/lint-live-read-pinned-mutation.go` clean; `gofmt -l scripts/` empty.
2. Gate B, self-tested, run on `main` — its finding list is the vector set to land; one vector per op in the
   owning package (assertion = the op's effect, or its named refusal where a key the derivation cannot return is
   read from `state` and refused absent); `go test ./packages/<pkg>/ -count=1` per touched package; `STRICT=1 go
   run ./scripts/lint-derive-reads-bare-vector.go` clean.
3. Wiring (ci.yml, Makefile, lint-gates.md), this document's findings tables, the two dossier retirements.

**In-scope gotchas.** A `//go:build ignore` file's gofmt is CI's own `gofmt -l .` step (CLAUDE.md) — run it. A
package whose SCRIPT changes bumps `manifest.yaml` + `Version` (`lint-package-version`); a test-only change does
not. The corpus-wide gates reach the platform packages too (identity-*, objects-base, privacy-base) — a finding
there is fixed here, not allowlisted (the page-limit precedent). Dossier entries in play (part 5): the
shared-vertex repoint class (all six sightings — three are the base shape this gate reads, three are the walk
half: an enumeration-shaped cap, the subject re-derived on re-execute, a directOp rewriting a maintained aspect
off the Weaver row); the read-declaration class (its second sighting is the `state`-not-`kv.Read` refusal shape a
bare vector proves).

**Adjacent finds.** None filed at brief time; the gates' own finding lists are absorbed into increments 1–2.

**Non-goals.** The walk halves of both classes stay in the Retired entries' text, not in a gate. `(a)`/`(d)`
reads followed by a bare update are engine-conditioned and are not findings. A `create` is never a finding. The
dossier's other 19 live entries are untouched.

## Method

Two read-only `haiku` scouts (helper / read-site / annotation census over the whole corpus; harness + `derive_reads`
wiring + per-op vector census over the vertical packages), the lead verifying every claimed hazard at file:line
before briefing — two of the three the first scout named were `(d)`-annotated or on a different key and are
recorded above as the gate's negative vectors. The gates are run against `main` to reproduce the census and
against the fixed tree to go clean; a gate that finds nothing on `main` is proven by its mutation vectors alone.

## Findings

*(filled at close)*
