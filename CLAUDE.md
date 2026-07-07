# Lattice — house rules for anyone (or any agent) editing this codebase

These apply to **all** work in this repo — the main session and every sub-agent. They are
codebase conventions, not story-specific instructions; story briefs should not need to restate them.

## Roles (who is who)

Two kinds of actor read this file; know which one you are.

- **The lead / adjudicator is "Winston"** — the BMad System Architect persona
  (skill: `bmad-agent-architect`; senior architect, calm and pragmatic, lean-architecture wisdom).
  Winston is the **main session only**: stays lean, runs heavy work in sub-agents, triages their
  findings, and is the **sole** actor who commits (direct to `main`) and watches CI. If you are the
  main session driving the story loop, you are Winston.
- **Sub-agents are not Winston.** A `bmad-create-story` / `bmad-dev-story` / review sub-agent does its
  scoped task and reports back — it **never commits, pushes, or branches** (see Workflow below). The
  other persona names map here: Bob drafts stories, Amelia implements, the three review skills hunt
  adversarially. The commit/CI gate stays with Winston.

## Code conventions

- **No history / changelog comments in code.** Never write `// Story 7.1 …`, `// Replaces …`,
  `// Previously …`, `// Was: …`, `// renamed from …`, `// moved from …`, or any comment that
  narrates a change relative to a prior state. **git blame + the commit message are the record.**
  Comments describe what the code does *now*, for a reader who has no idea a change ever happened.
  (This is the single most-violated rule — do not reintroduce it.) **This applies to planning artifacts
  too** — the backlog / board (`_bmad-output/planning-artifacts/backlog*`) is an **index, not a journal**:
  a row says what an item *is* and its current state + a link to the design doc; it never narrates the
  fire-by-fire build history (`✅ Fire 2 SHIPPED (sha)…`, `Was: …`). That history lives in the design doc
  + git. (Learned the hard way 2026-06-29: the lane files grew to 250–300 KB of in-cell journals — too big
  for any agent to `Read` — and were reformed to capped rows; see `agentic-ops-swimlanes-design.md` §5.)

- **Key-shape conventions (Contract #1).** Aspects are 4-segment `vtx.<type>.<id>.<localName>`
  (never an `asp.*` prefix). Links are 6-segment
  `lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>` (never the short `lnk.<id>.<name>.<id>` form).
  Meta-vertices are `vtx.meta.<NanoID>` with a `.canonicalName` aspect (never
  `vtx.meta.<canonicalName>`).

- **Link naming reads as a sentence** "source <relation> target", and direction follows
  Contract #1 §1.1: the **later-arriving vertex is the source**, the pre-existing one is the
  target. E.g. `identity holdsRole role`, `permission grantedBy role`, `task assignedTo identity`,
  `task forOperation meta`, `task scopedTo <target>`. Run the sentence test on any new link name.

- **Read-path / write-path invariants (lattice-architecture.md P5 / P2) — the most-ignored architecture
  rules; honor them so you don't waste a fire going the wrong way.**
  - **P5 — applications read *lens projections*, never Core KV.** A `cmd/<app>` vertical application (e.g.
    `cmd/loftspace-app`) serves its views from a **lens read-model target** — the NATS-KV read-model buckets
    (e.g. `weaver-targets`) or Postgres — read via `KVGet` / `KVListKeys`. **Copy the pattern in
    `cmd/loftspace-app/{listings,applications}.go`, not Loupe's `corekv` handlers.** **Loupe is the *only*
    application exception** — it is the admin/console *inspector*, so it (and the platform binaries:
    `bootstrap, bridge, lattice, lattice-pkg, loom, object-store-manager, processor, refractor, weaver`) may
    read Core KV directly. The `lint-conventions` **P5 gate** fails any other `cmd/<app>` that references
    `"core-kv"` / `CoreKVBucket`. **If the view you need isn't projected by any lens, add the lens (DDL) to the
    owning package — that's *package* work, not a Core-KV read. A missing platform *primitive* (engine / op /
    substrate / orchestration) is the real Lattice gap; never reach into Core KV as a shortcut.**
  - **P2 — the Processor is the sole writer to Core KV.** Mutate state by **submitting operations**
    (`core-operations` → Processor), never by writing KV directly (Loom / Weaver included). **Reads = lenses;
    writes = ops.** The only sanctioned direct-KV writes outside Refractor's own lens targets are Health KV
    (`health.<component>.<instance>` — operational self-reporting, not Core KV, not a lens).

