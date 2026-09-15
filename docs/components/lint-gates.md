# Lint gates (`scripts/lint-*.go`)

The repo's static gates. Each is a standalone `//go:build ignore` Go program run by CI's `lint-static` job
as `STRICT=1 go run ./scripts/lint-<name>.go` (`.github/workflows/ci.yml` is the authority for the list),
and by a `make lint-<name>` target for local use. Unset `STRICT` warns; `STRICT=1` exits non-zero on any
non-advisory finding.

| Gate | What it holds |
|---|---|
| `lint-conventions` | CLAUDE.md code conventions: key shapes, no history comments, and the author-declares families (`# read-posture:`, `# authcontext-target:`, `# workplace-exempt:`, `# op-name:`) |
| `lint-web` | G2: no client-side hash-to-NanoID derivation in `.js`/`.mjs` |
| `lint-lens-anchors` | A Personal lens declares its actor→anchor `Walk` |
| `lint-package-standard` | Vertical Package Standard (S1 descriptors, S6 floor, S7 manifest) |
| `lint-package-version` | A `packages/` content edit bumps its manifest version |
| `lint-facet-discovery` | No vertical vocabulary in `cmd/facet` |
| `lint-facet-renderer-drift` | Descriptor vocabulary parity across renderers |
| `lint-app-op-descriptors` | A vertical app wires UI only to described ops; ratchets its hardcoded-op-literal count |
| `lint-manifest-entity-type` | An edge-manifest lens tail's `entityType` matches its `entityKey` binding |
| `lint-doc-orphan` | A doc comment names the declaration it sits above |
| `lint-capability-kv-readers` | One reader owns Contract #6 §6.1 |
| `lint-gap-column-declaration` | Every `missing_*` column that lands in a weaver target's rows is declared in that target's `gaps` map — derived through `internal/lenscolumns`, the one reading of "which keys does a row of this lens carry", which `internal/pkgmgr` holds the same invariant on at the two non-CI paths (the installer's live preflight refuses the install; the capability-artifact validator records the proposal invalid). Second rule over the same rows: every `maxretries_<g>` retry cap has a `missing_<g>` gap beside it — the engine reads a cap only under the name it derives from the gap key, so a hand-spelled cap under any other name is dead |
| `lint-board` | The backlog is an index, not a journal |
| `lint-slog-values` | An slog attribute value's in-module struct type implements `slog.LogValuer`/`json.Marshaler`/`encoding.TextMarshaler` — a JSON handler never consults `fmt.Stringer` |
| `lint-flag-consumer-census` | A registered process-wide flag's readers are a declared ledger (file + function), so a new reader re-reads the bound the flag's own comment prices |
| `lint-loupe-console-grants` | Every op `cmd/loupe` submits through the Gateway under the operator's token is granted to `consoleOperator` by `packages/console-operator` at the lane it submits on — the console never holds `operator`, so a grant missing from that one package is a silent denial every package-local pin misses; relay sites are a ledger whose op set is derived from the consumer's own source; a known gap is pinned with the fix it waits on and fails the day the grant lands |
| `lint-seed-declared-reads` | A `scripts/` seed or verify tool that dispatches an op declares the optional reads the op's `OpMetaSpec.Dispatch.OptionalReads` declares — the op's refusals read them, so an envelope that stopped tracking the descriptor is a fixture the op may now refuse and a must-accept helper turns a designed refusal into an aborted seed; self-scope probes and probes keyed on optional payload fields are not required, and a non-literal `OptionalReads` is listed (`VERBOSE=1`), not judged |
| `lint-opmeta-required-fields` | A payload field a package script refuses without — a requirer call (`required_string(p, "memo")`, derived from the helpers' own refuse-on-absence bodies) at the top level of the op's dispatch block or one helper hop below it — is listed under `required` by the op-meta's InputSchema, or filled by the descriptor itself (`contextParams` / `targetField`); the script and the descriptor are two declarations of one op, and a refusal added to one while the other still says optional renders the field optional and fails a blank submit with a raw InvalidArgument |
| `lint-ceremony-throw-path` | In every vertical FE, the catch beside a ceremony op's submit (an op-meta with a Ceremony spec — the client shows a minted secret exactly once — or any generic dispatcher that calls `revealCeremonySecret`) never asserts the write did not land: the transport throws on a 5xx after the Processor may have committed, so assertive wording ("could not", "failed to") must carry the landed-ambiguity vocabulary ("may have landed" / "not confirmed"); the write transports are derived from the file (the function passing fetch a non-GET method, and its callers), and the JavaScript is read through goja's parser |
| `lint-stale-render-guard` | An async renderer that re-checks "am I still current" after an await re-checks it on every path out of that await before anything it paints — a `try` followed by `if (<test>) return;` opens its catch with the same guard, and a function capturing `const g = ++counter` re-checks `g !== counter` between every await and the next statement that could paint; read through goja's parser over every vertical FE |
| `lint-markup-escaping` | Every value a vertical FE writes into markup (`innerHTML` / `outerHTML` / `insertAdjacentHTML`) is a literal, a call to the file's escaper (derived — the function mapping `&` to `&amp;`), or derived from those — a file-defined builder whose every `return` is safe, a numeric formatter, a `.map`/`.join` chain over a safe receiver, a local fed only by safe values; a member read, a parameter or an unresolved call fails at its line, and `// markup-safe: <why>` declares a stripped key; read through goja's parser |
| `lint-link-target-count` | A `len(<link-target list>)` comparison in a package script counts vertices screened for liveness — a soft-delete cascades onto no link, so a live link to a dead endpoint is otherwise a live candidate in an exactly-one / ambiguity decision; parses every shipped script with `go.starlark.net/syntax`, resolves target-list producers transitively, binds the liveness test (`vertex_live` / `vertex_alive` / a `kv.Read` document's `isDeleted`) to the counted element and its branch, and `# link-count: live-screened <why>` declares a screening the recogniser cannot see |
| `lint-links-page-limit` | Every `kv.Links(...)` call in a package script states an explicit page limit — the 5th positional argument or a `limit=` keyword — because starlark_kv.go charges the clamped limit (default 256) against the script's live-read budget regardless of how many links the hub actually carries, so an unlimited call inside a per-candidate loop turns a long-history hub into a permanently rejected, endlessly re-dispatched evaluation; parses every shipped script with `go.starlark.net/syntax`, resolves the limit to an integer literal, a top-level `_PAGE_LIMIT` constant, or a helper's own parameter, and fails a missing limit or a bare literal at or above the engine default (256) — a named constant resolving that high is exempt |
| `lint-derive-reads-bare-vector` | Every op a DDL script's `derive_reads` dispatches on has, in its own package, at least one `OperationEnvelope{…}` submission — INSIDE a top-level test function named `Test*UndeclaredSubmitter*` — declaring no `Reads`, `OptionalReads` AND `EgressReads` — proof the derivation, not the submitter, carries the op — because `contextHint` is submitter-supplied and the descriptor floor only demotes a required read to optional, never adds one, so a key `derive_reads` does not return is hydrated only when the submitter's own declaration names it, and a caller that forgets loses the Contract #3 §3.2 auto-conditioning a bare update needs (café `CreditCafeAccount`, 2026-09-05; lease-signing `TombstoneSupersededLeaseServiceInstance`, 2026-09-13, second sighting); parses every shipped package script for a top-level `derive_reads` (matched by name alone, any arity) and the op names its comparisons resolve to literals or module-level string/list constants — an unresolved comparison, or none at all, governs every op the DDL permits rather than silently under-approving — then walks the matching test functions for the bare-vector literal. An `Enumerations`-only `ContextHint` still counts, since a live `kv.Links` walk is a caller-declared class (e) channel `derive_reads` never populates; a non-empty `EgressReads` does NOT count as bare, since step 4 hydrates it exactly like a declared read (step4_hydrate.go:459-500). The function-name restriction (a 2026-09-14 cold-review fix) closes a phantom-vector hole: a bare literal inside a test asserting something else entirely (a malformed-payload refusal whose `derive_reads` short-circuits to `{}`) no longer counts, and a helper-built envelope (the value not a literal at the call site, or a literal sitting inside a non-`Test*UndeclaredSubmitter*` helper function) stays invisible to the check either way |
| `lint-live-read-pinned-mutation` | A `kv.Read` whose read-posture is class (e)/(c), or unannotated (class-(b) debt) — the three shapes step 4 never hydrates — or a `kv.Links(...)` page entry's own `.key` (it carries a `.revision` just as live, `starlark_kv.go:317`), followed, reachably in program order, by an `update`/`tombstone` on the same key with no `expectedRevision` is a lost-update race: `applyHydratedRevisions` only conditions a bare mutation on a key in the hydrated set, so this one stays unconditioned and the write lands over whatever changed since the read (identity-domain `CreateUnclaimedIdentity`, 2026-08-15, re-found repeatedly through 2026-09-14, incl. wellness-domain's per-occurrence session tombstone and a page-entry class across eight further sites); parses every shipped script (and every nested `def`) with `go.starlark.net/syntax`, resolves each read's class from ONLY an annotation whose own anchor is that read's statement (an inherited indentation-block span never hydrates it), and tracks live reads/mutations through a control-flow- and alias-aware, program-order scan (a branch that unconditionally exits — `continue`/`break`/`return`/`fail` — never hands its reads to a sibling statement; a `name = <expr>` alias resolves a key the same way a `+`/single-`%s` concatenation does, structurally rendered so two differently-shaped expressions never collide into a false match; a branch disagreement over what a name resolves to, or a name carried out of a loop, errs toward a finding by matching against every binding either path could produce, rather than silencing the match). A read routed through a READ HELPER — any `def` whose own body performs the live read on a key built purely from its own parameters (`vertex_live(key)`) — IS modelled, at the helper's call site; a mutation routed through any such HELPER (no naming convention required) is too, including one delegating to another; a `param=None`-defaulted revision argument the call omits, or passes an explicit `None` for, is BARE, not given the benefit of the doubt. NOT modelled: a read/mutation crossing into a different top-level/nested def any other way; a key built by anything other than a `+` chain or a single-`%s` `%` format; a helper reached only via `.extend(...)`. A conditional pin shape OTHER than the one recognized (`if <param> != None: ...` on a `param=None` default) is treated as unconditionally BARE, not unmodelled. `# occ: live-unpinned <why>` is the declared escape hatch — anchored to a `def`/`if`/`for` header rather than a simple statement, it is refused as a finding of its own and grants no exemption |

## The author-declares shape

Several gates share one design: rather than trying to classify a site, they **default-deny** it and require
the author to declare which sanctioned shape it is, in a machine-checked annotation carrying a
human-written `<why>`. `# read-posture:` is the original; `# authcontext-target:`, `# workplace-exempt:`,
`# op-name:` and `# link-count:` follow it. The payoff is that the gate never has to be smarter than the author — it only
has to make the author say what they meant, where the next reader will see it.

Two properties make one of these work, and both have been got wrong:

- **A declaration binds to ONE thing.** `annotationSpans` binds an annotation to the statement it
  introduces and that statement's own block. A declaration covering several subjects is a blanket: one
  sentence written about one of them vouches for the rest.
- **A required sub-field must be resolved, not counted.** `read-posture (e)`'s `relation=` and
  `op-name (policy)`'s `pin=` name something that must exist. A sub-field checked for presence alone
  admits a value naming nothing, which reads exactly like one that holds.

## Review keeps catching (dossier)

Same contract as every dossier: fire briefs copy the applicable entries into part 5
(`agents/fire-brief-template.md`); the item-close review appends new ones (`agents/steward/SKILL.md` §4);
**capped at 12 one-liners**; an entry retires when a lint/test gate mechanizes it.

- **A default-deny gate must key on the HAZARD, not on a proxy for it — and the proxy always looks
  equivalent while you are writing it.** Minted three times in one fire (`op-name`, 2026-08-28), each
  passing its own tests and each failing at the exact site it was written for. Liveness keyed on the
  annotation's **text** rather than its anchor: two byte-identical declarations vouched for each other, so
  the rename half went silent on the very pair the gate was designed around. A blanket rule keyed on
  **block syntax** (`does the anchor line end in "{"`) rather than on how many subjects the span covers:
  it denied `if op.OperationType != "X" {`, which is the `(policy)` category's own defining shape, making
  the category unannotatable and driving a contributor to restructure working code to satisfy a lint. And
  `pin=` keyed on **substring presence** rather than resolution: `pin=` and `pin=TestThatNeverExisted`
  both passed. Check: for each rule, write down the hazard in one sentence, then ask what the code
  actually tests — if the answer is a syntactic stand-in, find the input where the two diverge, because a
  reviewer will.
- **A derived set must resolve the language's indirection, or it rots on day one exactly like the hand
  list it replaced.** `op-name`'s universe scraped string literals out of `PermittedCommands` and missed
  the eleven operations two reminder packages declare through Go constants — 6% short, and 100% short for
  those packages, silently permitting every one of those names anywhere in the tree. Deriving is only
  honest if it goes through `go/ast`. Check: whatever a declaration site is *allowed* to contain by the
  language, the deriver has to handle or explicitly refuse; count the derived set and reconcile it against
  an independent census before trusting it. Corollary, from the same fix: an identifier index must be
  scoped to the Go package that declares it — two packages bound `reminderOp` to different operations, and
  a flat index would have attributed one vertical's verb to the other.
- **Scoping-out is as silent as scoping-in, and only one of them is safe.** A path spelled
  `./internal/x.go` or absolutely matched no prefix in `scanSource`, so every prefix-scoped rule reported
  nothing and the run ended "0 issues" — indistinguishable from a clean file. Check: normalize the path
  before the scope tests, and prove a known-bad fixture trips under every spelling a caller might use.
- **A gate's self-test must prove its positive vector reaches the gate.** Shared with the Processor
  dossier, and it is what makes the deny cases here worth anything: pair every "denied" fixture with an
  otherwise-identical annotated one in the same in-scope path, so a case cannot pass because scoping
  silently excluded it.
- **Before deriving a set the runtime already reads, find the sibling that derives it — and read the
  WHOLE function that defines the hazard, not the leg you went in for.** `lint-gap-column-declaration`
  keyed on the lens's `Output.BodyColumns`, having read `projection/driver.go:70-72`, and its header
  argued at length that this was the hazard rather than a proxy for it. The row body is actually the
  union with `Output.StaticEmptyColumns` — written by the SAME function twenty lines further down
  (`:125-130`) — so a `missing_*` name declared only in the second list passed the gate and is a
  present key at runtime. `internal/pkgmgr/orchestrationguard.go:434-441`, the installer's companion-pair
  check sitting 200 lines from the type being gated, states the union verbatim and computes it in a
  reusable helper. Minted 2026-08-29, alongside its twin in the same gate: `LensRef` was a proxy for the
  positional `<targetId>.` key-prefix binding that `definition.go:336-338` names outright ("lane-1
  dispatch watches weaver-targets directly, not via LensRef"), so a second lens rendering that prefix
  fed the target's rows unchecked. Check: for every set a gate derives, grep for an existing consumer or
  sibling gate that derives the same set and diff the two definitions before writing your own; read the
  producing function to its end; and where the binding is positional, key on the position.
- **A gate that SKIPS what it cannot parse fails OPEN — an unreadable candidate is a finding, not a pass.** The
  `validator-lens-resolver` pin counted a call's arguments by splitting on commas and `continue`d on any count it did
  not expect, so gofmt's own trailing comma, a `//` comment with an apostrophe inside the argument list, a typed-nil
  conversion, an identifier declared `var x T` and never assigned, and an aliased import each slipped past a rule whose
  whole job was to catch an unwired nil. Minted: weaverTarget gap-declaration holder, cold review (2026-09-13). Check:
  when a rule's reader cannot parse a candidate it EMITS ("cannot read the argument list — write it in a shape the gate
  can read"); enumerate every spelling the language allows for the hazard value (`nil`, `T(nil)`, a never-assigned
  declaration, an alias) and give each a denied fixture beside its allowed twin.
