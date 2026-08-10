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
refusal, and the `meta`/`op` name reservation (paired with the Processor's step-6 gates).

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
- **The installer parses but never reads a lens spec's labels** — any authoring check that assumes label
  availability at install time is uncomputable until the label-extraction board row lands. Minted:
  dynamic-type-taxonomy §17.10. Check: that row.
- **A corpus-wide guard read must exclude the churn namespaces** — an install-time scan over every vertex
  root also walks `vtx.op.<requestId>` idempotency trackers, a 24h-horizon population that is millions of
  keys on a busy kernel, against a 45-60s install deadline (and the long-lived Loupe process). Minted:
  dynamic-type-taxonomy C1.1. Check: the candidate set excludes reserved segments, and the losslessness of
  the exclusion is argued at the call site rather than assumed.
- **RETIRED (the model of a retired entry):** a package-content edit without a version bump silently
  no-ops on a live stack — mechanized: `scripts/lint-package-version.go`.
