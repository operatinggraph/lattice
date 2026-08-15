# Facet — a queued ceremony reveal must survive a reload, and its loss must never be silent

**Status:** ✅ Winston-ratified — build-ready (implementation-level decision, no frozen contract, no
architectural fork; decided per `agents/steward/SKILL.md` §0).

Board row: `_bmad-output/planning-artifacts/backlog/lattice.md` — "[Facet] A durably-queued ceremony
write outlives the plaintext it minted." Imp ★★, Size S-M.

## 1. Scope sentence (verbatim)

> The mint-and-reveal ceremony (`client-ceremony-op-descriptors-design.md` §4.3, implemented in
> `cmd/facet/web/app.js`) holds its minted plaintext in an in-memory `pendingReveals` Map only. A page
> reload between enqueue and the outbox `confirmed` frame loses the plaintext even though the write is
> already durably queued and will land regardless; a sign-out does the same, deliberately, per the
> `facet-app-ux.md` §4.4 purge. Either way the confirm frame today calls `settleCeremonyReveal`, finds
> nothing held, and returns silently — the identity is armed with a secret nobody has, with no signal
> anywhere. Fix: (a) persist the hold in `sessionStorage` so an ordinary reload recovers it, and (b)
> replace the silent no-op with a distinct, discoverable signal whenever a ceremony write confirms with
> no recoverable plaintext (purge-before-settle or any other loss), without weakening the §4.4 sign-out
> purge itself.

## 2. Grounded mechanism (verified live, this fire)

- `mintSecret()` (`cmd/facet/web/app.js:1944`) generates the plaintext client-side; only its SHA-256 hash
  is ever sent to the server (`submitDescriptorForm`, confirmed live by the pinned test "the minted
  plaintext must not appear anywhere in the enqueued request").
- `enqueueOperation()` (`app.js:2495`) does `POST /api/enqueue`; the handler
  (`cmd/facet/server.go:149-220`, `handleEnqueue`) hands the envelope to `eng.agent.Enqueue`, which
  persists it to the Edge device's own durable SQLite queue (`internal/edge/agent`) — durability is
  device-scoped from that point, fully decoupled from the browser tab's lifetime.
- The plaintext is held only in the browser's `pendingReveals` Map (`app.js:1959`), set by
  `holdCeremonyReveal` right after a successful enqueue (`app.js:2625`).
- Confirmation arrives as an `outbox` SSE frame. `cmd/facet/feed.go`'s `feed.outbox` map is **server
  (device)-process-scoped, not per-browser-tab or per-cookie** — `snapshotOutbox()` (`feed.go:342`)
  replays the full current outbox state (queued/submitting/confirmed alike, until
  `trimOutboxLocked` at 200 entries) to every fresh `/api/feed` connection, explicitly "a page refresh's
  'what's outstanding'" (`feed.go:342` comment). So a reload's new SSE connection **will** redeliver the
  eventual `confirmed` frame for a write enqueued before the reload — the write was never in doubt, only
  the reveal.
- `applyOutboxFrame` (`app.js:1959` region) calls `settleCeremonyReveal(requestId, state)`
  (`app.js:1983`) for **every** outbox frame, ceremony or not — it distinguishes by whether
  `pendingReveals` holds an entry for that `requestId`. Today: no entry → `return` (`!pending`), silent,
  indistinguishable from "this was never a ceremony write" (the pinned test
  `cmd/facet/web/ceremony.test.mjs:309` "an outbox frame for a request with no held secret is inert"
  covers exactly that ordinary-write case and must keep passing).
- The in-code comment at `app.js:1955-1958` ("a reload drops an unrevealed secret, which is the correct
  failure — the write either landed … or it did not") is **wrong**, refuted by the mechanism above: the
  write's landing is decoupled from the browser's memory lifetime, so "did not land" is not the
  alternative to "lost the reveal." That comment is corrected as part of this fire, not left standing.
- Precedent for testable, DI-friendly storage access in this codebase:
  `cmd/facet/web/boot.mjs:59` — `resolveDeviceId(storage = globalThis.localStorage)`. This fire follows
  the same shape (a `typeof … !== "undefined"` guard, since `cmd/facet/web/*.test.mjs` run app.js inside
  a `vm.createContext` sandbox — see `ceremony.test.mjs` header comment — that does not define
  `sessionStorage` unless a test explicitly injects one).

## 3. Decision (Winston, this fire)

Two independent, additive changes — reload-recovery is not a substitute for the signal, because the
signal is what covers the case reload-recovery deliberately does not (sign-out):

**(a) Reload recovery — persist the hold in `sessionStorage`, tab-scoped.** The plaintext is already
displayed once in cleartext in the DOM (`showCeremonyReveal`, a `<pre data-reveal-secret>`) — the same
JS-same-origin trust boundary `sessionStorage` sits behind, and it is auto-cleared on tab close, so this
adds no new exposure class. `holdCeremonyReveal` / the terminal branches of `settleCeremonyReveal` /
`purgeCeremonyReveals` all sync a `PENDING_REVEALS_KEY` `sessionStorage` entry; a fresh page load
rehydrates `pendingReveals` from it before the SSE reconnect can replay the confirm frame. **Sign-out
still purges it** (§4.4 is a deliberate security wipe on a shared/kiosk device — not the bug this fire
closes, and not weakened here).

**(b) The signal — a purge-before-settle keeps a `{lost: true}` marker, not a deletion.**
`purgeCeremonyReveals` today deletes every pending entry outright, which is indistinguishable from
"never held." Replacing the deletion with a `{lost: true}` marker (title kept for context, plaintext
dropped) lets a later confirm frame for that `requestId` still reach `settleCeremonyReveal` and tell the
two cases apart: genuinely-unrelated write (`pending === undefined`, unchanged, stays inert — the pinned
test keeps passing) vs. a ceremony write whose secret is confirmed-but-unrecoverable (`pending.lost`).
The latter now: (1) does **not** call `showCeremonyReveal` (nothing to show), (2) fires a `toast` so an
operator watching right then sees it, and (3) marks the outbox entry itself (`e.secretLost = true`)
before it's rendered, so `outboxCard` / `outboxHistoryCard` carry a persistent "Secret was not shown —
issue a new one" note discoverable later in Activity — a toast alone can be missed if nobody's looking
at the moment a delayed confirm lands.

**Non-goals (this fire):** persisting the secret across an explicit sign-out (contradicts §4.4 by
design); a server-side/durable record of ceremony requestIds (the `{lost}` marker is sufficient and
needs no new server state); auto-reissue of a fresh secret (an operator action, not this fire's job).

## 4. Touch-list (verified)

- `cmd/facet/web/app.js` — `pendingReveals` lifecycle (`holdCeremonyReveal`, `settleCeremonyReveal`,
  `purgeCeremonyReveals`), a new `persistPendingReveals`/`loadStoredReveals` pair, `applyOutboxFrame`'s
  call site, `outboxCard`/`outboxHistoryCard` rendering.
- `cmd/facet/web/ceremony.test.mjs` — extend with the purge-then-settle (`lost`) cases and a
  reload-recovery case (inject a fake `sessionStorage` into the sandbox); the existing "inert" test for
  an unrelated write must keep passing unchanged.

## 5. Gates

`go build ./cmd/facet/...`, `make lint-web`, `make test-facet-web` (`node --test cmd/facet/web/*.test.mjs`),
`STRICT=1 go run ./scripts/lint-conventions.go`. No Core KV / contract / P5 surface touched — pure
client-side JS + its Go-served handler is unaffected (no server-side change).
