# A declared read is never scope-checked against the operation — read-scope authorization

**Status:** 📐 **awaiting-Andrew (ratification).** Design/doc-only; nothing built.
**Lane:** Lattice (Stream 2). **Board row:** *[Processor] A declared read is never scope-checked against
the actor's grant* (★★, M-as-filed → **L/XL as designed**, see §9).
**Filed by:** `client-ceremony-op-descriptors-design.md` §12.7 — the residual class-(g) `derive_reads`
made the *platform* exercise on every submitter rather than only the ones that asked.
**Subsumes:** the *[Processor] Whole-set `state` exposure remains an existence oracle for sensitive
classes* row (`sensitive-read-tracker-consumption-design.md` §2.2), filed `🚧 seq behind read-path auth
(D1)` — closed structurally by Increment 1 here, and its D1 sequencing cleared by §2.
**Adversarial review:** two independent passes on the frozen draft (mechanism-feasibility,
security-soundness). **They returned 4 blockers and 9 majors, and reshaped the design** — the draft's
Increment-1 claim was false, its size estimate was wrong by ~3×, and three of its proposed reuses were
mechanisms it had not opened. Findings are folded into the body; §11 records what changed and why.

---

## For Andrew

**What it does, in two lines.** `contextHint` is a client-supplied channel that decides which Core-KV
keys an operation hydrates, and nothing on the write path relates it to the operation being performed:
any actor holding any operation grant can have the Processor read — and, for a `sensitive` aspect,
**decrypt** — any key in the graph. This design makes an operation's read set a **function of the
operation** (its DDL + its own envelope) instead of an independent client channel.

**One reframe, and it removes a fork rather than raising one.** The design that closed the `contextHint`
existence oracle (`3a78c109`) recorded in its §1.2 that the complete answer *"is read-path authorization
(D1) — a named Andrew fork."* **That is a false fork, and the row inherited it.** The write path does not
need "may this **actor** read K" — Core KV has no per-key actor model, and P5 says applications never
read it at all. It needs "may this **operation** read K", which is answerable from package-authored
machinery already shipped. So **no architectural fork is being asked of you**, and the §2.2 row currently
parked behind D1 is unparked by ratifying this.

**The honest size changed under review.** The draft claimed a ~35-op migration that was "mostly
transcription". The real corpus is **105 distinct permissioned operationTypes**, only 47 of which carry a
dispatch descriptor, and the vertical apps (LoftSpace, Clinic, Café, Wellness) hand-build their
`contextHint` in JavaScript rather than from descriptors at all — so for a large fraction there is no
template to transcribe. Increment 1 is S–M; **Increments 2–3 are honestly L–XL**. I am not shrinking the
shape to fit the label (per your standing rule), but you should ratify knowing the difference.

**My recommendation.** Ratify all three; build **Increment 1 now** and let 2–3 queue behind it. Increment
1 is independently valuable, needs no migration and no client change. But I want to be exact about what it
does *not* do, because the draft got this wrong and the review caught it: **Increment 1 closes the
decrypt of a key the operation never names. It does not close the decrypt of a key the operation *does*
name from an attacker-supplied payload field** — that is Increment 2's job, and until then the only thing
containing it is the step-6 external-egress guard (§4.2).

**Frozen-contract change — one file staged UNCOMMITTED, one asked for in words.**
- `docs/contracts/02-operation-envelope.md` §2.5 — **staged uncommitted in `main`** (the diff is the
  proposal): a *Read authorization* paragraph, plus a pointer added where §2.5 currently states the
  un-inspected property as settled fact.
- `docs/contracts/03-mutation-batch-event-list.md` §3.10 — **deliberately NOT staged.** That file already
  carries the *subject-anchored sensitive aspects* design's uncommitted proposal, and interleaving two
  designs' hunks in one file means ratifying either sweeps the other in on a whole-file `git add`. The
  replacement text is quoted verbatim in §7.2 and belongs in the commit that ratifies **this** design.

---

## 1. The gap, grounded

`contextHint` is client-supplied. `ParseEnvelope` (`opwire.go:273-310`) validates the enumeration shape,
the `egressReads`-vs-`reads` exclusion and the 1000-key ceiling. Step 3 authorizes on
`operationType + actor + authContext` and never references `contextHint` (`step3_auth_capability.go`,
`step3_auth_matcher.go` — the dispatch registry keys on `AuthContext` alone). Step 4 then hydrates every
declared key, one Core-KV GET each (`step4_hydrate.go:239-327`).

**The Gateway forwards `reads` and `optionalReads`, and silently drops `egressReads`.**
`gateway.go:786-793` constructs `&processor.ContextHint{Reads, OptionalReads, Enumerations}` — there is no
`EgressReads` field in that literal. So the egress-ref channel is unreachable from the HTTP boundary
today; its only producers are `internal/loom/actuator.go:101-102` and
`internal/refractor/keyshredded/manager.go:474`. **That is containment by omission, not by design** — a
submitter reaching `core-operations` directly is not bound by it, and nothing states the rule. (The draft
said the Gateway forwards `contextHint` verbatim; it does not.)

So: **the set of keys an operation reads is chosen by the client, and no layer relates that choice to the
operation being performed.**

### 1.1 What that is — and, precisely, what it is not

Two things this is commonly assumed to be, which grounding disproves.

- **It is no longer a pre-script existence oracle.** `3a78c109` (deferred-miss hydration) made a
  declared-but-absent key *recorded* rather than faulted; the `HydrationMiss` fires only where the
  operation **names** the key (`step4_hydrate.go:206-232`, `stateMapValue.Get`). **Closed; not re-opened.**
- **It does not widen the OCC condition set.** Step 8 builds one `BatchOp` **per mutation**, conditioned
  on the mutation's own `ExpectedRevision` or the prior doc's revision (`step8_commit.go:194-213`). A
  read-only declared key contributes no condition, so a surplus declaration can neither manufacture a
  `RevisionConflict` nor serve as a change oracle. Nothing to design here.

