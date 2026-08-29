# Lint gates (`scripts/lint-*.go`)

The repo's static gates. Each is a standalone `//go:build ignore` Go program run by CI's `lint-build` job
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
| `lint-gap-column-declaration` | Every `missing_*` column that lands in a weaver target's rows is declared in that target's `gaps` map |
| `lint-board` | The backlog is an index, not a journal |

## The author-declares shape

Several gates share one design: rather than trying to classify a site, they **default-deny** it and require
the author to declare which sanctioned shape it is, in a machine-checked annotation carrying a
human-written `<why>`. `# read-posture:` is the original; `# authcontext-target:`, `# workplace-exempt:`
and `# op-name:` follow it. The payoff is that the gate never has to be smarter than the author — it only
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
