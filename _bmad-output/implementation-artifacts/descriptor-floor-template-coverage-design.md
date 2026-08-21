# Descriptor-floor template coverage — closing §2.5's client-only-vocabulary skip

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — build-ready for the Lattice Steward.**
**No architectural fork. No frozen-contract change** (§7 proves both) — this design makes the committed
Contract #2 §2.5 floor clause *true* for the template vocabulary it already governs; the clause text is
untouched.

Backlog row: *[Processor] §2.5's floor silently skips the key templates it cannot resolve* (★★, filed off
`auth-plane-projection-latency-design.md` §19.7's "Recorded, not fixed" sibling scope).

---

## 1. Problem + intent

Contract #2 §2.5 (02-operation-envelope.md:160) is unconditional: *"Where an operation's descriptor declares a
key under `optionalReads`, a submitter-supplied `contextHint` naming that same key under `reads` MUST NOT
harden it."* The clause **binds the submitter** and is a security boundary — it is what keeps an
anti-enumeration op's generic rejection from being re-opened as an existence oracle via `HydrationMiss`'s
`details.missingKey`.

The enforcement (`internal/processor/descriptor_floor.go`) resolves only `{actor}`, `{service}`,
`{payload.<field>}` (±`:id`). The descriptor template vocabulary
(`internal/pkgmgr/definition.go`, OpDispatchSpec doc; client authority `cmd/facet/web/app.js`
`substituteTemplate`) is larger: `{scopedTo}`, `{me.<type>}`, `{entity.<column>}`, plus the `:id` and `?`
suffixes. A template carrying client-only vocabulary yields **no floor** — a Warn log and nothing else
(`descriptor_floor.go:164`) — so every key such a template declares optional stays hardenable by a hand-rolled
`contextHint`. The clause reads unconditional; enforcement is not. That is the
committed-clause-ahead-of-enforcement direction: reads as closed, is fail-open.

**Live corpus** (census, §6): exactly three such templates, all cafe-domain — `Charge` ×1, `Settle` ×2, all
`{me.leaseapp:id}` fragments inside 6-segment link keys. The concrete exposure they carry today is modest
(§5.3) — the driver is the **class**: the next descriptor written with `{me.<type>}` on a genuinely sensitive
path (a claim-ceremony-like op) inherits the hole invisibly, and nothing errors. identity-domain's
claim-path descriptors — the ops the floor was built for — use only resolvable vocabulary today, which is why
the gap is latent rather than burning.

**Intent:** the floor covers the entire read-template vocabulary a descriptor may legally carry, the
vocabulary itself is pinned at authoring time, and the only residual skip is version skew (an older Processor
meeting a newer vocabulary), Warn-logged.

## 2. Grounded mechanism summary (what exists)

- **Floor site:** `step4_hydrate.go:204` — applied to the envelope's declaration at the head of step 4,
  before `derive_reads`, so `mergeDerivedReads`' "envelope's disposition stands" rule sees the demoted set.
- **Resolution:** `substituteDescriptorTemplate` (descriptor_floor.go:187) expands
  `{actor}`/`{service}`/`{payload.*}` ±`:id`; anything else → `ok=false` → template contributes no floor →
  Warn per op.
- **Demotion:** exact-key match against `contextHint.reads` → move to `optionalReads`; against
  `egressReads` → mark `EgressAbsenceTolerant` (never move — moving would swap a bridge-opened
  `$sensitiveRef` for plaintext).
- **Descriptor index:** `DDLCache.byOpType map[string][]string` stores **only** the `optionalReads`
  templates (`loadOpMetaDispatch` reads `data.optionalReads` off the `.dispatch` aspect); duplicates union
  order-independently and `Invalidate` rebuilds from contributors (`floorsByOpType` over `byOpMetaRoot` —
  the §22.2-fixed shape).
- **Wire:** the installed `.dispatch` aspect already carries `reads` too (`pkgmgr/build.go:699`) — the
  required-template list is on the wire today; only the Processor-side loader ignores it. **No package
  re-emission, no version bumps.**