What remains is one mechanism with four faces.

**(a) An arbitrary actor can cause an arbitrary identity's PII to be decrypted, without naming it.**
Step 4 calls `decryptSensitiveDoc(..., egress=false, ...)` on every present declared key
(`step4_hydrate.go:253` for `reads`, `:292` for `optionalReads`; `:323` is the `egressReads` loop and is
`egress=true`, the ref disposition). For a key whose class resolves to a `sensitive` DDL that reads the
host identity's `piiKey` envelope and performs a Vault decrypt (`sensitive_decrypt.go:213-234`). The
actor needs no relationship to that identity — only *some* grant on *some* op, with the victim's aspect
key in `contextHint.reads`. **This is what Increment 1 closes.**

**(b) An arbitrary actor can cause an arbitrary identity's PII to be decrypted *by* naming it, through a
live read — and Increment 1 does not touch this.** `kv.Read(K)` takes any string the script computes,
issues a live GET and decrypts under the same rules (`starlark_kv.go:115-136`, `:397`). The live instance
is real: `CreateLeaseServiceInstance` takes `subjectKey` **from the payload**, checked only for
`vtx.identity.<id>` shape and liveness (`packages/lease-signing/scripts.go:1136-1178` — **no ownership
guard**, permission `Scope: "any"`, `permissions.go:79-83`), and `resolve_subject_params`
(`packages/orchestration-base/external_params.go:63-76`) reads `subject_key + "." + aspect` where the
**aspect segment also comes from the payload**, feeding an `external.<adapter>` event bound for a vendor.
The `$sensitiveRef` safety that script's own comment claims holds **only** when Loom declared the aspect
in `egressReads` (`internal/loom/externaltask_params.go:79-83`); a directly-submitted envelope has an
empty `egressKeys` set (`starlark_kv.go:396`), so the same read returns **plaintext**. Today the only
thing preventing exfiltration is the step-6 guard (`step6_validate.go:97-114`) firing off
`tracker.plaintextRead`. **This is Increment 2's job, and §5.4 resolves it.**

**(c) The whole-set exposure guard is itself the residual oracle.** The consumption tracker
(`sensitive_decrypt.go:29-70`) flips `plaintextRead` when the script takes a recorded document —
including via `state.values()` / `items()` / `String()`, which hand over every hydrated document without
naming a key. For an op that emits an external event, **a surplus sensitive declaration therefore splits
the outcome on whether the victim's key exists.** That is `sensitive-read-tracker-consumption` §2.2's
filed residual, whose own conclusion is that "only read-scope validation of the declared set closes it."

**(d) The working set is wider than the operation, on the non-sensitive plane too.** Leaking it requires
a script that renders the whole set into a mutation or event. **Census: zero** — `state.values()`,
`state.items()`, `str(state)`, `"%s" % state`, `json.encode(state)` and the `for k in state:`+subscript
pattern return nothing across `packages/**` and `internal/bootstrap` (independently re-run in review).
That is a fact about **today's corpus**, not about the mechanism: the containment holds by accident of
what has been written.

Blast radius is bounded only by the declared-read ceiling — **1000 keys per envelope**, for any actor with
any grant.

---

## 2. The reframe — op-scoped, not actor-scoped

The existence-oracle design deferred this row because read-scope validation "*is* read-path authorization
(D1) — a named Andrew fork" (§1.2 there). Re-derived, that does not survive:

- **Core KV has no actor-scoped read model, and should not grow one.** P5: applications read **lens
  projections**, never Core KV. D1 (`read-path-authorization-d1-design.md`) authorizes the *projection*
  plane — a protected lens, an RLS anchor, a Postgres policy. None of that is addressable to a raw
  `vtx.…` key inside the Processor's hydration loop. "May this actor read key K" would be a **new**
  authorization plane, not an application of D1.
- **The write path does not need that question answered.** Every read exists to serve **one operation**,
  whose script is package-installed and not attacker-controlled. The missing property is narrower:
  *the keys an operation reads should be a function of that operation* — its DDL and its own envelope —
  **not of an independent client channel**.
- **Two package-authored declarations of exactly that already ship, and neither is enforced.**
  `OpDispatchSpec.Reads`/`OptionalReads` (`internal/pkgmgr/definition.go:556-640`) — per-op read
  templates the client resolves into `contextHint`, which the server never re-resolves; and class-(g)
  `derive_reads` (`internal/processor/derive_reads.go`, shipped `8aaea2b9`) — already proof that the
  platform can compute an operation's read set from the operation.

### 2.1 What the property covers — and what it cannot

The draft claimed this makes a declared read "either a read the script itself makes, or refused." **That
was false, and the correction matters enough to state before the mechanism.** `contextHint` is only the
*pre-fetch* channel. An operation also reads through:

- **class-(b)/(c) live `kv.Read`** — any key the script computes at execution time (§1.1b);
- **class-(e) `kv.Links` + follow-ups** — data-derived keys resolved mid-walk, which Contract #2 §2.5
  states are undeclarable by construction, bounded only by the live-read budget
  (`opwire.go:94-97`, `:106-111`; `starlark_kv.go:149-235`, `:304-336`);
- **`contextHint.enumerations`** — a fourth client-supplied channel, validated for shape at parse and
  otherwise inert (no Processor consumer today).

A design that bounded only the declared set would leave an unbounded parallel channel beside it and could
not honestly claim the property. **So `readScope` governs every Core-KV read an operation makes**: it is
checked at hydration *and* at the live `kv.Read` seam, and a class-(e) follow-up is admitted by the
declared enumeration it came from (§5.3). That is the whole reason Increment 2 is larger than the row
suggested.

### 2.2 Why "put K in the payload" is not a bypass — and where that argument runs out

