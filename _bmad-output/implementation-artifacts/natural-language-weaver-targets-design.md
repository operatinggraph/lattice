# Natural-language Weaver targets — the real capability-author adapter + the Describe entry

**Status: 📐 DRAFT — awaiting Andrew** (fork class: first third-party secret + first paid external API call; not Winston-adjudicatable).
**Designer:** Winston (chat-tasked by Andrew, 2026-08-21 — "let a human describe what they want Weaver to do, let AI do the rest").
**Vision anchor:** the vault's *Why AI Loves the Lattice* §4 — "give the Weaver a Natural Language Target State"; the buildable form of it is compose-time authoring, not runtime LLM-in-the-loop.
**Builds on (all shipped):** [ai-authored-capabilities-design.md](ai-authored-capabilities-design.md) Fires 1–2 + the platform-protected-package guard (`c51746ec`); [weaver-target-studio-design.md](weaver-target-studio-design.md) F25 (Author/check/propose); the weaver-target `.description` aspect (2026-08-21 fire — the durable landing slot for NL intent).
**Companion precedent:** [augur-design.md](augur-design.md) §3.5/§11 — the other model-backed bridge adapter this design's substrate must also serve.

## For Andrew (ratify in one look)

1. **The bridge becomes the platform's first third-party-secret holder and first paid API caller.** The real `capabilityAuthor` adapter lives in `internal/bridge` (registered in `cmd/bridge`), calls the Anthropic API via the official Go SDK, and reads `ANTHROPIC_API_KEY` from the bridge's env — the bridge's existing env-only config posture, and the component whose whole purpose is external side-effects (its idempotency/redelivery/Health machinery is exactly the cost-control we need). Loupe never holds the key and never calls a model. **Registration is explicit env opt-in; with no key set, the shipped `FakeCapabilityAuthor` posture is unchanged** — ratifying this design changes nothing until you set the env.
2. **Spend posture (numbers to ratify):** default model `claude-opus-5` (authoring is intelligence-sensitive + low-frequency — the original design's argument; its 2026-06 pin of `claude-opus-4-8` predates Opus 5; model stays param-overridable per the Augur `params["model"]` precedent). Per-call `max_tokens` cap 16k; bounded self-repair (≤2 model calls per request); daily request cap via env (`BRIDGE_AUTHOR_DAILY_CAP`, default **20**/day) — over-cap requests Nak-with-delay and author when the window rolls (the idempotency key dedups; nothing is dropped). Token usage lands in bridge Health metrics.
3. **The record-time self-attestation hole closes.** Today the adapter's `validation.state` is trusted verbatim (`fake_capability_author.go:120` self-attests "valid"; flagged residual, ai-authored-capabilities-design §8 Fire 2). The real adapter computes the verdict by calling the same deterministic `pkgmgr.ValidateCapabilityArtifact` path Loupe's approve uses, via a constructor-injected validator — **model output is never trusted for the verdict**, and an artifact that still fails validation records as `invalid`, visibly, exactly like today's malformed-result path. Approve-time re-validation stays (defense in depth).
4. **Autonomy boundary unchanged:** propose-only. The adapter authors proposals; a human approves in the review console; the operator applies. Fire 5 auto-apply stays design-only/Andrew-gated; the protected-package guard stays in force.
5. **Out of scope, unchanged:** the approve→apply TOCTOU residual (`DefinitionForCapabilityArtifact` re-checks no scope) is an apply-path issue already tracked in the AI-authored-capabilities design; this design neither fixes nor worsens it.

## 1. Problem & intent

The Weaver Target Studio's Author stage is twelve free-text fields plus hand-written openCypher — every value (targetId token rules, `missing_<gap>` column names, per-action required fields, `row.<column>` templating, the key-composition idiom in the RETURN) must be known from memory, and validation arrives only on a server round-trip. It is operable by us; it is not operable by an operator.

The intent: the operator writes **what they want in plain language**; the platform authors the structured `{weaverTarget + violation lens}` bundle; the human reviews, optionally edits in the Author form, approves. The NL intent persists onto the installed target as its `.description` aspect, so the roster answers "what is this?" forever after.

## 2. Why this is nearly free (what already ships)

The AI-authored-capabilities pipeline was built for exactly this and is idle for want of a brain and a doorway:

- **Capture:** `RequestCapabilityAuthoring {proposalId, intent (required free text), contextRef?}` → `vtx.capabilityproposal.<id>` + `.request` (`packages/capability-author/ddls.go:441-462`). *No production caller exists.*
- **Dispatch:** `capabilityAuthorDispatch` weaver target → `capabilityAuthorPending` lens (`missing_authoring` = no claim ∧ no artifact) → `capabilityAuthor` Loom externalTask → bridge `handleExternal` → adapter `Execute(Request{Params: {requesterId, intent, contextRef}})` (`targets.go:12-20`, `patterns.go:22-41`, `internal/bridge/dispatch.go:133-264`).
- **Record:** adapter's `CapabilityAuthorProposal{Kind, Content, Target, Rationale, Confidence, Validation, Provenance{Model, PromptHash, CatalogHash, ReasonedAt}}.Encode()` → `RecordCapabilityProposal` (never fails post-Ack; bad results record as `invalid`) (`capability_author_proposal.go`, `ddls.go:562-697`).
- **Review + apply:** Loupe review console (ai/operator source badge, provenance section, fresh server-side re-validation on approve, F-004 two-commit apply, protected-package guard at three chokepoints) (`cmd/loupe/review.go`).
- **CI brain:** `FakeCapabilityAuthor` — memoized, refusal-path, zero-spend — stays as the test/default double; `FakeAugur` is its structural twin awaiting the same substrate.

What does not exist (verified 2026-08-21): an entry point; a registered real adapter (`cmd/bridge/main.go:139-148` registers neither `capabilityAuthor` nor `augur`); any model-call substrate (no HTTP client, prompt builder, structured-output helper, or spend accounting in the repo — the only third-party HTTP anywhere is the JWKS poller); real `promptHash`/`catalogHash` computation; a proposal→Author-form edit path.

## 3. The shape

### 3.1 `internal/reasoning` — the shared model-call substrate (new package)

One package both real adapters (capabilityAuthor now, augur later) build on. Wraps the official Anthropic Go SDK (`github.com/anthropics/anthropic-sdk-go` — new `docs/vendors.md` row at build time, version-pinned):

- **Client:** constructed from env (`ANTHROPIC_API_KEY`); absent key = substrate unavailable (constructors return a sentinel the caller uses to fall back to the fake). Streaming under the hood with final-message collection, so long thinking turns don't trip HTTP idle timeouts.
- **Structured output:** the artifact envelope is a strict tool schema (`strict: true` + `additionalProperties: false`) — the model can only emit a well-formed `{kind, content, rationale, confidence, description}` shape; JSON is parsed, never string-matched.
- **Determinism accounting:** `promptHash` = sha256(prompt-template version ‖ assembled prompt); `catalogHash` = sha256(canonically-sorted catalog snapshot). Computed here, stamped into Provenance — the fields stop being `"fake-*"` literals. (Stale-detection *comparison* remains future work; approve-time re-validation stays the drift defense, unchanged.)
- **Spend:** per-call `max_tokens` cap; a rolling daily-window request counter (cap from env); usage tokens surfaced to the caller for Health metrics. No retry loops of its own — the bridge's Nak/redelivery machinery is the retry layer, and the adapter's idempotency memo is the dedup boundary, both already shipped.

### 3.2 The real `capabilityAuthor` adapter

`internal/bridge/capability_author.go`, same `Adapter` interface, registered in `cmd/bridge/main.go` **only when the substrate is available** (env opt-in; otherwise the fake registers, byte-identical to today). Constructor-injected dependencies, following the `NewFakeDocGen(conn, bucket, cap)` precedent:

1. the `reasoning` client,
2. a **catalog source** — reads the `capability-author-context` bucket (a lens read, P5-clean),
3. a **validator closure** built in `cmd/bridge/main.go` from the same three `pkgmgr.ValidateCapabilityArtifact` dependencies Loupe's `freshCapabilityVerdict` constructs (`cmd/loupe/review.go:551-569`) — keeping `internal/bridge` itself free of a pkgmgr import.

Execute flow (sync — see §3.5): assemble catalog + hash → prompt (intent + catalog + the authoring rules) → strict-schema model call → **deterministic validation of the returned artifact** → if invalid, one bounded repair pass (validator errors fed back; ≤2 model calls total) → emit `CapabilityAuthorProposal` with the *computed* verdict (valid or invalid — never self-attested), real hashes, model id, and `description` distilled from the operator's intent so the installed target carries it. Refusals and exhausted budgets follow the existing outcome semantics (terminal business refusal → `OutcomeFailed`; transient/over-cap → `error` + Nak).

### 3.3 Catalog widening (package edit, `capability-author` bump)

`capabilityAuthorContext` today projects only op self-description columns. Add `spec` and `description` to its projection so the author sees: existing lens bodies (what cypher looks like here, which columns exist), existing weaver-target bodies (style + collision avoidance), and — post the `.description` fire — every target's prose (style examples). The reader filters by class; the lens stays one `MATCH (m:meta)`.

### 3.4 The Loupe half — Describe + edit loop (Loupe lane, Winston-adjudicated UX)

- **Describe panel** on `#/weaver/author`: one textarea + Submit → `POST /api/weaver/author/request` → relays `RequestCapabilityAuthoring` via the Gateway under the operator's token (permission `operator @ any` already granted). The panel links to the review queue where the draft will appear.
- **"Load into Author"** on a weaverTarget-kind proposal detail: hydrates the Studio form from the proposal's artifact `content` (the inverse of the propose mapping — including `description`), so the operator edits, re-checks, and re-proposes as an operator-source proposal; they reject the AI draft from the same screen. Review stays approve/reject/apply for unedited drafts.

### 3.5 Sync now, batch later

The pattern is sync-only (no `DispatchOp`), and authoring is low-frequency: one blocking `Execute` of ~1–3 min is acceptable **provided the events consumer's AckWait tolerates it** — a named build-time verification; if AckWait can't be raised safely, the fire falls back to the established async shape (add a `DispatchOp` mirroring lease-signing's `RecordServiceDispatch` + `Poll`). The Batches API (50% cost, minutes-scale) is the natural cost-optimized follow-on and would ride the same async shape; explicitly deferred.

## 4. Contract surface

None. All touched ops/lenses/patterns/targets are `capability-author` package data (version-bumped); the adapter/registry are bridge-internal; Contract #10 shapes are untouched. The one *posture* novelty — a platform binary holding a third-party secret and spending money — is precisely what the For-Andrew section ratifies.

## 5. Decomposition for the Steward

- **Fire NL-1 (lattice lane, L): the brain.** `internal/reasoning` + the real adapter (validator-injected, catalog+hashes, repair pass, spend caps, Health metrics) + `cmd/bridge` opt-in registration + the context-lens widening + colocated tests (fake-backed CI; a build-tag or env-gated live smoke test, never in default CI). Full 3-layer adversarial review (capability plane). Named build-time verifications: AckWait vs sync call; module-boundary check on the validator injection; vendors.md row.
- **Fire NL-2 (loupe lane, M, blocked-on NL-1): the doorway.** Describe panel + request relay + "Load into Author" hydration + goja/Go tests. Sally UX pass, Winston-adjudicated (lane delegation, 2026-07-02).
- **Deferred (named consumers):** Batches cost path (consumer: spend dashboards once volume exists); catalogHash stale-flagging in review UI (consumer: reviewers of long-pending proposals); the real `augur` adapter riding `internal/reasoning` (consumer: augur-design §11 — unblocks for free once NL-1 lands).

**Deploy note (not code):** the dev stack never provisions `capability-author`'s grants (`make install-ai` is opt-in; `cap.role-by-operation.*` empty ⇒ every op rejects — known environment gap, loupe lane 2026-08-02). Live verification of either fire needs `install-ai` on the target stack.

## 6. Risks & self-adversarial pass

- **Prompt-injection via the catalog** (a lens description authored by a tenant flows into the prompt): the catalog is meta-vertex self-description written by package authors/operators, not tenant data; the adapter's output can only become a *proposal* that deterministic validation + human review + the protected-package guard bound. Residual accepted.
- **Model emits a plausible-but-wrong target** (valid shape, wrong semantics): unchanged trust model — same risk as a human author today; the review console + Trial panel (born-disabled targets) are the containment. The `description` on the draft makes wrongness *legible*.
- **Key leakage:** the key lives only in the bridge's env (compose secret), never in Loupe, never in KV, never logged; the reasoning package must redact it from error strings — named review item for NL-1.
- **Cost runaway:** idempotency memo (dedup) + Weaver anti-storm mark (dispatch-side) + daily cap (adapter-side) + `max_tokens` (call-side) — four independent bounds, three of which already ship.
- **Rejected alternative — Loupe calls the model directly:** wrong trust shape (console holding spend keys), wrong reuse shape (Augur couldn't share it), and it would bypass the request/claim provenance chain the pipeline already gives us.

## 7. What lands where

| Piece | Where | Lane |
|---|---|---|
| `internal/reasoning` (SDK wrapper, hashes, spend) | new package | lattice |
| Real `capabilityAuthor` adapter + registration | `internal/bridge`, `cmd/bridge` | lattice |
| Context-lens `spec`/`description` widening | `packages/capability-author` (bump) | lattice |
| Describe panel + request relay | `cmd/loupe` + `web/js` | loupe |
| Proposal→Author hydration | `cmd/loupe/web/js` | loupe |
| Vendors row (Anthropic Go SDK pin) | `docs/vendors.md` | with NL-1 |