## Authoritative external sources (vendors)

When you need the **authoritative behavior of a vendored dependency** (semantics, version-gated
features, edge cases), cite the **upstream project's own docs / source / ADRs, version-matched to our
pinned version** — never a secondary blog or an unqualified web search. Web search is a last resort and
must be corroborated against the upstream before you rely on it.

The canonical, version-controlled list of vendors + their authoritative sources + our pins lives in
**[`docs/vendors.md`](docs/vendors.md)** — consult it first, and add a row there when a new vendor's
behavior becomes load-bearing. (For NATS — the substrate — the authority is <https://nats.io> +
<https://github.com/nats-io> incl. the `nats-io/nats-architecture-and-design` ADRs; our pin is **NATS
2.14**, `go.mod` / `docker-compose.yml`.)

## What NOT to edit

- **Frozen contracts** under `docs/contracts/*` are FROZEN — build to them; **don't casually commit changes
  to them.** When work genuinely needs a contract change, make the **edit to the contract doc in `main`, left
  UNCOMMITTED** — that diff **is** the proposal Andrew reviews (don't write a separate amendment-request doc,
  and don't fold it into an in-flight implementation commit). **When the edit is paired with a design doc
  awaiting ratification, Winston commits it once Andrew ratifies that design** — the contract edit + the
  design-doc status + the board row in one scoped commit, promptly (not held "until the build"; a dangling
  ratified edit just makes every fire dance around the dirty shared tree). A standalone contract edit with no
  paired design stays UNCOMMITTED for Andrew. A needed contract change is **never** a reason to skip the work:
  build everything around it, stage the contract edit uncommitted, and flag it.
- **Planning artifacts** under `_bmad-output/planning-artifacts/*` (epics, prd, lattice-architecture,
  data-contracts, MORPH-DEVIATIONS) are owned by the planning lead — do not edit them while
  implementing a story.
- **New documentation goes in `/docs`** (close to the code), not `_bmad-output/`.

## Workflow

- The project runs **session-per-story** — there are **no sprints**, in any phase. Do not run
  `bmad-sprint-planning`/`bmad-sprint-status`. "Launch the next story" means go straight to it.
- **Sub-agents never commit, push, or branch.** Leave changes in the working tree; the lead
  (Winston) reviews, adjudicates, commits direct to `main`, and waits for CI green.
- Verification gates (run before declaring work done): `go build ./...`, `make vet`,
  `golangci-lint run ./...`, `make verify-kernel`, and the relevant `go test` packages — the
  security proof lives in each mechanism's own colocated test (`internal/natsperm`, `internal/processor`'s
  `starlark_*`/`step6_validate`, `internal/refractor/adapter`'s `rls_*`, `internal/gateway/auth`, the
  CapabilityAuthorizer tests) plus the outcome-level residual in `internal/bypass`, all under `go test ./...`.

### Current workflow (Winston drives it): the swim-lane fleet, not a manual story chain

Work is driven by the **scheduled swim-lane fleet** — `steward`, `designer`, `owner`, `fe-engineer`,
`whetstone`, `surveyor`, `vertical-po` (roles: `agents/README.md`; execution model:
`_bmad-output/implementation-artifacts/agentic-ops-swimlanes-design.md`) — not a manual
create-story → dev-story → review chain. Each role already carries its own review-depth-scaling and
progress-tracking (the lane file's row state in `backlog/{lattice,verticals}.md`, not a story file's
`Status:` field).

The bmad `create-story` / `dev-story` / `code-review` / review-hunter skills still exist locally in
`_bmad/` for ad-hoc manual use, but are not the default path and are not part of the scheduled closure.
