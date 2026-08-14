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
- **A refusal's stated remedy must not be a move that defeats the gate** — the cap refusal advised dropping
  the redundant concrete label, which clears exhaustiveness, takes the lens broad forever, and so trades the
  refusal for the exact silent footprint regression the gate exists to detect. Minted: dynamic-type-taxonomy
  C3.7. Check: name the safe moves explicitly and say which lookalike move is not one.
- **RETIRED:** the installer parses but never reads a lens spec's labels, so any install-time authoring check
  over them is uncomputable — mechanized: the `CypherParser` seam returns `SpecLabels`
  (`internal/refractor/ruleengine/full/spec_labels.go`), with the corpus tripwire in
  `lenslabelcap_corpus_test.go` sweeping all 31 packages.
- **A corpus-wide guard read must exclude the churn namespaces** — an install-time scan over every vertex
  root also walks `vtx.op.<requestId>` idempotency trackers, a 24h-horizon population that is millions of
  keys on a busy kernel, against a 45-60s install deadline (and the long-lived Loupe process). Minted:
  dynamic-type-taxonomy C1.1. Check: the candidate set excludes reserved segments, and the losslessness of
  the exclusion is argued at the call site rather than assumed.
- **RETIRED (the model of a retired entry):** a package-content edit without a version bump silently
  no-ops on a live stack — mechanized: `scripts/lint-package-version.go`.
- **A security-plane skip guard keyed on tombstone-state alone, with no anchor-type check, silently widens
  past its ratified scope** — a fix scoped to "respect a revoked grant" first-cut as "respect any
  surviving tombstoned key," quietly covering package definitions (`vtx.meta.*`) the design never
  analyzed. Minted: grant-provenance §12 (un-tombstone prerequisite), caught by cold adversarial review.
  Check: none yet (key the guard on the anchor-type prefix, e.g. `metaVertexPrefix`, never on
  tombstone-state alone).