An attacker can name K in a payload field the policy admits. That is the objective, not a hole: the
existence-oracle design established the same pivot — "putting K's id in the payload is precisely the path
the ownership guards stand on." A key reached through a declared payload field is a key **the script
itself reads**, in the branch its guards adjudicate.

**But the guards exist where the author wrote them.** `CreateLeaseServiceInstance` has none on
`subjectKey` (§1.1b) — only a type check and a liveness check. So the safety transfer is real but
conditional, and this design does not pretend otherwise: §5.4 handles that class structurally rather than
relying on a guard nobody wrote.

---

## 3. Shape at a glance

| Inc | What lands | Independent value | Size |
|---|---|---|---|
| **1** | **Naming is the sole gate for sensitive plaintext at hydration** — hydrate as stored; decrypt lazily on `.data` access; whole-set seams redact anything not *proven* non-sensitive | §1.1a and §1.1c closed structurally; no migration, no client change, no contract dependency | S–M |
| **2** | **`readScope` on the DDL** — package-declared per-`operationType` key templates, checked at hydration **and** at the live `kv.Read` seam; wildcard aspect segments force the ref disposition (§5.4); migration + blocking gate | §1.1b and §1.1d closed for every permissioned op | **L–XL** |
| **3** | **Coverage + default-deny** — engine/platform ops declare; a script-bearing DDL with no `readScope` refuses every read; AI-authoring surface carved (§6) | absence stops meaning "unrestricted" anywhere | M–L |

---

## 4. Increment 1 — naming is the sole gate for sensitive plaintext

**The rule.** A `sensitive` aspect's plaintext is produced **only where the operation reads its body**.
Hydration carries the document as stored; the decrypt moves to the point of body access.

This is the **third application of one pivot** the Processor has established twice: naming the key is the
operation depending on it. `3a78c109` used it for the fail-closed fault;
`sensitive-read-tracker-consumption` for the egress guard's flip; this uses it for the decrypt itself.

### 4.1 The mechanism, corrected against the review

The draft proposed decrypting inside `stateMapValue.Get` and memoizing into the state dict. **Both are
wrong**, and the corrections are structural, not cosmetic:

- **Never `SetKey` into the state dict.** `Dict.SetKey` → `hashtable.insert` → `checkMutable` rejects any
  insert while `itercount > 0` (go.starlark.net `hashtable.go:328-338`, `:394`). `stateMapValue.Iterate`
  returns the dict's live iterator, so the legal idiom `for k in state: v = state[k]` would raise
  *"cannot insert into hash table during iteration"* on a script that works today. Memoize in a **side
  map** on `stateMapValue`, consulted before `s.d.Get`. (`Freeze` is *not* the hazard —
  `starlarksandbox.Run` calls `prog.Init` and never freezes the globals, `sandbox.go:223`.)
- **Never decrypt in `Get`.** `in` routes through `Get` and is indistinguishable there
  (`starlark_runner.go:500-503`), and the corpus idiom is `state[K] if K in state else None` — two `Get`
  calls per read, inside loops (`packages/identity-hygiene/ddls.go:286-345`,
  `packages/identity-domain/ddls.go:1156-1434`). Decrypting in `Get` would make a *membership probe* a
  Vault round trip. **Decrypt at body access instead:** a pending-sensitive entry hydrates as a lazy
  document value whose `.key` / `.class` / `.isDeleted` are eager and whose **`.data` decrypts on first
  access**, marks plaintext and consumes. `in`, `keys()` and iteration then cost nothing.
- **Never re-read.** The draft said to reuse `ScriptContext.KVReader`. That is a one-method interface
  (`script_context.go:108-110`) whose implementation issues a fresh live `KVGet` (`starlark_kv.go:384`) —
  using it would re-read at a newer revision, violating the invariant `starlark_kv.go:20-24` protects
  (*"echoing the snapshot revision as expectedRevision is what makes the commit's OCC check sound"*) and
  corrupting the conditions `applyHydratedRevisions` derives (`commit_path.go:409`). The decrypt operates
  **in place on the `VertexDoc` already in hand**, via a purpose-built decryptor closed over
  `(conn, bucket, DDLCache, vault)` threaded onto `ScriptContext`.
- **Classify once, fail closed.** `sensitivePending` is populated on
  `resolveGoverningDDL(...).Sensitive == true` at hydration (`sensitive_decrypt.go:148-151`) and carries
  the resolved `MetaVertexRef`, because there is **no** resolution cache — `decryptSensitiveDoc` builds a
  fresh `&ddlResolver` per call (`:143-147`) and the miss path walks the instanceOf chain with live reads
  (`step6_resolve_ddl.go:197-241`). Splitting classify-from-decrypt without carrying the ref would run
  that walk twice per key.
- **"As stored" is not "ciphertext".** With no Vault wired, step 6.5 never encrypted on the way in, so a
  sensitive aspect sits in Core KV as **plaintext** (`sensitive_decrypt.go:206-216`); a malformed or
  non-identity-anchored sensitive aspect likewise passes through readable (`:162-172`). The name-time seam
  is therefore **decrypt-if-encrypted, mark-and-consume always** — keyed on *sensitive-classed and
  readable*, the same fail-closed predicate `markPlaintext` uses today. Keying it on "I decrypted it"
  would leave step 6's egress guard vacuous for exactly the deployment with no crypto boundary.
- **Bind the decrypt to the execution context and charge it.** `Get`/`Attr` receive no `*starlark.Thread`,
  so `ContextFromThread` (the seam every other impure call uses — `starlark_kv.go:122`, `:210`) is
  unreachable, and `Run`'s outer ctx is *not* the wall-budget-derived `execCtx`
  (`sandbox.go:200-205`). Increment 1 therefore adds a small `starlarksandbox` seam handing the derived
  execution context to the caller, and each on-demand decrypt charges `sc.LiveReads`. Without this a Vault
  stall inside `.data` is unbounded by `DefaultScriptWallBudget` and `thread.Cancel` cannot reach it.