- **Why declaring an arbitrary key is not itself an oracle** (step4_hydrate.go, hydrate-loop comment): a
  missing `reads` key is *recorded* required-absent, and `HydrationMiss` is raised only where the **script
  touches** the key. The oracle therefore exists only for keys the script actually reads — keys derived from
  the submitter's own payload/actor/target. This is the load-bearing fact behind the soundness argument
  in §5.

## 3. The shape

Three pieces, no new components, no new state outside the existing descriptor index.

### 3.1 Pattern compilation (Processor, `descriptor_floor.go`)

A template compiles per Hydrate call (same lifetime as today's resolution — derived from the envelope + the
cache's descriptor snapshot, dropped with `declaredReads`, re-derived on every OCC retry) into one of:

| Template element | Compiles to |
|---|---|
| literal text | itself |
| `{actor}`, `{service}`, `{payload.<f>}` (±`:id`) | concrete substitution, exactly as today; unresolvable (missing/non-string/empty) ⇒ the template yields no floor + Warn, as today |
| `{scopedTo}` (±`:id`) | concrete substitution from `env.AuthContext.Target` **only when `env.AuthTargetValidated`** (step 3 proved the target on the self/task paths); otherwise the template yields no floor + Warn. Never a pattern — see §5.4 |
| `{me.<type>:id}` | a **one-segment wildcard** constrained to `substrate.IsValidNanoID` |
| `{me.<type>}` (whole-key form) | the literal `vtx.<type>.` + a NanoID-constrained segment (3 segments, 2 fixed) |
| any other placeholder | the template yields no floor + Warn — the skew/bypass residual, §5.5 |
| a segment mixing literal text with a client-only placeholder (the mid-segment fragment idiom, e.g. a hypothetical `bkr{me.instructor:id}`) | the template yields no floor + Warn — never an over-wide wildcard. The gate (§3.3) refuses the shape at authoring; see §5.6 |

A template with at least one wildcard is a **pattern**; one with none resolves to a concrete key and takes
today's exact-map path unchanged. `{entity.<column>}` and the `?` marker compile to nothing because §3.3
removes them from the read-template vocabulary (they are ContextParams vocabulary; zero read-template
instances exist — census §6).

**Matching:** a concrete envelope key matches a pattern iff it has the same segment count and every
non-wildcard segment is byte-equal; a wildcard segment matches iff `IsValidNanoID(segment)`. A wildcard is
always a **whole** segment — the compiler never emits a fragment wildcard (see the table's mixed-segment
row), so matching needs no prefix/suffix arithmetic. (Link keys are 6 segments, vertex keys 3, aspect keys
4 — segment count is already the shape discriminator, Contract #1.)
Cost: O(declared keys × patterns) segment compares; declared keys ≤ 1000 (`MaxDeclaredReads`), patterns ≤ the
descriptor's own template count. Negligible; no budget machinery.

**Demotion via pattern is identical to demotion via exact key:** every matching `reads` entry moves to
`optionalReads` (all matches — a submitter declaring fifty same-shape guesses gets all fifty demoted, which
is precisely the intended effect); every matching `egressReads` entry is marked `EgressAbsenceTolerant` in
place. Repeated placeholders within or across templates do not correlate — each pattern demotes
independently (§5.2 bounds the cost of that).

### 3.2 Required-wins precedence (Processor, `ddl_cache.go` + `descriptor_floor.go`)

`opMetaDescriptor` and the per-opType index additionally carry the descriptor's **`reads`** templates
(loader: `loadOpMetaDispatch` reads `data.reads` beside `data.optionalReads`; the aspect already carries it).
At floor time both lists compile, and **a key that matches any required template is excluded from
demotion.** Fail-closed precedence: where the descriptor contradicts itself (a key both required-shaped and
optional-shaped), the required reading stands, because wrong-demotion is the dangerous direction (a silent
`None` where the script expected `HydrationMiss` — the exact failure §2.5's authoring rule exists to
prevent, per descriptor_floor.go's own "Direction of failure" header).

Required templates only ever resolve **concretely**: §3.3 restricts client-only vocabulary to
`OptionalReads` (census: zero required-side instances exist), so no required *pattern* can arise. That
restriction is load-bearing, not stylistic — a required `{me.<type>}` would compile to a loose pattern
whose exclusion blankets every declared root of that type out of demotion, quietly preserving the oracle
for the whole class (adversarial-pass finding 2, §10). A required key the server cannot resolve is also a
key the floor machinery can never reason about; an op that genuinely needs one keeps it out of the
descriptor and lets the script's own declared-read discipline govern it.

Index-shape rules ride the existing §22.2 fix unchanged: `floorsByOpType` unions **both** lists per
contributor order-independently, and `Invalidate` rebuilds both from `byOpMetaRoot` — the union is never the
thing a withdrawal subtracts from.

### 3.3 The authoring gate (pkgmgr, install-time — required increment, not a follow-on)

A new `Definition.validateOpDispatchTemplates()`, wired into `validateAll()`, refuses at install any
`Dispatch.Reads`/`Dispatch.OptionalReads` entry whose placeholders are not in the **documented read-template
vocabulary**: `{actor}`, `{scopedTo}`, `{service}`, `{payload.<field>}`, `{me.<type>}`, each ±`:id`. Three
sharper rules, each backed by a zero-instance census (§6):

- `{entity.<column>}` and the `?` marker are refused (ContextParams-only vocabulary; in a read template the
  client's `wholeKey` silently drops a `?`-marked entry today, so it is already dead weight);
- **client-only placeholders (`{me.<type>}`) are `OptionalReads`-only** — a required-side instance would
  force a required *pattern*, which §3.2 shows blankets a whole key class out of demotion;
- **a client-only placeholder must occupy a whole segment** — the mid-segment fragment idiom
  (`bkr{actor:id}`, wellness-domain, a live *resolvable* shape that stays legal) is refused when the
  embedded placeholder is client-only, because a fragment wildcard is inexpressible in §3.1's matcher and
  the compiler's fallback for it is no-floor.

Unknown future placeholders are refused by default — the vocabulary is a closed set an author extends
deliberately, with its floor semantics designed at extension time (the `# read-posture:` default-deny
shape: declaring is cheap, forgetting fails closed).

This is what turns §3.1's "unknown placeholder ⇒ no floor" from a standing fail-open into a rare-path
residual: the gate makes the case unreachable from the authored corpus; the runtime Warn covers what
remains (§5.5). Install-time (`validateAll` preflight — binds every pkgmgr-mediated path including the
AI-authored runtime channel) rather than a `scripts/lint-*.go` (which binds only CI). The AI-authored
validator's stricter anchored-only vocabulary (`capabilitymaterializer_starlark.go` `sensitiveReadAspect`)
remains a strict subset — no interaction; its posture comment stays true.

**Stated plainly: the gate is corpus hygiene, not the security boundary.** A package-plane actor submitting
`InstallPackage`/`UpgradePackage` directly reaches the kernel install script without pkgmgr's Go preflight
(adversarial-pass finding 3, §10), so the gate does not bind that channel. That is acceptable because the
floor's threat model is a hostile **submitter**, not a hostile **package author** — an actor who can author
descriptors authors the op's script itself and needs no floor trick; that actor class is the
package-authority-minting design's scope (📐 awaiting-Andrew), not this one's. Two facts keep the bypass
from degrading the floor below its pre-design state: an unknown template contributes **no floor and nothing
else** (the runtime posture is the backstop on every channel), and the duplicate-descriptor union is
monotone-protective — a second descriptor claiming an opType can only *add* floor templates, never withdraw
a legitimate contributor's (`floorsByOpType` rebuilds from all contributors, the §22.2 shape).

## 4. What each piece buys (payoff, traced per consumer)

- **cafe `Charge`/`Settle`** (the live shapes): `lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}`
  compiles to `lnk.leaseapp.<nanoid*>.applicationFor.identity.<actorID>` — every segment concrete except the
  lease id, anchored to the caller's own resolved actor. Any hand-rolled hardening of any lease-ownership
  probe for this actor is demoted. `Settle`'s second template patterns the tab-side link the same way.
- **The clause**: becomes true over the whole legal vocabulary — the guarantee the contract text has been
  asserting since `20a45bb4`.
- **The next author**: cannot introduce a floor-skipping read template at all (gate), and cannot silently
  extend the vocabulary past enforcement (default-deny).

## 5. Soundness (the part that must survive adversarial reading)

### 5.1 Why patterns are sound where client-faithful resolution would not be

The floor's job is to demote the key **the script will touch**, because that is the only key whose
`HydrationMiss` is observable (§2 last bullet). For `{payload.*}` templates, concrete resolution is sound
*because it cannot be decoupled*: the floor key and the script's touched key resolve from the same payload
field. For `{me.<type>}` there is no server-side value to resolve — and a hypothetical "resolve it like the
client would" is **unsound** anyway: `Settle`'s script recovers the lease from the *tab's own* `.status`
(never caller-supplied), so an attacker submitting a victim's `tabKey` makes the script touch the *victim's*
lease link — a key the attacker's own `me.leaseapp` would never name. The pattern
(`lnk.leaseapp.<*>.applicationFor.identity.<actorID>`) covers both the honest key and the decoupled one.
Shape-match is not an approximation of resolution; for this vocabulary it is the *correct* semantics.

### 5.2 Over-demotion (the dangerous direction) is bounded twice

Demoting a key the op genuinely requires turns `HydrationMiss` into silent `None`. Bounds:

1. **Required-wins** (§3.2): any key matching the descriptor's own `reads` templates is never demoted. The
   descriptor is the op's submission recipe — its required reads are exactly the ones a data-driven client
   declares hardened, so the recipe's own required set is protected structurally.
2. **Pattern tightness:** every server-resolvable placeholder stays concrete inside a pattern, so the live
   patterns fix 5 of 6 link-key segments. The one loose class is the whole-key `{me.<type>}` form
   (`vtx.<type>.<nanoid*>` — demotes any declared root of that type): zero live instances, and a
   submitter-added extra hardened read of the same type *would* be softened. That is the documented cost of
   declaring "the caller's own `<type>` vertex is optional" — the descriptor author's declaration governs the
   shape, and a script whose correctness requires a same-shaped key must put it in the descriptor's `reads`
   (which then wins). Recorded as a residual, not designed around: no live consumer can express the failure
   today (census §6), and the structural guard for the expressible case ships with it.

### 5.3 The exposure this closes, stated honestly

For cafe today: the un-floored channel confirms/denies `lnk.leaseapp.<X>.applicationFor.identity.<Y>` for
ids the attacker already holds — a relationship-confirmation oracle over an unguessable NanoID space, further
bounded by the script only touching keys derived from its own payload. Low. The design's value is
contract-integrity + the class (§1), not a burning exploit — which is why the build is right-sized at two
increments (§9) and no emergency posture is claimed.

### 5.4 Why `{scopedTo}` never patterns

`authContext.target` is client-supplied except where step 3 proved it (`AuthTargetValidated`: platform
scope=self and task paths). Resolving concretely from an **unvalidated** target would let a lying submitter
steer the floor away from their probe key; patterning it (`vtx.<*>.<nanoid*>` — type unknown) would demote
*every* declared vertex root, an over-demotion §5.2's bounds don't cover (a legitimate extra read the
descriptor never mentions). So: concrete-when-validated, no-floor-plus-Warn otherwise. Zero live templates;
the rule exists so the first user inherits sound semantics.

### 5.5 The residual, named

An op-meta whose templates this Processor cannot compile — via version skew (a newer vocabulary) or via the
direct-op install channel the gate does not bind (§3.3) — contributes no floor for those templates,
Warn-logged per op (the existing "the control did not apply must be as visible as the control fired"
posture). Fail-open by deliberate choice: refusing the op is an availability failure on every deploy-order
race, and demote-everything is the dangerous direction. In both cases the outcome is exactly the
pre-design status quo for those keys, never worse, and any vocabulary extension must ship its floor
semantics in the same change (default-deny at the gate makes forgetting impossible).

### 5.6 The mid-segment fragment idiom

The documented `:id` fragment may appear mid-segment to build deterministic composite localNames —
`vtx.session.{payload.session:id}.bkr{actor:id}` is live wellness corpus. With **resolvable** placeholders
the fragment substitutes concretely and nothing changes. A **client-only** fragment
(`bkr{me.instructor:id}`) is inexpressible in the whole-segment matcher; rather than invent
prefix/suffix-wildcard matching for a shape with zero live instances, the compiler treats it as
unresolvable (no floor + Warn — never an over-wide wildcard) and the gate refuses authoring it. The
fragment-match extension is straightforward if real demand arrives, and it ships with that demand's own
design.

## 6. Executable censuses (re-run at build Phase-0)

The adversarial pass (finding 4, §10) showed hand-tuned greps mis-reproduce (ContextParams and prose
collide with read-template hits), so the censuses ship as a **Go test**, not shell: the Inc-2 corpus test
walks every package `Definition`'s `Dispatch.Reads`/`Dispatch.OptionalReads` **structurally** (the same
walk the validator does) and asserts:

- client-only placeholders in `OptionalReads`: exactly **3** entries, all cafe-domain `{me.leaseapp:id}`
  (Charge ×1, Settle ×2) — as of `654d5924`;
- client-only placeholders in `Reads`: **0**;
- `{entity.*}`, `{scopedTo}`, or `?`-marked entries in either read list: **0**;
- mid-segment client-only fragments: **0**.

Running the full corpus through `validateOpDispatchTemplates` green *is* the "gate breaks nothing" proof,
and the count assertions keep the census re-runnable mechanically rather than as prose.

## 7. Contract surface + fork check

- **No contract change.** §2.5's floor clause is already unconditional; this design makes enforcement match
  it. The template vocabulary lives in `definition.go` (code doc) + `app.js` (client authority) — neither is
  a frozen contract. The `.dispatch` aspect shape is unchanged (its `reads` field already exists on the wire).
- **No architectural fork.** No new component, no engine touch, no read-path/auth-plane structure change; the
  enforcement point (step 4, Processor) is where the shipped mechanism already lives, chosen by what the rule
  protects (a submitter-facing security invariant ⇒ commit-path guard — the enforcement-point doctrine).
- Therefore **Winston-adjudicated** under the 2026-08-20 delegation; the adversarial gate is discharged
  in §10.

## 8. Reconciliation with the existing mental model

- *Didn't we already handle this?* The floor itself shipped (`abd76359`, §19.7) and covered the resolvable
  vocabulary; §19.7 explicitly recorded two coverage gaps as filed rows. This design closes the
  template-vocabulary one. The **derived-reads** one (*"a derived `reads` can harden a floored key the
  envelope never declared"*) stays its own row deliberately: it is **not** solved by patterns — a
  `derive_reads` script returning a required key that the descriptor calls optional is a package-internal
  contradiction, and silently demoting a *derived* requirement is the dangerous direction for a read the
  DDL's own author demanded fail-closed. Its right fix is closer to an authoring-time contradiction refusal
  and possibly a clause-scope question; folding it here would smuggle a contract-scope decision into a
  no-contract design.
- *Does this duplicate an established pattern?* It extends the shipped floor in place; the gate mirrors the
  `# read-posture:` default-deny convention and lands beside the existing `Definition.validate*` family.
- *New state?* Only the descriptor index widening to carry the `reads` templates it already receives on the
  wire — same lifetime, same §22.2 rebuild rules, no new latch/registry/watch.

## 9. Decomposition for the Steward (2 increments, S–M total; one fire)

1. **Inc 1 — Processor: pattern floor + required-wins.** `descriptor_floor.go` compilation/matching per
   §3.1–3.2; `ddl_cache.go` loader + index carry `reads` templates; `{scopedTo}` concrete-when-validated.
   Owns: compilation table tests; match/demote semantics tests; required-wins exclusion test; egress
   pattern-marking test; the **decoupled-probe attack vector** (fixture descriptor using cafe's real
   template strings, envelope hardens a same-shape key ≠ the client-faithful resolution, assert demotion —
   the positive vector that keeps the negative claim honest); duplicate-descriptor union + `Invalidate`
   rebuild tests over both lists (§22.2 regression shape). **Posture-changing — full review depth.**
2. **Inc 2 — pkgmgr: `validateOpDispatchTemplates`.** Vocabulary pin per §3.3 (incl. the
   OptionalReads-only and whole-segment rules for client-only placeholders). Owns: refusal tests per
   excluded shape (`{entity.*}`, `?`, unknown placeholder, required-side `{me.*}`, mid-segment client-only
   fragment), acceptance tests per legal shape incl. mid-segment *resolvable* `:id` fragments, and the
   structural corpus census test (§6). Mechanical — standard depth.

Every prescribed test is owned above; no unowned tail. Gates: the standard suite + `verify-kernel`;
no live-stack step is required (the mechanism is unit-provable; the shipped claim-ceremony harness
`make test-claim-ceremony` remains the descriptor floor's live regression and is unaffected).

## 10. Adversarial pass — one cold external reviewer (read-only, grounded in the mechanism files) + a self re-walk of the §2 reflex list

All findings folded into the body above; the material ones:

- **(external, blocker) The whole-segment wildcard model missed the live mid-segment `:id` fragment idiom**
  (`bkr{actor:id}`, wellness) — a client-only fragment would have either silently no-floored or
  over-wildcarded. Resolved as §5.6 + the gate's whole-segment rule: fail toward no-floor, refuse at
  authoring, extend the matcher only when real demand arrives.
- **(external, material) A required-side `{me.<type>}` would compile to a loose required pattern whose
  exclusion re-opens the oracle for a whole class** — resolved by making client-only vocabulary
  OptionalReads-only at the gate (§3.2/§3.3; zero live required-side instances).
- **(external, material) "Binds every install path" was false** — the direct
  `InstallPackage`/`UpgradePackage` channel skips pkgmgr preflight. Reframed honestly in §3.3: the gate is
  corpus hygiene; the runtime no-floor posture is the backstop; the hostile-author channel belongs to the
  package-authority-minting design.
- **(external, material) The shell censuses did not reproduce their counts** — replaced with a structural
  Go corpus test (§6).
- **(external, checked-OK)** `env.Actor` is step-3-authenticated so `{actor:id}` anchoring is not
  submitter-steerable; `AuthTargetValidated` is set before step 4 runs; the `.dispatch` aspect carries
  `reads` on the wire; `byOpType`/`DispatchOptionalReads` have no other consumers that the index widening
  breaks.

- **"Resolve `{me.*}` like the client" was the intuitive first shape and is unsound** — caught by tracing the
  script's touched key on the decoupled-tabKey path; became §5.1 and the Inc-1 attack-vector test.
- **`{scopedTo}` concrete-always was unsound** (attacker-steerable on unvalidated paths) — became §5.4's
  validated-only rule.
- **Pattern fallback for `{scopedTo}`/`{entity.*}` was over-broad** (typeless `vtx.<*>.<*>` demotes unrelated
  declared roots) — resolved by vocabulary removal (`{entity.*}`) and validated-only (`{scopedTo}`) instead
  of a clever matcher.
- **Required-wins needs the `reads` templates the cache never stored** — the assumed-transport check found
  the aspect already carries them (`build.go:699`), collapsing a feared package-re-emission increment to a
  loader change.
- **Checked against in-flight designs:** sensitive-aspect-class-integrity (step 6/6.5),
  package-authority-minting (bootstrap), retention-class-key-custody §30 (pkgmgr manifest verifier), NL-1
  (weaver/bridge) — no seam overlap; §30's Inc 3 also adds a `Definition` validator but on a disjoint
  concern (reserved column names), trivially co-mergeable.

## For Andrew (transparency, no decision required)

Winston-adjudicated under your 2026-08-20 delegation: no fork, no contract edit — §7 is the proof, §5.3 the
honest exposure statement. The one judgment call worth your eyes if you ever revisit: §3.3 **closes** the
read-template vocabulary (default-deny unknown placeholders at install). That trades authoring flexibility
for the guarantee that enforcement can never silently lag vocabulary again — the same trade the
`# read-posture:` annotation made, and reversible by ordinary design work if a future vocabulary need
arrives.
