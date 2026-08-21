# Model-runner

**Component reference** | Audience: implementers + architects

> Design of record: `_bmad-output/implementation-artifacts/natural-language-weaver-targets-design.md`
> (§3.1 + For-Andrew — the ratified credential/egress posture). The wire shapes below are
> `internal/modelrunner/wire` package data, not a frozen contract. Update this page in the same commit
> as the code; drift between page and code is a documentation bug.

---

## Overview

The model-runner is the platform's **sole external-model egress and sole third-party-credential
holder**: a small standalone binary serving one NATS micro-service endpoint in a **queue group**, so N
instances load-balance and fail over with no coordinator. Callers (today: the bridge's
`capabilityAuthor` adapter; later: the real Augur adapter) send a **domain-free** generation request;
the runner calls the vendor (Anthropic API, official Go SDK) with the request's single strict tool
schema, and lands the structured result in the `model-results` KV bucket for the caller to poll.

The runner knows nothing about capabilities, targets, or the Augur — it is "a model call as a NATS
service". Domain assembly (catalog, prompt, validation of the returned artifact) stays caller-side.

**It holds no NATS authority beyond its own surfaces**: subscribe on `svc.model.>`, write
`model-results` + its own Health keys. It never reads or writes Core KV, and it submits no operations.

## Wire contract (`internal/modelrunner/wire`)

- **Request** (`svc.model.generate`, queue group `model-runners`): `{ref, model?, maxTokens?, system,
  prompt, tool {name, description, inputSchema}}`. `ref` is the caller's opaque correlation token and
  becomes the result key; `model` defaults to the runner's configured default (`claude-opus-5`).
- **Ack** (immediate micro reply; never blocks on the vendor): `{status: accepted|busy|invalid, ref}`.
  `busy` = worker pool saturated or the daily cap reached — the caller's own retry/timeout machinery
  governs; nothing is queued runner-side.
- **Result** (KV `model-results.<ref>`, per-key TTL): `{state: inflight|completed|refused|failed,
  output?, model, usage {inputTokens, outputTokens}, refusalCategory?, error?, completedAt}`.
  `output` is the strict-tool input JSON verbatim — the model cannot answer in any other shape.

**Idempotency / double-spend guard:** the runner CAS-creates the `inflight` marker at `<ref>` before
any vendor call; a redelivered or duplicate request for a ref that exists acks `accepted` and spends
nothing. The runner is the **only writer** of `model-results` — no other component holds write on the
bucket, so no second party can forge or erase an outcome. Consumers only read; **per-key TTL is the
reaper**: the in-flight marker carries a short TTL (2× the vendor timeout, so a runner killed mid-call
never strands its ref), terminal results carry 7d.

## Spend posture (ratified numbers)

Per-call `maxTokens` capped at 16384; daily vendor-call cap `MODEL_RUNNER_DAILY_CAP` (default 20),
enforced via a CAS-incremented `__usage.<UTC-day>` counter key in `model-results` — over-cap requests
ack `busy` and cost nothing. Usage tokens from every response are surfaced in Health metrics. The
runner deliberately enables **no** fallback re-serve: a refusal records as `refused` with its category,
and provenance names exactly the model that answered.

## Environment

`NATS_URL` · `NATS_NKEY` (deploy/nkeys/model-runner.nk) ·
`ANTHROPIC_API_KEY` (**required** — a keyless runner exits at startup; the key exists only in this
process's env and is redacted from every log/error path) · `MODEL_RUNNER_DAILY_CAP` (default 20) ·
`MODEL_RUNNER_MAX_CONCURRENT` (default 2).

Launch: `make model-runner` (pgrep-guarded background launch; deliberately **not** part of the
`orchestration` tier — deploying the runner is the opt-in that turns the AI-authoring path on).

## Health

`health.model-runner.<instance>` heartbeats via `internal/healthkv` (Contract #5 conventions; schema:
`docs/observability/health-kv-schema.md` → Model-runner). Metrics: accepted/busy/completed/refused/
failed totals, vendor token counters, and the current day's call count.

## Non-goals

No Core KV access (not on the P5 inspector/platform-read list, and needs none) · no domain knowledge ·
no queuing (busy is a real answer) · Batches API cost path deferred (design §5) · production mTLS per
the parked NATS-write-restriction row.
