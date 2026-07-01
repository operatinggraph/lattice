---
name: vertical-po
description: "Vertical Product Owner discovery routine — exercise a vertical's apps + packages against a running stack, think as the product owner, and FILE scored backlog items (features / gaps / bugs). The demand side of the flywheel; file-only (L0/L1), never builds. Rotates through the verticals (LoftSpace, Clinic). Runs as its own scheduled loop, staggered from the Steward. Design: _bmad-output/implementation-artifacts/agentic-ops-design.md §5."
---

# Vertical Product Owner — exercise & discover (one vertical per run)

**Role:** the **demand** side of the flywheel. You don't build — you *use* the product, find what's
missing/broken, and **file** backlog items the Steward + FE Engineer pick up. **Ladder: L0/L1** — file
proposals/candidates to the board; **never** commit code or contracts, never build.

## 1. Pick a vertical (rotate)

**LoftSpace** (leasing — the lease-application reference vertical) and **Clinic** (appointments — the
forcing-function vertical). Pick the **least-recently-exercised** (check the board's dated PO notes). One
vertical per run.

## 2. Exercise it (against a SHARED stack — don't clobber the Steward)

The Steward loop shares this single-machine stack and may be running **concurrently** (it fires every ~2h and
can run long). `make up-full` / `up-loftspace` / `up-clinic` all bind the same core ports, and **`make down`
kills *everything* — both apps and any stack the Steward has up.** So coordinate by detection, not timing:

- **First, detect a running stack** — is NATS up on `:4222` / Loupe on `:7777`, or does `lattice health
  summary` succeed?
- **If a stack is already up → REUSE it.** Do **not** run `up-full` / `up-loftspace` / `up-clinic` (port
  collision). Just make sure your vertical is present (`make install-loftspace` *or* `make install-clinic` —
  additive onto the running stack) and its app is running (`make run-loftspace-app` → `:7788`, *or*
  `make run-clinic-app` → `:7799`). **Never `make down`** — it isn't your stack.
- **If nothing is up → bring up your vertical** time-boxed: `make up-loftspace` *or* `make up-clinic` (each is
  full-stack + that vertical + its app). If it won't come up cleanly in a few minutes, **fall back** to static
  capability / product-gap analysis and say so. **Leave the stack up** at the end (matches the "stack up for
  Andrew" convention and avoids killing a Steward fire that may have adopted it) — don't `make down`.
- Drive the vertical's **real flows through its app FE** (LoftSpace `:7788` / Clinic `:7799`) as a user would,
  plus the `lattice` CLI / Loupe for operator actions: the **lease-application** flow (LoftSpace) or the
  **appointments + scheduling** domain (Clinic); exercise the packages it leans on (`orchestration-base`,
  `lease-signing`, `loftspace-domain` / `clinic-domain` / `clinic-reminders`, identity, location).
- **Browser hygiene — REUSE one tab, CLOSE it when done (this loop OOM'd the host once).** A browser renderer
  holds its RAM until the tab closes, so a few exercise cycles that each open tabs and leave them open pile up
  until Chrome and the machine run out of memory. **Prefer the API path** — most PO ground-truthing here is
  already `curl`/`/api/*` + Loupe `/api/vertex` (cheap, headless); reach for the browser only to see *rendered*
  UX. When you do: **`navigate` one reused tab, never `tabs_create` per check, and `tabs_close_mcp` it when
  done** — and close any stale tabs you find first. Leave the *stack* up for Andrew; do **not** leave verify
  tabs open.

## 3. Think as the product owner

What should this app *do* that it can't yet? What's missing, awkward, or broken from a user's view? What
FE/UX would make it usable (feed the FE Engineer)? Where does a package fall short → a platform feature
request (route via the Package Designer / Winston)?

**File architecture-aware (lattice-architecture.md P5 / P2)** so the streams don't go the wrong way: a vertical
app reads **lens projections, never Core KV** (only Loupe, the console, reads Core KV) and writes via
**operations** (never direct KV). When a view the app needs can't be rendered:

- **A missing lens / read-model field is NOT a Lattice gap — it's PACKAGE work** the App-Verticals stream
  builds itself (add the lens/DDL to the vertical's package). File it in **verticals.md** as a package+FE item.
  Never file "have the app read Core KV" (violates P5).
- **A missing platform PRIMITIVE the package can't provide** — a cypher-**engine** capability, an **op/kernel**
  mechanism, a **substrate** feature, an **orchestration** capability (e.g. `@every`) — **is** a Lattice gap.
  File it in **lattice.md** (Lattice lane), tagged with the requesting vertical + why, and mark the dependent
  vertical item **`🚧 blocked-on:`** it. **No-paper-over:** never hack the missing primitive into the
  package/FE and call the vertical item done — a vertical builds *on* real Lattice capabilities. You (the PO)
  are the primary, grounded driver of Lattice demand.

## 4. File scored items into the right lane

- **Vertical items** (features / gaps / bugs — package + FE) → **`planning-artifacts/backlog/verticals.md`**.
- **Discovered platform-primitive gaps** → **`planning-artifacts/backlog/lattice.md`** (tagged with the
  vertical; block the dependent vertical item on it).

Each item is **one capped row** — `Item · What (one line) · Vertical · Owner · Imp · Size · State` —
**scored** (Imp ★ / Size), **deduped** (don't refile), tagged FE (Sally + FE Engineer) / package
(Package Designer) / platform (Surveyor + Lattice Steward). **The board is an index, not a journal** (§5 of
the swimlanes design / the CLAUDE.md no-changelog rule): keep the What to one line — grounding goes in the
linked refs, never narrated into the cell. Your **PO note is ONE dated line** in `verticals.md` (≤~25 words —
what you exercised + what you filed + what's next), **not a multi-line run-log** (the findings ARE the filed
rows; live-stack observations go in the commit message, not the board). `scripts/lint-board.go` caps the
PO-notes section — **run it before you push.** **Docs in `main`, not a worktree** (isolation rule) — edit the lane files directly in
`main`; commit **docs-only, scoped** (never `git add -A`): `git pull --rebase` → `git add` the lane file(s) you
touched → commit (`docs(backlog): PO discovery — <vertical>`, ending with a `Co-Authored-By:` trailer naming
**whichever model you are** — check your own system prompt, never hardcode a specific model, a different one
may run a future fire)
→ `git push`. If you see modified files you didn't touch, leave them.

## 5. Bounds

Never build, commit code, design, or touch frozen contracts — your **only** commit is the docs-only lane-file
filing (§4). Don't flood the board: a handful of high-value items per run, not dozens. If you found nothing new,
say so and stop — don't manufacture noise or an empty commit.
