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
  `sessionStorage` unless a test explicitly injects one). The guard must sit *inside* the `try`, not
  outside it: `sessionStorage` is a resolvable global whose getter can throw `SecurityError` (site data
  blocked, a sandboxed/partitioned frame), so `typeof sessionStorage === "undefined"` itself can throw —
  and since `loadStoredReveals()` runs at classic-script top level, an uncaught throw there aborts the
  rest of `app.js`'s evaluation and the app fails to boot.
- **`signOut()`'s exit is not symmetric with `onAuthDeath()`'s, and that difference is load-bearing.**
  `signOut` → `POST /api/logout` → `main.go`'s `OnSignOut: engines.Purge` → `enginemanager.go`'s `Purge`,
  which evicts the identity's whole engine **and deletes its durable intent-queue file**
  (`os.Remove(StoreDir/<identityID>.db)` — the same store `agent.Enqueue` writes to). A ceremony write
  still `queued` at an explicit sign-out is **destroyed, not landed**: no identity is ever armed by that
  path, and the next login for that identity gets a brand-new engine with an empty outbox, so no confirm
  frame for the old requestId will ever replay. `onAuthDeath()` (`app.js`, session/token expiry or a
  network-triggered auth death) is the OTHER exit and does neither of those things — it does not call
  `/api/logout` and does not touch the server-side engine or store at all. A write queued before an
  auth-death event survives server-side and **will** confirm later, and that confirm frame **will**
  replay to the next `/api/feed` connection for that identity's engine. Pre-fire, `onAuthDeath` left
  `pendingReveals` unpurged, which was harmless because the in-memory Map died with the page at
  `location.replace`. Post-fire, with the hold persisted to `sessionStorage`, it is no longer harmless:
  `sessionStorage` survives a same-tab navigation, so the plaintext now outlives a session's end and
  rehydrates for whoever loads the app next in that tab — including a *different* identity signing into
  the same shared/kiosk tab. That is a new exposure window on exactly the threat model §4.4 exists for.
- **`sessionStorage` is disk-backed, not a RAM-equivalent.** Chromium persists Session Storage to a
  LevelDB on disk (surviving a browser crash and "reopen closed tab" / session restore); Firefox similarly
  persists it for session restore; "Duplicate Tab" clones it into the new tab. It is *tab-scoped* and
  cleared on a genuine tab close, which is the property this fire's reload-recovery actually needs — but
  it is not equivalent to the browser-process memory `pendingReveals` lived in before, and the "adds no
  new exposure class" framing below is corrected accordingly: the exposure class is bounded (same-origin
  JS access, same as the DOM reveal already grants), but it is not *unchanged* from pre-fire.

## 3. Decision (Winston, this fire)

Two independent, additive changes — reload-recovery is not a substitute for the signal, because the
signal is what covers the case reload-recovery deliberately does not (sign-out):

**(a) Reload recovery — persist the hold in `sessionStorage`, tab-scoped, timestamped for eviction.**
The plaintext is already displayed once in cleartext in the DOM (`showCeremonyReveal`, a
`<pre data-reveal-secret>`) — the same JS-same-origin trust boundary `sessionStorage` sits behind. Every
hold carries an `at` timestamp; a rehydrate older than 24h is dropped rather than trusted (nothing else
reaps a hold whose write never reaches a terminal state — a host restart drops `feed.outbox`'s in-memory
state, `trimOutboxLocked` ages out old terminal entries past 200, and an explicit sign-out deletes the
store entirely, per the engine-purge finding above). `holdCeremonyReveal` / the terminal branches of
`settleCeremonyReveal` / `purgeCeremonyReveals` all sync a `PENDING_REVEALS_KEY` `sessionStorage` entry;
a fresh page load rehydrates `pendingReveals` from it before the SSE reconnect can replay the confirm
frame.

**Both exits purge the client-held plaintext — `signOut()` (unchanged) AND `onAuthDeath()` (new).**
Any auth-boundary exit — voluntary sign-out or involuntary auth death — wipes `pendingReveals` (converted
to `{lost:true}` markers, not deleted outright — see (b)) and its `sessionStorage` mirror. This closes the
cross-identity exposure the engine-purge finding above identified, and it is also what gives (b) a
reachable consumer: because `onAuthDeath` (unlike `signOut`) does not touch the server-side engine or
store, a marker created there CAN be settled by a later replayed confirm frame — the sign-out path's
marker, by contrast, can never be settled (see below), which is a known, accepted gap.

