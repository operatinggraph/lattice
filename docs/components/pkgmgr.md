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
- **RETIRED (the model of a retired entry):** two writers of one deterministic key — a create-only writer
  bricks the second — now carried by the fire-brief standing checklist (#5), which every brief copies in,
  so it reaches a builder without a dossier slot.
- **An injected dependency held in a nil-able field silently disables the gate it feeds** — the compiler
  enforces the interface and sees nothing at the injection site, so a fixture that builds the struct by hand
  runs the gate as a no-op and reports green on code the real entry point refuses. Minted:
  dynamic-type-taxonomy C3.7 (`Installer.SpecParser`; 30 of 34 fixtures unwired). Check:
  `scripts/lint-conventions.go` pins `pkgmgr.NewInstaller` to its sanctioned callers. **Second sighting
  (descriptor-floor template coverage, `39d6cb6`), in the thinner shape: the RULE was covered twelve ways
  and the line DELIVERING it was covered zero — deleting `def.ValidateOpDispatchTemplates` from
  `validateAll`'s list broke no test anywhere, because every refusal test called the exported rule directly
  and the corpus census walked `Definition`s structurally. A corpus test can never supply that coverage
  either, since a healthy corpus contains no violating input. Check (mandated test shape): every member of
  `validateAll` carries at least one test that drives `def.validateAll()` — not the rule — over a fixture
  legal in all other respects, asserting wording only that member emits, so a short-circuit from an earlier
  validator cannot pass for it. `packagename_test.go` is the idiom.**
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
  **Third sighting (permission reconciler, 2026-08-23): THREE of one gate's four printed remedies were
  false, and reading the code is what produced them — only a reviewer who RAN each remedy against a live
  stack found it.** A bootstrap reconcile retains a tombstoned non-`vtx.meta.*` key, a same-version install
  is skipped outright, and the upgrade a remedy promised would revive a revoked grant is precisely the path
  that stopped reviving it. Check tightened: a remedy ships only after it has been EXECUTED against the
  state that prints it, and the before/after of the condition recorded — a remedy traced on paper is a
  hypothesis, not a check.
  Third sighting (§31): the unattested-erasure refusal printed a correct flag, correctly spelled, with the
  package name ahead of it — and Go's `flag` stops at the first positional, so the one string a refused
  operator pastes exited 2 on a usage error. Every reviewer who *read* it passed it; all three who *ran*
  it caught it. Check extended: a refusal that renders a command feeds that exact rendered string through
  the real parser in a test, and a placeholder that can repeat is numbered so the paste does not collide
  with itself.
- **A new failure mode is not shipped until every surface that renders it says the right thing** — the
  message, the error's own shape, and each status/UX mapping downstream. `ErrDeclaredKeysOccupied` fell
  through `cmd/loupe`'s default arm to **502**, the code that UI's own front end treats as a transport blip
  worth retrying, for a state that fails identically forever; and the two occupancy buckets were carried
  only in prose, so a review got a real defect — every tombstoned key also reported live — past the FULL
  suite by lowercasing one word. Minted: reinstall-over-uninstall-occupancy close review. Check: a new
  sentinel is grepped across every `errors.Is` status/UX mapping in `cmd/` before it ships (and duplicated
  mappings are folded into one helper so they cannot drift); a distinction the code MAKES is carried in
  fields and asserted from fields, never scraped from the rendered message. **Adding the sentinel to the
  map proves nothing on its own** (§31): the uninstall path listed `ErrNotInstalled` in the 409 arm and a
  table test went green while the producing call site returned a BARE `fmt.Errorf`, so the real request
  still 502'd — a code that UI treats as a retryable blip. The row must be driven from the entry point
  that produces the error, not asserted against the mapping function in isolation. **Third sighting
  (capability-apply removal refusal): `undeclaredSecureColumnDropError` was a bare `fmt.Errorf` too, and
  the removal guard's own reordering put it on a path a capability proposal reaches.** The bare-error half
  is now MECHANIZED — `scripts/lint-conventions.go`'s `refusal-sentinel` rule fails any `fmt.Errorf` in
  `internal/pkgmgr` whose text says it refuses and which wraps no sentinel, with a declared
  `refusal-sentinel: (transient)` exemption for the refusals a retry can genuinely clear (a torn
  multi-key read). The rest of the entry stands: the gate cannot tell whether the sentinel reached the
  status mapping, so the handler-driven test is still the check.
- **A corpus-wide guard read must exclude the churn namespaces** — an install-time scan over every vertex
  root also walks `vtx.op.<requestId>` idempotency trackers, a 24h-horizon population that is millions of
  keys on a busy kernel, against a 45-60s install deadline (and the long-lived Loupe process). Minted:
  dynamic-type-taxonomy C1.1. Check: the candidate set excludes reserved segments, and the losslessness of
  the exclusion is argued at the call site rather than assumed.
- **A gate you run locally and the gate CI runs are not the same gate** — two dimensions, both sighted.
  **Scope:** `golangci-lint run ./internal/pkgmgr/... ./cmd/...` is not `golangci-lint run`; a Steward
  linted every tree the fire touched except `packages/`, and CI — which lints the whole module — failed on
  a helper the fire's own refactor had left unused. **Tracked-ness:** every `scripts/lint-*.go` reads its
  scan set from git, so an uncommitted edit or an untracked new file is invisible locally and fails on CI's
  committed tree. Minted: §30's CI red and the privacy-base ceremony's; scope sighting, capability-apply
  removal refusal (main went red). Check: invoke the gate the way `.github/workflows/ci.yml` invokes it —
  no path arguments where CI passes none — and run it AFTER committing. `lint-conventions` names the
  untracked `.go` files it did not scan; diff-based gates take `DIFF_BASE=<merge-base>`.
  **Diff-basedness:** `lint-package-version` fires on the pair "`internal/pkgmgr/` changed AND a package
  declaring `ReadGrantDomains` kept its version" — read from the committed tree against CI's merge base, so
  a working-tree run cannot see what it will say, and an unchanged version no-ops a plain install so the
  regenerated lens never reaches a running stack. Minted three times, each a red `main`:
  uninstall-attestation (`c91d3a4a`), edge-manifest (`5c9a2354`), capability-kv single read path
  (`553249f`) — the last added one new file to `internal/pkgmgr` and nothing else. Check: any fire touching
  `internal/pkgmgr` runs `DIFF_BASE=<CI's base> go run ./scripts/lint-package-version.go` AFTER committing.
  A bump the gate asks for is cheap even when the generated artifact is provably unchanged; prove the
  byte-identity if it matters, then bump anyway (2026-08-23).
- **An accessor that returns a struct of slices is a write handle, not a copy** — `MaterializedDefinition()`
  was unexported-field-plus-getter precisely so the applied artifact would be the reviewed artifact, and it
  handed back the plan's own backing arrays: a caller could rewrite the reviewed lens body and have the
  sanctioned apply submit it, with no `Apply(` call in the shape for a lint to match. Minted: capability-apply
  removal refusal, cold review, demonstrated by mutation. Check: an accessor that exists to protect a value
  deep-copies it (a reflection walk, so a field added to a spec struct later cannot silently re-alias), and
  a test mutates what the accessor returned and asserts the source is unchanged.
- **A guard protecting what a consumer READS must enumerate every condition under which that consumer
  stops seeing the thing** — the retention-class destruction oracle drops a lens on THREE independent
  conditions (secure-column content, the vertex root's `class`, an `eventStream` source), and a guard built
  against one of them left the other two as silent erasures with a clean "0 retired" report. Minted:
  retention-class-key-custody §30 (both found by cold review executing them, not by reading). Second
  sighting: the occupancy probe reads DOCUMENTS via `KVGetMulti`, which drops delete/purge markers, while
  the commit's `CreateOnly` fails against a marker exactly as against a document — so the probe's "free" is
  narrower than the commit's, in the one state an operator reaches by clearing a key by hand. Third,
  fourth and fifth sightings, retention-class-key-custody §31, all in the *same* guard and all found by
  executing it beside the reader: enumerating the right conditions is not enough when the guard **decodes
  more loosely** than the reader (a map read never type-errors, so it skipped an eventStream spec the
  oracle's typed probe still counts), reads **fewer nesting levels** (one `targetConfig` where the reader
  unions two), or **stops at a narrower unusable-input set** (an absent spec reported, a present-but-
  undecodable one not). Check (mechanized for this pair):
  `TestUninstallGuardAgreesWithDestructionOracleOnEveryLensShape` drives the REAL `health.RegistryProbe`
  and the guard over one committed KV state per lens shape and fails on any unexplained disagreement —
  a new spec shape is a new row. Elsewhere the check stands: the brief names the consumer's full exclusion
  set, the guard carries one vector per condition, and a deliberately uncovered condition is a named
  narrowing with its direction (under- vs over-reporting) stated in the code. **Sixth sighting (permission
  reconciler, 2026-08-23), the unusable-input shape again and now the ENTRY point rather than a guard:** a
  typed `json.Unmarshal` into a struct of `string` fields `continue`d on error, so a vertex written with one
  unread field as a number entered no class and produced no finding — the gate opted out of by a type — while
  the same function hard-failed on the identical failure four lines away. It also mis-diagnosed: a declared
  key present but unreadable reported as "no live vertex". Mandated test shape, now general: any typed
  decoder standing in front of a looser reader carries a present-but-UNDECODABLE vector, not just an absent
  one, and asserts the decoder's verdict is no wider than the reader's. Also fold in the retired sibling
  lesson: state the property over the DECLARED set (`old \\ new`), never over the mutations the mechanism
  emits, and write the state table over the states the entry point can reach.
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
- **A security-plane skip guard keyed on tombstone-state alone, with no anchor-type check, silently widens
  past its ratified scope** — a fix scoped to "respect a revoked grant" first-cut as "respect any
  surviving tombstoned key," quietly covering package definitions (`vtx.meta.*`) the design never
  analyzed. Minted: grant-provenance §12 (un-tombstone prerequisite), caught by cold adversarial review.
  Check: none yet (key the guard on the anchor-type prefix, e.g. `metaVertexPrefix`, never on
  tombstone-state alone).
- **A check whose DECLARED side is read from the same store the writer controls is an echo, not a check** —
  the permission reconciler compared live `vtx.permission.*` against each package's `declaredKeys`, and both
  halves are Core KV written by one actor in one operation, so an attacker-authored manifest declaring an
  attacker-authored permission reconciled perfectly clean while reading as a security gate. The tell is that
  no input to the comparison comes from outside the system under audit. Minted: the live-vs-declared
  permission reconciler, cold security review (2026-08-23). Check: anchor the declared side in something the
  writer cannot author — for a package that is `internal/pkgregistry`'s compiled `Definition` (note it cannot
  be imported from `internal/pkgmgr`: every `packages/*` imports pkgmgr, so the anchor lives in the calling
  script) — and where no anchor exists, say so as a named residual at the point the code drops the data,
  never as a silent narrowing behind a passing gate.
- **One fact computed twice, owned by nobody, is a divergence waiting to be exploited — and one helper
  answering two different questions is the same defect** — `patternVarNames` served both the accumulator
  check (which must see every name a clause RESOLVES) and the cross-walk disjointness rule (which must see
  only what a clause BINDS), so widening it for the first silently widened the second and two sibling walks
  each carrying `{cancelled: false}` collided on the keyword, refusing a valid package at install time.
  Beside it, the node parser found a property map's end by counting braces while the extractor found it
  quote-aware: the two disagreed on a clause carrying a quoted `}`, and an accumulator reference rode
  straight through the gate into the emitted producer. Minted: the property-map var fix, both halves caught
  only by cold review (2026-08-23). Check: for each shared helper name the question each caller asks and
  split them if the answers differ; for each boundary computed in two places, delete one and make the other
  the single source.
- **A provenance stamp the audited party writes is a label, not evidence — so the classes keyed on it must
  not be where the semantic check lives** — the grant-edge reconciler classified `origin` into five classes
  and ran its derivation check ("an edge may only grant a permission its declaring package owns") in the
  `package` arm alone. `origin` is client-supplied at every authoring channel, so a forger picks their own
  class: stamping `{"origin":"runtime"}` landed a live edge conferring the kernel's `InstallPackage`
  permission on an arbitrary role in an inventory NOTICE, invisible to the gate. The design had reasoned
  that omitting the field was the cheapest laundering; adding one was cheaper. Minted: grant-edge
  provenance, cold security review (2026-08-25). Check: for every class a self-asserted field selects, ask
  what is still verified when the attacker picks that class — and anchor at least one predicate on something
  the writer does not control (here: a kernel permission has exactly one legitimate grant edge, so any other
  edge onto it is drift whatever it claims).
- **A reconciler reading its two sides at different moments reports a state that never existed** — the
  permission gate takes six sequential unfenced reads; a package uninstall commits the manifest tombstone and
  the declared-key tombstones atomically, but a read landing between them sees live package-origin edges and
  an already-uninstalled package, and reports one forgery-shaped drift finding per edge. The remedy it prints
  describes an attack; the cause is a concurrent install. Minted: grant-edge provenance, cold correctness
  review (2026-08-25). Check: a gate that FAILS CI on a multi-read comparison must either fence the reads,
  intersect two passes, or state its quiescence precondition where the operator reading a red gate sees it —
  a false positive in a gate is a defect, not noise.
