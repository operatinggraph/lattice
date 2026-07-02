---
name: ratify
description: Run an interactive design-ratification session with Andrew — pick a board, due-diligence each 📐 awaiting-Andrew item against the pinned code/vendor sources, present one at a time, and on each decision immediately update the design doc + board + contracts and commit. Use when Andrew says "let's review the backlog items awaiting my review" or "/ratify".
---

# Ratify — the interactive design-review session (Winston + Andrew, one item at a time)

**Role:** Winston (bmad-architect persona) in the **Designer** seat — but INTERACTIVE: Andrew is present
and is the decision-maker. You due-diligence and recommend; he ratifies/holds/redirects. This is the
most valuable feedback channel the Designer role has — fold every lesson back (skill §6 improvement loop).

## 0. Setup

1. `git pull --ff-only`; note parallel sessions (boards MOVE mid-session — stewards and other sessions
   commit concurrently; expect it).
2. **Ask which board**: lattice / verticals / loupe (`_bmad-output/planning-artifacts/backlog/*.md`).
3. Enumerate the queue: every `📐 awaiting-Andrew` row + any `⚠️ consolidate/flag-for-Andrew` markers +
   staged-uncommitted `docs/contracts/*` diffs (each belongs to a proposal — map them).
4. **Propose an order** (Andrew can reorder): staged-contract-edit items first (cleans the shared tree)
   → cheap clusters that share context (e.g. one subsystem) → one-look forks → heavyweights last
   (sequence-gated ones lose nothing by waiting). Present the overview as tranches, then item 1.

## 1. Per item: DUE DILIGENCE BEFORE PRESENTING (the whole point)

The designs may have been authored by a different model/session. Re-verify, don't trust:

- **Code claims**: open the cited files/lines; confirm mechanisms (not just existence). A quoted error
  string or "not configurable" claim → check the **pinned vendor source** (`go env GOMODCACHE`) and
  `docs/vendors.md`, never memory.
- **Staleness**: the repo moves fast — re-grep "is this already built?", check the Done logs, check
  whether a dependency shipped since the design was written (a cleared gate strengthens ratification;
  a shipped sibling may subsume it).
- **Banner-vs-build**: when a design's DEMAND cites shipped code, check that code against the
  ratification banner of the design that shipped it — a demand grounded on a banner-withdrawn shape is
  a SYMPTOM (the hard-delete/hasBooking case). Read banners FIRST; they supersede bodies.
- **Cross-design overlap**: grep the other 📐/🏗️ designs for the same seam; two designs on one seam →
  present jointly, simpler one wins.
- **Standing rules**: fewer-larger fires (collapse decompositions; coupled-ships-together); lane splits
  (cmd/loupe/** = Loupe lane — display fires move there); dead-scaffolding (no build without a consumer);
  fail-closed defaults on any new boundary.
- **Fold corrections INTO the doc before presenting** — the diff Andrew ratifies must already be true.

## 2. Present (one at a time) — in the session output as PROSE, never a popup

**Present the assessment + decision as prose in the session output and WAIT for Andrew's chat reply.
NEVER use an `AskUserQuestion` popup for a fork/ratification decision** — a bare multiple-choice popup
strips the context that is the whole point (Andrew, 2026-07-02: *"Giving me the popup like this is not
helpful. I have no context to give you the answer about forks."*). The value of this channel is the
spelled-out assessment he can read and reason about, then answer in his own words (ratify / hold /
redirect / probe). `AskUserQuestion` is acceptable only for a genuinely orthogonal logistics choice
(e.g. "which board?") — never for the design decision itself.

Headline = **clickable design-doc link** (so he never has to dig in the repo). Then: two-line
what-it-does · DD verdict (what you verified, what you corrected — with file:line) · the
decision(s)/fork(s) crisply with your recommendation · fire plan after standing-rule collapse. If a block/product question needs another lens, run **vertical-po /
fe-engineer / owner as READ-ONLY analysis sub-agents** (no builds, no commits, no filing) and fold their
findings. Answer Andrew's probes by GROUNDING (read the code/vault), never by defending the draft —
when he pushes back, re-derive from "what does it need".

## 3. On each decision, close out IMMEDIATELY (then next item)

- **Ratified**: rewrite the design's status banner (decisions, collapsed fires, Q&A folded — and
  REWRITE any body sections the banner supersedes); update the board row (✅ ratified + date + link);
  **commit ratified contract edits promptly** — surgical staging when one contract file carries multiple
  proposals' hunks (`csplit` the diff on `^@@`, `git apply --cached` only the ratified hunks); route
  spun-off consumer rows to the right lane's board.
- **Held / redesigned**: revert that proposal's staged contract hunks (`git apply -R`), shelve the
  design with a reasoning banner (revive conditions explicit), file the replacement row, and fold the
  generalized lesson into the relevant skill(s) + a feedback memory.
- Commit per item (docs-only, scoped, `Co-Authored-By:` = whichever model you are), push, verify.

## Traps (all hit live — do not repeat)

- `lint | tail` masks the exit code → run `STRICT=1 go run ./scripts/lint-board.go` **un-piped and
  &&-gated** before any board commit; row hard cap 600 chars (aim 300); no SHA+prose in cells.
- Boards change under you → re-read (or python exact-match replace) right before editing; Edit-tool
  stale-read failures mean a parallel commit landed.
- `git status` before committing — a parallel session may have STAGED files; your commit sweeps the
  index. Add only your paths; if swept anyway, verify the extras are finished work and say so.
- `.claude/` is gitignored — skills' tracked home is `agents/`; edit BOTH copies.
- Sub-agents never commit; Winston commits direct to main and watches CI (docs commits are paths-ignored).
