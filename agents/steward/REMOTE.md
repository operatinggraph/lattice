# Steward — REMOTE rider (Claude Code remote container)

**When this applies:** the Steward fire runs in a Claude Code remote session (fresh-cloned repo in an
ephemeral cloud container) instead of on Andrew's Mac. Everything in `SKILL.md` still binds **except**
the environment assumptions below — each delta here was earned in a real remote fire (2026-07-04/06),
not speculated. Read `SKILL.md` first; this file only overrides.

## 0. What is NOT in the clone (gitignored, Mac-local)

`/.claude`, `/_bmad`, `/.agents` are gitignored — a remote session never sees them. `CLAUDE.md` is
committed (readable remotely). The scheduled-task closure — `steward`, `designer`, `owner`, `fe-engineer`,
`lamplighter`, `whetstone`, `surveyor`, `vertical-po`, and everything they reference — is self-contained
under `agents/`, including the three BMAD personas the roles invoke: `bmad-agent-architect` (Winston, via
`designer`), `bmad-agent-ux-designer` (Sally, via `fe-engineer`/`steward`), `bmad-agent-qa` (Quinn, via
`whetstone`) — hand-mirrored from `_bmad/`, no external config dependencies. Invoke them exactly as you
would locally.

- The house rules the `agents/*/SKILL.md` files quote inline (no-changelog rule, `docs/vendors.md`
  pointer, key-shape invariants) still bind — they're restated in the committed skills.
- Review is handled by the roles themselves (steward's risk-scaled lead/3-layer-adversarial call,
  designer's pre-build gate) — not by invoking bmad's story-loop or review skills. Those stay local-only
  in `_bmad/` (Andrew still runs that chain there) and are outside the remote closure entirely.
- Anything else under `.claude/` / `_bmad/` / `.agents/` (other BMAD personas/workflows, local hooks,
  IDE-specific config) is still genuinely unavailable. If one turns out load-bearing for the fire,
  **ask Andrew to commit it** — don't guess its contents.

## 1. Isolation: worktree → fire branch; docs still in `main`

- **CODE** builds on the session's designated `claude/<fire>` branch (the remote substitute for the
  worktree), merged to `main` when gates are green, then **pushed** — an unpushed merge does not exist.
- **DOCS** (board, design docs) still commit directly to `main`, but `main` moves under you mid-fire
  (other fires push concurrently): **always `git pull --rebase origin main` immediately before every
  `main` push**, retry on rejection (2s/4s/8s/16s). The board lint (`go run scripts/lint-board.go`)
  **exits 0 even on FAIL — read its output**, don't trust `&&`.

## 2. Contract proposals: BRANCH COMMIT, never an uncommitted tree

The SKILL's "edit contract in `main`, UNCOMMITTED" protocol assumes Andrew shares your filesystem. A
remote container's working tree is **invisible to Andrew and dies with the container.**

- A frozen-contract proposal = **a commit on the fire branch** (branch = `main` + the proposal; the
  branch-vs-main diff IS the proposal; ratify = Andrew merges, reject = delete). In-text
  `📐 PROPOSED — UNRATIFIED` banners on every added section.
- **Corollary (trialed twice):** Andrew's Mac may hold *local uncommitted contract diffs you cannot
  see*. Absence-from-`main` of contract text the design says was staged is **NOT evidence of loss or
  drift — ask Andrew before filing** a drift/loss finding.

## 3. Stack: no shared stack exists — bring up your own, natively

All shared-stack etiquette (never `make down`, detect-don't-clobber, leave it up for Andrew, PO
coordination) is **void**: the container's stack is yours and disposable.

- **Unit + e2e `go test`:** self-contained — NATS is embedded in the test binaries (`nats-server` is a
  Go dependency). `go test ./... -p 4` runs as-is.
- **Postgres-gated tests:** postgresql-16 is installed natively (`/usr/lib/postgresql/16/bin`). Init a
  cluster, start it, `export POSTGRES_TEST_DSN=postgres://…` — full CI-unit parity (CI does the same
  via a service container).
- **Live stack (`verify-kernel` / `verify-package-*` need `NATS_URL` up):** replace `make up`'s two
  compose containers natively — `go run github.com/nats-io/nats-server/v2 -js` (offline from the
  module cache) + the native Postgres + the repo's own component binaries (they already run as native
  processes even under `make up`). Then run the verify targets normally.
- **Docker:** the daemon *starts* (`sudo dockerd &`) but **image pulls are blocked by the environment's
  network policy** (Docker's blob CDN 403s at the egress proxy). Prefer native; if compose parity is
  ever truly needed, the policy change is Andrew's environment setting, not a workaround to build.
- **Attended / destructive-to-shared-stack fires** (e.g. Vault 5b's delivery-boundary reset) are
  **Mac-only**: say so and stop — never simulate them remotely.

## 4. Ephemerality is the prime directive

Nothing outside pushed git survives the session. **Push the fire branch before long test runs**;
checkpoint the design doc (and push) **before** ending or handing off; never leave a deliverable only
in the working tree. The build lock is void (own container) — push races replace it (§1 rebase-retry).

## 5. FE / browser checks

Headless Chromium + Playwright are preinstalled (`PLAYWRIGHT_BROWSERS_PATH`; never `playwright
install`). Vertical apps run against the ephemeral stack; FE verification is possible, just headless.

## 6. Fresh-context invocation

A remote Steward fire needs exactly: **"As Steward (<lane>), implement <item / Fire N> from
`<design-doc path>`"** — the design doc's checkpoint section is the only cross-session memory (write
yours before exiting), and this rider covers the environment. No conversation history required.
