# Pkgmgr — the package manager (`internal/pkgmgr`)

Installs, upgrades, and verifies **packages** — the declarative bundles (DDLs, lenses, operations,
permissions, seeds) that define a vertical. The package *model* itself is documented in
[`_packages.md`](_packages.md); this doc covers the machinery that applies it.

Pkgmgr parses a package definition, diffs it against the running stack's meta state, and applies the delta
**as operations** (P2 — it writes nothing to Core KV directly). The capability materializer
(`capabilitymaterializer.go`) generates the capability/grant lenses a package's permission specs imply.
Verification (`make verify-package-<x>`) replays a package against a **live** stack (it targets `NATS_URL`;
it is not self-contained) and asserts the projected state.

**Taxonomy duties** (dynamic-type-taxonomy design): the install/upgrade-time gates for abstract vertex
types — declaration shape, acyclicity + depth, the abstract-flip no-live-instances guard, event-class
refusal, the `meta`/`op` name reservation (paired with the Processor's step-6 gates), and the
**narrowed-filter label cap** (`lenslabelcap.go`), which refuses an install whose own lens cannot fit
`K + Σ leafBudget ≤ 8` at its abstract labels' declared worst case.

*This doc is deliberately minimal — mandate + dossier. The owner fleshes out mechanism sections as it works
the component (docs-in-Definition-of-Done, `agents/owner/SKILL.md` §6).*

---

## Review keeps catching (dossier)

Same contract as every dossier: fire briefs copy the applicable entries into part 5
(`agents/fire-brief-template.md`); the item-close review appends new ones (`agents/steward/SKILL.md` §4);
**capped at 12 one-liners**; an entry retires when a lint/test gate mechanizes it.

- **canonicalName and the instance key segment are different namespaces** — a guard, census, or check keyed
  on canonicalName-as-segment is vacuous for a type whose instances use a different segment (and a rule
  conflating the label namespace with either one is wrong a third way). Minted: dynamic-type-taxonomy
  §17.9 — it both retired ratified check A3-1 and left the flip guard's divergent-type gap. Check: none
  yet (the board row's fix).
- **Two writers of one deterministic key** — a create-only writer bricks the second; a second writer needs
  an explicit arbitration or a single owner, decided before it is added. Minted: package-authoring
  incident. Check: standing checklist #5.
- **An injected dependency held in a nil-able field silently disables the gate it feeds** — the compiler
  enforces the interface and sees nothing at the injection site, so a fixture that builds the struct by hand
  runs the gate as a no-op and reports green on code the real entry point refuses. Minted:
  dynamic-type-taxonomy C3.7 (`Installer.SpecParser`; 30 of 34 fixtures unwired). Check:
  `scripts/lint-conventions.go` pins `pkgmgr.NewInstaller` to its sanctioned callers.
- **A gate that cannot price one input must not present the remaining sum as a worst case** — an
  unresolvable expansion label contributed 0 to a total the refusal message then called "the worst case",
  sending an author to shrink the wrong budget. Minted: dynamic-type-taxonomy C3.7. Check: the un-priced
  labels are named in the message and the total is stated as a floor.
- **A refusal's stated remedy must not be a move that defeats the gate — and "the verb exists and is
  granted" is NOT evidence the remedy works.** Two sightings. The cap refusal advised dropping the redundant
  concrete label, which clears exhaustiveness and trades the refusal for the exact silent regression the gate
  detects (dynamic-type-taxonomy C3.7). Then the occupancy refusal told operators to restore a revoked grant
  with `CreatePermission` — verified only as far as "rbac-domain grants it" — when authority travels the
  `grantedBy` edge a SECOND verb writes, so the remedy returned a success reply and no grant
  (reinstall-over-uninstall-occupancy §3, struck in the design body). Check: trace the remedy to the
  OUTCOME it promises, through the projection or consumer that delivers it, not to the existence of its
  first step; and a remedy printed for every caller must be qualified per caller — name the states it is
  false in (a reserved operationType, a declared `Lanes`, the package that grants the remedy verbs itself).
- **A new failure mode is not shipped until every surface that renders it says the right thing** — the
  message, the error's own shape, and each status/UX mapping downstream. `ErrDeclaredKeysOccupied` fell
  through `cmd/loupe`'s default arm to **502**, the code that UI's own front end treats as a transport blip
  worth retrying, for a state that fails identically forever; and the two occupancy buckets were carried
  only in prose, so a review got a real defect — every tombstoned key also reported live — past the FULL
  suite by lowercasing one word. Minted: reinstall-over-uninstall-occupancy close review. Check: a new
  sentinel is grepped across every `errors.Is` status/UX mapping in `cmd/` before it ships (and duplicated
  mappings are folded into one helper so they cannot drift); a distinction the code MAKES is carried in
  fields and asserted from fields, never scraped from the rendered message.
- **A corpus-wide guard read must exclude the churn namespaces** — an install-time scan over every vertex
  root also walks `vtx.op.<requestId>` idempotency trackers, a 24h-horizon population that is millions of
  keys on a busy kernel, against a 45-60s install deadline (and the long-lived Loupe process). Minted:
  dynamic-type-taxonomy C1.1. Check: the candidate set excludes reserved segments, and the losslessness of
  the exclusion is argued at the call site rather than assumed.
- **RETIRED (the model of a retired entry):** a package-content edit without a version bump silently
  no-ops on a live stack — mechanized: `scripts/lint-package-version.go`.
- **A guard protecting what a consumer READS must enumerate every condition under which that consumer
  stops seeing the thing** — the retention-class destruction oracle drops a lens on THREE independent
  conditions (secure-column content, the vertex root's `class`, an `eventStream` source), and a guard built
  against one of them left the other two as silent erasures with a clean "0 retired" report. Minted:
  retention-class-key-custody §30 (both found by cold review executing them, not by reading). Second
  sighting: the occupancy probe reads DOCUMENTS via `KVGetMulti`, which drops delete/purge markers, while
  the commit's `CreateOnly` fails against a marker exactly as against a document — so the probe's "free" is
  narrower than the commit's, in the one state an operator reaches by clearing a key by hand. Check: the
  brief names the consumer's full exclusion set and the guard carries one test vector per condition; where
  a condition is deliberately left uncovered, the code says so as a named narrowing with its direction
  (under- vs over-reporting), and the operator-facing text closes the path that leads into it.
- **A field validated after normalization must be MATCHED after the same normalization — but folding a
  DESTRUCTIVE resolver's match set is the wrong cure.** `Lens` was `TrimSpace`-checked for emptiness and
  resolved raw, so a declaration with a trailing space was refused with a remedy identical to the line
  already in the author's file. The obvious fix — fold both sides — was then applied to
  `findInstalledPackage` and refuted in review: `normalizePackageName`'s original consumer is a DENY-LIST
  (a wider match only denies more), while a resolver hit is a diff-apply or a tombstone (a wider match
  mutates more), so the same fold has opposite polarity per consumer. Minted: §30 fix round + the
  `packageName` follow-on. Check: normalize symmetrically where a match GRANTS nothing, and where a match
  selects something destructive keep the comparison exact and make the near-miss a loud refusal; every
  identity string resolving to a deterministic key carries a whitespace/case vector in its own test.
- **A local gate run and CI's gate run do not see the same tree** — every `scripts/lint-*.go` reads its
  scan set from git, so what the author has not committed is not what the gate judged. Two sightings: a
  version bump unified away by a rebase, where the merged base read unchanged while `internal/pkgmgr/`
  still differed; and a brand-new **untracked** file, invisible to `git ls-files` and so reported "0
  issues" locally while CI — linting a committed tree — failed on it. Minted: §30's CI red; second
  sighting the privacy-base ceremony's CI red. Check (mechanized): `lint-conventions` now names the
  untracked `.go` files it did not scan; for diff-based gates re-run against CI's base,
  `DIFF_BASE=<merge-base> go run ./scripts/lint-package-version.go`. Run both **after committing**.
- **A security-plane skip guard keyed on tombstone-state alone, with no anchor-type check, silently widens
  past its ratified scope** — a fix scoped to "respect a revoked grant" first-cut as "respect any
  surviving tombstoned key," quietly covering package definitions (`vtx.meta.*`) the design never
  analyzed. Minted: grant-provenance §12 (un-tombstone prerequisite), caught by cold adversarial review.
  Check: none yet (key the guard on the anchor-type prefix, e.g. `metaVertexPrefix`, never on
  tombstone-state alone).