### 4.2 The whole-set seams redact, fail-closed

`state.items()` / `values()` / `String()` hand over every hydrated document without naming a key. Under
Increment 1 they render a **redacted placeholder for every document not *proven* non-sensitive** — i.e.
the only documents rendered in full are those whose governing DDL resolved *and* is not `sensitive`.

That predicate is deliberately the inverse of today's. `resolveGoverningDDL` **fails open** on at least
five paths — unparseable/link key (`step6_resolve_ddl.go:204`), an instanceOf read fault (`:264-268`,
commented *"Fail-open to the permissive default"*), an ambiguous multi-edge chain (`:259/271`), a cycle
(`:216`), hop exhaustion (`:209`) — plus the ordinary permissive-by-default case of a vertex with no DDL
at all (Contract #1 §1.5 step 6). Redacting only *proven-sensitive* documents would inherit every one of
those as an exposure. Redacting everything not proven safe inherits none.

Because nothing sensitive (or unclassified) leaves those seams, `consumeAll`'s flip is **removed** rather
than retained — which is precisely what closes §1.1c's oracle: the operation's outcome no longer depends
on whether a key it never named exists. Retaining the flip as a "backstop" would have preserved the very
oracle the increment exists to close; making the classification fail-closed is what makes removal safe.

Two constraints for the builder: the placeholder must keep the `starlarkstruct.Struct` **shape**
`vertexDocToStarlark` produces (`starlark_runner.go:623-638`) with only `.data` replaced, or `v.data` on a
redacted entry raises an AttributeError instead of yielding a redacted value; and redaction must **not**
be implemented by adding an `Items()` method, which would re-open the `json.encode(state)` / `dict(state)`
hole the type comment at `:456-463` deliberately closes — build a separate `*Dict` and return *its*
`items`/`values`, since `Attr` today returns the underlying dict's own bound builtin (`:582-593`).
Dict **membership** still reveals which declared keys are present, but that is the pre-existing
enumeration signal `keys()`/`Iterate` have always carried and which the deferred-miss design already
adjudicated (§2.4 there) — the placeholder adds no channel and should carry no key.

### 4.3 What Increment 1 closes, and what it does not

**Closes, structurally:** the decrypt of a key the operation never names (§1.1a) — no Vault call, no
`piiKey` read, no plaintext; the §1.1c whole-set oracle; and the surplus Vault round trips a hostile
envelope could demand (up to 1000 per op).

**Does not close:** §1.1b — a key the script *does* name from an attacker-supplied payload field is still
decrypted, and the sole containment remains the step-6 external-egress guard. And §1.1d — a surplus
**non-sensitive** declaration still enters the working set (it is redacted from whole-set seams only
because unclassified documents are, not because it is protected). Both are Increment 2.

### 4.4 Non-script consumers of `Hydrated`

`ScriptContext.Hydrated` stops carrying plaintext for un-named keys. Consumers: `step6_resolve_ddl.go:258`
and `:374` (working-set instanceOf edges, target-key liveness — structure and `IsDeleted`, never `.Data`),
`commit_path.go:409` (revisions only), and `internal/spike/starlark/runner.go:30` (outside the commit
path). Step 8's tombstone body preservation is **not** affected: `readPriorDocuments` reads prior docs
from KV *"rather than trusting step-4 hydration"* (`step8_commit.go:500-503`), so a tombstone writes back
at-rest ciphertext regardless.

### 4.5 Tests

A declared-but-unread sensitive key yields **zero** Vault calls and no `piiKey` GET (fake-Vault counter);
`K in state` on a pending-sensitive key yields zero decrypts, and `state[K].data` then decrypts **exactly
once** across repeated access (the side-map memo, asserted under `for k in state:` to pin the
iteration-mutation blocker); each of `state[K].data`, `state.get(K).data` and `kv.Read(K)` yields
plaintext byte-identical to today; whole-set seams render the placeholder and the test asserts
non-vacuously that the placeholder **is** present *and* the secret is **not** in the rendered string;
an unclassifiable document is redacted (drive one through the fail-open resolver path); a **Vault-less**
deployment still marks-and-consumes on body access (the `v == nil` path) so the egress guard stays live;
an `external.*`-emitting op with a surplus sensitive declaration now **commits** where it is rejected
today (the §1.1c oracle closing) while the same op reading the body is still rejected. E2E: a
clinic/identity sensitive aspect read by its owning op is unchanged end to end.

---

## 5. Increment 2 — `readScope`: the DDL declares which keys its operations may read

**The declaration lives on the DDL**, beside `permittedCommands`, `script` and `sensitive` — a
`vtx.meta.<NanoID>.readScope` aspect declared by `DDLSpec.ReadScope map[string][]string`
(operationType → templates) and carried on the DDL cache's `MetaVertexRef`. The op-meta's
`OpDispatchSpec.Reads`/`OptionalReads` remain the **client-facing** projection of the same declaration,
with a package-build-time subset check (`Dispatch.Reads ∪ OptionalReads ⊆ readScope[op]`) that is
worth having on its own — today the client's templates and the script's actual reads can drift silently.

### 5.1 The admitted set

At the head of step 4, after derivation, the Processor computes the authorized set for
`env.OperationType` and refuses any key outside it — **and the same set is re-checked at the live
`kv.Read` seam** (§5.3):

```
authorized := resolveTemplates(ddl.ReadScope[env.OperationType], env) ∪ rawDerivedKeys
for k in declared.Reads ∪ declared.OptionalReads ∪ declared.EgressReads:
        if !admits(authorized, k) { fault ReadNotAuthorized(k) }
```

`rawDerivedKeys` is the derivation's **raw** output captured before `mergeDerivedReads`, not the merged
set. This is load-bearing: step 4 assigns the merged set straight over the envelope's
(`step4_hydrate.go:189-196`, `declared = derived`), so afterwards envelope-declared and derived keys are
indistinguishable; worse, `appendDerived` (`derive_reads.go:296-298`) **skips** a derived key the
envelope already declared, so computing `rawDerivedKeys` from the appended delta would refuse exactly the
read-before-create key class (g) exists for. `deriveReads` returns both sets.

`resolveTemplates` uses the envelope alone — no Core-KV read, nothing that would put a walk on the write
path. `env.Payload` is a `json.RawMessage` (`opwire.go:133`), so the resolver shares the single generic
parse the runner and the derivation already perform (`starlark_runner.go:645`, `derive_reads.go:149`)
rather than adding a third.

| Template | Server-side resolution | Strength |
|---|---|---|
| a literal (`vtx.config.<id>`) | none needed | **exact** |
| `{actor}` / `{actor:id}` | `env.Actor` (step-3 authenticated) | **exact** |
| `{payload.<field>}` (`:id`, `?`) | parsed payload | attacker-chosen value, **exact key shape** — the script's guard adjudicates |
| `{scopedTo}` | `env.AuthContext.Target` — **only exact when `env.AuthTargetValidated`** | otherwise **type-and-shape only** |
| `{service}` | `env.AuthContext.Service`, service path | **type-and-shape** unless proven |
| `{me.<type>}` | not resolvable server-side (client's `selfAnchors`) | **type-and-shape** |
| `{entity.<column>}` | not resolvable server-side (a projected `manifest.ent` row) | **type-and-shape** |

**`{scopedTo}` is not the control it looks like.** `authTargetValidated` is true only on the task path and
on a platform path with `Scope == "self"` (`operation_context.go:46-57`); `opwire.go:141-148` states
plainly that on scope=any it is *"an unchecked client-supplied hint"*. The corpus is overwhelmingly
scope=any (301 `"any"` vs 50 `"self"` across `packages/*/*.go`), so treating `{scopedTo}` as exact would
have handed most ops a wildcard while presenting as precise.

**`{entity.<column>}` is a sixth placeholder the platform's own doc comment omits** — it lives in the real
resolver (`cmd/facet/web/app.js:2223-2247`) and is in production use in `packages/wellness-domain`
(`opmetas.go:109`, `:247`, `:300-306`, `:346`), currently in `ContextParams`; nothing stops it appearing
in `Reads`, since `substituteTemplate` is applied uniformly (`app.js:2475`).

The three type-and-shape rows are the mechanism's honest floor: they constrain the working set to the
**type** the op reads and hand ownership back to the script's guard. Closing them further would require
resolving the actor's self-anchors or a projected row during hydration — a walk on the write path, which
is the no-scans invariant, and something `derive_reads` cannot do either (its `kv` module is a fail-closed
stub by design). Recorded as a residual in §9, not hidden.

**Fail-closed direction.** `ReadNotAuthorized` is a **step-4 runtime fault**, not `EnvelopeMalformed` —
the authorized set depends on the resolved DDL, which envelope validation cannot see (the same reasoning
§2.5 applies to the derived-key ceiling). It is decided **before any GET**, from the envelope and the DDL
alone, so it is not itself an existence oracle; the reply names the refused key, which the client chose.

### 5.2 The real migration input

The draft's "descriptors are the read set" premise is false in two directions, and both enlarge the work:

- **The vertical apps do not use descriptors at all.** LoftSpace, Clinic, Café and Wellness hand-build
  `reads`/`optionalReads` imperatively in JS from row data — `cmd/loftspace-app/web/app.js:1339-1342`
  (`reads: [state.applicant, row.unitKey]`), `:129`, `:1946`, `:3666-3676`, `:3858`;
  `cmd/clinic-app/web/app.js:696`, `:926`, `:945`; `cmd/cafe-app/web/app.js:446`, `:545`, `:942`.
  `CreateLeaseApplication` — the op behind the first — has a permission spec and **no op-meta at all**.
- **Even the descriptor-driven client sends keys no template produced.** `cmd/facet/web/app.js:2483`
  appends the resolved target key *"even when `dispatch.reads` doesn't name it explicitly"*, and `:2491`
  appends `me().identityKey` unconditionally (*"Over-reading is harmless"*). Every `readScope` must
  therefore admit `{actor}` and the dispatch target field, or every Facet submission fails — and the
  subset check would pass while the runtime refused.

**Corpus, measured (`grep` over `packages/*/*.go`, excluding tests):** **105 distinct permissioned
operationTypes**; **47** `OpDispatchSpec` blocks; **6** live `[no-op-meta:]` exemptions; 301 `"any"` /
50 `"self"` scope entries. The draft's "46 → 35 ops, mostly transcription" was wrong by ~3× and is
withdrawn. The migration input is the **union** of descriptor templates, each app's hand-built call sites,
and the client's unconditional appends — per op, read off the script.

### 5.3 The live seam, and class-(e)

`readScope` is checked again at `kv.Read`'s lazy fallthrough (`starlark_kv.go:115-136`) — the same
authorized set, the same fault. Without this, §1.1b survives every increment and §2's property is false.

Class-(e) follow-ups cannot be exact keys by construction. They are admitted by **provenance**: a key
reached from a `kv.Links` page over a hub+relation the op declared (`contextHint.enumerations`, today
inert beyond shape validation) is admitted for the keys that enumeration returned. This gives the
`enumerations` channel its first Processor consumer and keeps the mid-walk idiom the corpus documents as
permanent (`packages/clinic-domain/ddls.go:1878-1891`, `packages/cafe-ledger/scripts.go:390-402`) working
unchanged, while bounding it to the declared hub.

### 5.4 The one op class `readScope` cannot express — and the rule that fixes it

`CreateLeaseServiceInstance`'s honest read set is `{payload.subjectKey}` plus *an aspect segment named
inside the payload's own params* (§1.1b). No template in §5.1 has a payload-derived **aspect segment**,
and the AI-authoring vocabulary anchors the placeholder at the start with a literal suffix
(`capabilitymaterializer_starlark.go:73`). The two available declarations are both wrong: too narrow
breaks the op for every template a workflow author writes; `{payload.subjectKey}.*` readmits arbitrary
PII reads.

**Resolution — a wildcard aspect segment is legal and forces the ref disposition.** `readScope` admits a
trailing `.*` aspect wildcard, and **any `sensitive` aspect reached through a wildcard segment hydrates as
a `$sensitiveRef`, never plaintext** — the disposition `egressReads` already implements
(`sensitive_decrypt.go:173-203`). The op keeps working, the vendor still receives what it needs through
the bridge's ref unwrap, and the plaintext branch that today depends on an ownership guard nobody wrote
becomes structurally unreachable. The wildcard is lint-visible and countable, so a package cannot use it
to widen quietly.

This also corrects a claim the draft made: **the egress-ref path is not "already the correct shape"** to
copy uncritically. `handleDecryptRef` (`internal/vault/service.go:465-510`) validates the key shape and
recomputes the MAC over `{ref, requestID, ciphertext}` — it consults **no actor, no operation, no grant**.
The MAC is *provenance*, not authorization: it proves the Processor minted the ref for that request, and
nothing more. It is the right disposition for §5.4 precisely because it keeps plaintext away from the
script, not because it authorizes anything.

### 5.5 The migration and the gate ship together

Per the lint doctrine — a gate is the only thing that binds the next author — the mechanism, the
migration and the gate are one increment: extend `scripts/lint-package-standard.go` so an op with a
permission spec must declare `readScope[op]`, with the descriptor-subset check beside it. **The empty set
is legal and meaningful** ("this op declares no reads"), so the gate default-denies the *undeclared* case
and makes the author state which it is — the shipped `# read-posture:` pattern. Because the migration
leaves zero debt for the permissioned corpus, the gate ships **blocking**, not warn-first. A DDL with no
`readScope` is unchanged in Increment 2; that residual default-open is exactly why Increment 3 is not
optional.

### 5.6 The authoring surface a new DDL aspect touches

Adding `.readScope` is not one edit. All five are named so the Steward does not discover them mid-fire:

1. `internal/pkgmgr/build.go:100-142` — package install emits each DDL aspect explicitly.
2. `internal/bootstrap/meta_ddl.go:164-181` — `CreateMetaVertex`'s hard-coded aspect list.
3. `internal/bootstrap/meta_ddl.go:351` — `UpdateMetaVertex`'s `add_list_field` allowlist. **Without an
   entry here a package upgrade cannot change `readScope` — it silently no-ops** (verified: the allowlist
   is a fixed sequence of `add_*_field` calls). Also `:421-424` — `TombstoneMetaVertex`'s
   `aspect_suffixes`, which already omits `.sensitive` and would strand a live `.readScope` under a dead
   root.
4. `internal/bootstrap/verify.go:121-127` — the per-class required-aspect list `make verify-kernel`
   asserts.
5. `internal/pkgmgr/capabilitymaterializer_starlark.go:170-179` — `knownVertexTypeDDLFields` is a
   **closed** allowlist (verified: 8 entries, no extension point). An AI-authored `vertexTypeDDL`
   therefore cannot declare `readScope` at all, so under Increment 3's default-deny **every AI-authored
   DDL would refuse every read the moment it installs**. Resolved in §6.

Items 1–2 land in Increment 2 (nothing installs without them); 3–5 land with the default flip.

### 5.7 Tests

Template resolution per row of §5.1's table, including the `:id` fragment mid-key form
(`lnk.leaseapp.{payload.leaseAppKey:id}.applicationFor.identity.{actor:id}`), the `?` marker, and
`{scopedTo}` proving exact **only** under `AuthTargetValidated`; a surplus key refused with
`ReadNotAuthorized` naming it and **no GET issued**; a raw-derived key admitted even when the envelope
also declared it (the `appendDerived` trap); an empty `readScope` refusing every declared key; the live
`kv.Read` seam refusing an unauthorized key; a class-(e) follow-up admitted only for its declared hub; a
sensitive aspect reached through a wildcard segment yielding a ref, never plaintext. E2E: a real op
through the Gateway with a hand-added surplus `reads` entry is rejected; unmodified, it is unchanged.

---

## 6. Increment 3 — coverage, and the default flips to deny

The residual after Increment 2 is every op **without** a permission spec: Loom `instanceOp`s, Weaver
`directOp`s, pkgmgr's `submitDirectOp` (`internal/pkgmgr/installer.go:396`), bootstrap meta-DDL ops, the
bridge's reply ops, the `keyshredded` finalization op.

**The draft justified deferring these as "submitted by service actors, not ordinary actors." That premise
is struck.** Two paths falsify it. The `userFacing` classification the package gate uses
(`scripts/lint-package-standard.go:868-880`) treats `operator`, `consoleOperator`, `demoOperator`,
`control-operator` and `identityProvisioner` as trusted-tool roles — but that is an **authoring**
classification with no counterpart in step 3, which matches operationType+scope out of
`cap.roles.<actor>` and nothing else (`step3_auth_capability.go:503-586`); `operator` is a grantable human
role. And an **ephemeral task grant can name any op-meta**: `CreateTask` takes `forOperation` as any
`vtx.meta.<NanoID>` from the payload and validates existence only
(`packages/orchestration-base/ddls.go:265-277`), while `matchEphemeralGrant` checks only
`{taskKey, operationType, target, expiresAt}` (`step3_auth_capability.go:338-364`). The Gateway itself
constrains no operationType (`gateway.go:749-751`).

So Increment 3 is coverage of a reachable surface, not tidiness. It declares `readScope` for those ops and
flips the runtime default: **a script-bearing DDL with no `readScope` refuses every read.** Two shapes
resolved here rather than deferred:

- **Engine reads are templated against a lens row** (`Reads []string` with `row.<column>`,
  `internal/pkgmgr/definition.go:339`) which the Processor never sees. The resolution is *not* to plumb
  the row: the dispatched op's `readScope` is declared against **its own envelope**
  (`{payload.<field>}` / `{actor}`), and the engine's `row.<column>` template resolves into that payload
  before submission. Where a target's `Reads` names a key reaching the envelope through no payload field,
  that is a genuine gap in the target spec, surfaced by the migration.
- **AI-authored DDLs must be able to declare.** `readScope` is added to `knownVertexTypeDDLFields`
  (§5.6 item 5) in the same increment as the default flip. Carving AI-authored DDLs *out* of the flip was
  the alternative and is rejected: it would make the AI-authoring lane the one place the invariant does
  not hold, which is the opposite of where it is needed.
- **Bootstrap runs before packages install.** Kernel DDLs declare `readScope` in the same seed structures
  that already declare `permittedCommands`; `make verify-kernel` covers the result (§5.6 item 4).

---

## 7. Contract surface

### 7.1 Contract #2 §2.5 — staged UNCOMMITTED in `main`

§2.5 states the un-inspected property twice as settled fact — in the deferred-fault paragraph (*"step 3
authorizes … without inspecting the declared read set"*) and in the ceiling paragraph (*"which leaves
that count unpriced for any actor holding any operation grant"*). Both are load-bearing *rationales* for
shipped decisions, so neither is deleted: each gains a pointer, and the rule is stated as its own
paragraph — **Read authorization** — covering the authorized set, server-side template resolution and its
three type-and-shape degradations, the unconditional admission of raw-derived keys, the live-seam and
class-(e) provenance rules, the wildcard/ref disposition, and the `ReadNotAuthorized` runtime-fault
disposition. The staged diff is the proposal. **Increment 1 needs no §2 change.**

### 7.2 Contract #3 §3.10 — the exact edit, deliberately not staged

That file already carries the *subject-anchored sensitive aspects* design's uncommitted proposal, so a
second design's hunks there would ride in on whichever is ratified first. Apply in the commit that
ratifies **this** design:

- **Line 211**, today: *"Step 4 (hydrate) decrypts any sensitive aspect read into the Starlark context,
  so scripts operate on plaintext."* → **"Step 4 (hydrate) reads a sensitive aspect as stored; the
  decrypt happens where the operation reads the document's body, so scripts operate on plaintext exactly
  where they ask for it. A whole-set exposure (`state.items()/values()`, rendering `state`) yields a
  redacted placeholder for every document not proven non-sensitive — membership and key enumeration are
  unaffected."**
- **Line 264**, today: *"Hydration decrypts every present declared sensitive key, so keying the guard on
  the decrypt made a surplus declared read … split the outcome"* → **"Hydration no longer decrypts a key
  whose body the operation does not read, so a surplus declared read produces no plaintext to consume;
  the consumption trigger below governs the documents the script does read."** The rest of the paragraph
  (consumption, not decryption, as the trigger) stands and remains correct.

Contract #1 needs nothing: `readScope` is a DDL aspect in the same family as `permittedCommands`
(§1.x illustrates that aspect set rather than closing it).

---

## 8. Reconciliation — didn't we already handle this?

**"The existence oracle was closed — isn't this the same thing?"** No. `3a78c109` closed the *reply
channel*: a surplus declaration no longer changes the outcome. It deliberately left hydration untouched,
and said so. This addresses what hydration **does** — the Vault decrypt, the widened working set.

**"Doesn't the consumption tracker already contain sensitive plaintext?"** Only at the *egress*
boundary, and only by rejecting the operation. It does not prevent the plaintext being produced, does not
stop it reaching a mutation or an ordinary event, and its own flip is the §1.1c oracle. Increment 1
removes the production for un-read keys; the tracker keeps its job for the documents the script reads.

**"Is this D1?"** No — §2. The `sensitive-read-tracker` §2.2 row is filed `🚧 seq behind read-path auth
(D1)` on that inherited premise; ratifying this **clears that sequencing**, and the row is closed by
Increment 1 rather than built separately — it should retire to the Done log when Increment 1 ships.

**"Does this introduce new state?"** One declaration (`readScope`, a DDL aspect beside
`permittedCommands`) and two per-execution structures (`sensitivePending`, and the decrypt memo) scoped
exactly like the existing `RequiredAbsent` / `plaintextKeys` sets — built at hydration, consumed within
one execution, never carried across an OCC retry because step 4 re-runs inside the loop.

**"Does it duplicate `derive_reads`?"** It completes it. Class (g) computes keys the submitter cannot
express; `readScope` bounds the keys the submitter *can*. Together the read set becomes a function of the
operation from both directions.

---

## 9. Risks, alternatives, residuals

**Alternatives considered.**

- **Actor-scoped read authorization (the row's own framing).** Rejected in §2: it needs a per-key
  authorization plane Core KV does not have and P5 says it should not grow, and it answers a question no
  consumer asks — the guards want "may this operation touch this resource", which they already answer.
- **Envelope-reachability floor** (admit a key iff its bare id appears anywhere in the envelope). Zero
  declaration, zero migration, universal coverage. Rejected: it is shape without semantics — an op taking
  an `appointmentId` would admit `vtx.identity.<that-same-id>.demographics`, so the key the script reads
  and the key the attacker declared need not be the same, and the guard that adjudicates the former never
  sees the latter. It also still needs a declaration for literal/config reads.
- **Strip surplus declarations silently.** Rejected: a client declaring a key the op cannot read is an
  attack or a bug, and both deserve to be loud; silent narrowing also hides client/DDL drift.
- **`derive_reads` as the sole authority.** The most complete shape and compatible with this one, but the
  pre-pass runs the module's **whole top level** per operation (`step4_hydrate.go:38-43` is explicit its
  budget is sized against the same `Init` step 5 pays), so universal adoption roughly doubles per-op
  sandbox init for ops whose read set is a two-line template. Rejected as the *primary* mechanism on cost,
  not correctness — `readScope` admits raw-derived keys unconditionally, so a package wanting the stronger
  form already has it.
- **Do only Increment 1.** It carries the cheapest severity reduction and is what I recommend building
  first — but as the whole answer it is rejected, because after review it demonstrably leaves §1.1b (a
  payload-named decrypt of an arbitrary identity's PII, with a live shipped instance) contained only by
  the step-6 egress guard.
- **Keep `consumeAll`'s flip as a classifier-independent backstop** (the security reviewer's suggested
  fix for the fail-open classification). Rejected in favour of fail-closed *classification* (§4.2):
  retaining the flip would preserve exactly the §1.1c oracle the increment exists to close. Both defects
  are real; only one fix resolves both.

**Risks.**

- *Latency and failure timing shift into the script.* A decrypt now happens inside the execution budget
  where it used to happen in step 4. §4.1's context/charging seam and §4.5's "decrypts exactly once" test
  are the mitigations; the `in`-probe idiom is explicitly kept free of decrypts.
- *Increment 1 changes what whole-set seams return.* Zero shipped scripts use one (§1.1d), so the
  regression risk is in what someone writes next — which is the intended change.
- *Increment 2's migration is large and partly source-less* (§5.2). The subset check catches
  descriptor/DDL drift but not app-hand-built drift; the e2e vector (a real op through the Gateway) is
  what proves each migrated op.
- *Three template classes remain type-and-shape only* (§5.1) — named, not hidden.

**Residuals to file (not silently absorbed):**

1. `{me.<type>}` / `{entity.<column>}` / unproven `{scopedTo}` reads are admitted by type, not ownership.
   ★, S, behind an observed need.
2. `{entity.<column>}` is undocumented in `OpDispatchSpec`'s own doc comment while in production use
   (`packages/wellness-domain`). ★, XS — a doc fix in `internal/pkgmgr/definition.go:561-562`.
3. `contextHint.enumerations` is client-supplied and inert today; §5.3 gives it its first consumer, but
   until Increment 2 it declares nothing anyone checks. Fold into this design's row, don't duplicate.
4. Non-package submitters (`internal/gateway`, `internal/objectmanager`) build envelopes directly — the
   same boundary as the filed *G2 derived-key gate does not cover `internal/` submitters* row. Fold.

---

## 10. Decomposition for the Steward

| # | Scope | Green when |
|---|---|---|
| **1** | Hydrate-as-stored + `sensitivePending` carrying the resolved ref; lazy `.data` decrypt with a side-map memo (never `SetKey`, never `Get`, never a re-GET); fail-closed whole-set redaction and removal of `consumeAll`'s flip; the `starlarksandbox` execution-context seam + live-read charge; §7.2's §3.10 wording | `go test ./internal/processor/... ./internal/starlarksandbox/...` incl. §4.5's vectors; full suite; e2e sensitive read unchanged |
| **2** | `DDLSpec.ReadScope` → `.readScope` aspect → DDL cache (+ `build.go`, `CreateMetaVertex`); step-4 **and** live-`kv.Read` authorization; template resolver; raw-derived capture (`deriveReads` signature); class-(e) provenance; the wildcard/ref rule (§5.4); migration of the 105 permissioned ops; blocking `lint-package-standard` gate + subset check | full suite; `make verify-kernel`; `make verify-package-*`; every `scripts/lint-*.go` gate |
| **3** | Engine/platform ops declare; `UpdateMetaVertex` allowlist + tombstone suffixes + `verify.go` + `knownVertexTypeDDLFields`; runtime default flips to deny | full suite; `make verify-kernel`; live-stack smoke of Loom/Weaver/pkgmgr dispatch |

Increment 1 is the one to build first and is independently valuable. 2 and 3 must not be merged: 2's
migration has to land against its own gate, not in front of 3's default flip.

---

## 11. Adversarial review — what it changed

Two independent passes on the frozen draft. **4 blockers, 9 majors.** The draft would have shipped a false
security claim and three unimplementable instructions; what changed:

- **The Increment-1 claim was false.** The draft's "For Andrew" said Increment 1 removes the
  arbitrary-decrypt capability. The lazy `kv.Read` seam it marked "unchanged — already decrypt-on-name"
  decrypts any key the *script* computes, and `CreateLeaseServiceInstance` computes one from the payload
  with no ownership guard, feeding an external event. Claim narrowed (§4.3); the op named (§1.1b); the
  class resolved structurally (§5.4).
- **The stated property was unachievable as scoped.** Bounding only the declared set leaves live reads and
  class-(e) enumeration beside it. `readScope` now governs the live seam too, and §2.1 states the boundary
  instead of implying there isn't one.
- **Three proposed reuses were mechanisms I had not opened** — the exact blind spot §2 of the Designer
  skill warns about, in three subsystems at once: memoizing into the state dict is rejected by
  go.starlark.net during iteration; `ScriptContext.KVReader` is a one-method interface whose use would
  re-read and break the OCC snapshot; and `Get` has no thread, so the decrypt would escape the wall
  budget. All three corrected in §4.1.
- **The redaction was fail-open**, inheriting `resolveGoverningDDL`'s five documented fail-open paths;
  and "hydrate as stored" wrongly assumed ciphertext, which would have made the egress guard vacuous on
  Vault-less deployments. §4.1–§4.2.
- **The census was wrong by ~3×** and the "service actors only" premise for deferring engine ops was
  falsified by a grantable `operator` role and by ephemeral task grants naming any op-meta. §5.2, §6.
- **Four unnamed authoring-surface edits**, one of which (`knownVertexTypeDDLFields`) would have made
  Increment 3 brick every AI-authored DDL. §5.6, §6.

The pre-build gate this design set itself is therefore **discharged**: the passes ran, and their findings
are in the body rather than appended.
</content>