**(b) The signal — a purge-before-settle keeps a `{lost: true}` marker, not a deletion; loss is tracked
by requestId, not by frame object.** `purgeCeremonyReveals` converts every still-pending entry to a
`{lost: true, title, at}` marker (plaintext dropped, title kept for context) instead of deleting it, so a
later confirm frame for that `requestId` can still reach `settleCeremonyReveal` and tell the two cases
apart: genuinely-unrelated write (`pending === undefined`, unchanged, stays inert — the pinned test keeps
passing) vs. a ceremony write whose secret is confirmed-but-unrecoverable (`pending.lost`). Because
`cmd/facet/feed.go` marshals outbox frames from a shared pointer in a writer goroutine, a `confirmed`
request routinely receives 2-3 near-duplicate frames in a burst — tracking loss on the mutable frame
object itself (`e.secretLost`) would let a later duplicate silently erase the flag when it replaces the
object in `state.outbox`. Loss is tracked in a separate `requestId → timestamp` map, checked by requestId
wherever the flag matters, immune to which frame object is currently held. The confirmed-but-lost case:
(1) does **not** call `showCeremonyReveal` (nothing to show), (2) fires a `toast`, and (3) keeps its
Activity entry **pinned** in the main view (not auto-archived into collapsed Outbox history like an
ordinary confirm) with a "Secret was not shown — issue a new one" note and a Dismiss action, so it is
discoverable whether or not anyone was watching at the moment the delayed confirm landed.

**Known, accepted gap — the sign-out path's marker can never be settled.** `signOut`'s `/api/logout`
deletes the identity's engine and durable store outright (see above), so a marker created by `signOut`'s
purge has no future confirm frame to ever reach — it is inert by construction, not a bug. This is
acceptable: `signOut`'s purge exists for the confidentiality wipe (which it delivers unconditionally, sign-
out or not), not for the loss-signal (which `onAuthDeath` genuinely delivers, since that path leaves the
server-side write alive to eventually confirm).

**Known, accepted gap — the in-page (wasm/EDGE.5) host.** `internal/edge/browser/feed.go` documents that
an unknown requestId on that host is a silent no-op: its outbox dies with the page, so a rehydrated hold
surviving a reload on that host has no future confirm frame either, the same shape as the sign-out gap
above. Reload-recovery's *benefit* (claim a) is Go-host-only until a future fire gives the in-page engine
its own outbox-replay-on-reconnect. Not fixed here — filed as a follow-up, not silently dropped.

**Non-goals (this fire):** a server-side/durable record of ceremony requestIds (the `{lost}` marker plus
the requestId-keyed loss map is sufficient and needs no new server state); auto-reissue of a fresh secret
(an operator action, not this fire's job); closing the in-page-host gap above (needs a materially
different mechanism on that host, not a client-side JS change here).

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

## 6. Adversarial review — what it found, and the close

Full 3-layer cold review (Blind Hunter / Edge Case Hunter / Acceptance Auditor) ran against the first
build, since this touches secret-handling logic. All three independently converged on the same two
defects (the `onAuthDeath` purge gap and the outbox-card marker being unreachable dead code), and the Edge
Case Hunter additionally traced `signOut` → `engineManager.Purge` and found the sign-out marker path could
never be settled at all — the finding that reshaped §2/§3 above. A fix round closed all of it:

- `onAuthDeath()` now purges (§3 "Both exits purge…") — `app.js` around the `onAuthDeath` function.
- Loss is tracked in `lostSecretRequestIds` (a bounded, requestId-keyed `Map`), never on the outbox frame
  object, closing the duplicate-frame race the frame-object flag was vulnerable to.
- `renderActivity`'s pinned filter keeps a lost-secret entry visible in the main Activity view (not just
  collapsed history) until the operator dismisses it; `outboxCard` offers Dismiss (not Review — a landed
  write is not a resubmittable draft).
- `persistPendingReveals`/`loadStoredReveals` moved their `typeof sessionStorage` guard inside the `try`
  (a real browser can throw `SecurityError` on that access, not just return `undefined`).
- Holds carry an `at` timestamp; `loadStoredReveals` drops (and writes back the drop of) anything past
  `revealHoldMaxAgeMs` (24h) rather than trusting an unbounded rehydrate.
- Rehydrate validation tightened to `typeof reveal.plaintext === "string" || reveal.lost === true`.

Each fix was mutation-tested (revert → a specific test fails → re-apply), and `cmd/facet/web/ceremony.test.mjs`
gained a `frameHarness()` driving the real `applyOutboxFrame`/`renderActivity` rather than only the
lower-level `settleCeremonyReveal`, which is what let the dead-code and duplicate-frame findings slip
past the first build's own (real, but lower-level) tests. Final gate run (independently re-verified,
not taken on the builder's report): `go build ./cmd/facet/...` clean, `node --test cmd/facet/web/*.test.mjs`
221/221, `lint-web` 0 issues, `STRICT=1 lint-conventions` 0 issues.
